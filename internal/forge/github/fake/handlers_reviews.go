package fake

import (
	"net/http"
	"slices"
	"strings"
	"time"
)

func (s *Server) reviewRoutes(mux *http.ServeMux) {
	const r = "/repos/{owner}/{repo}/pulls"
	s.handle(mux, "GET "+r+"/{n}/reviews", s.listReviews)
	s.handle(mux, "POST "+r+"/{n}/reviews", s.createReview)
	s.handle(mux, "GET "+r+"/{n}/reviews/{id}", s.getReview)
	s.handle(mux, "PUT "+r+"/{n}/reviews/{id}", s.updateReview)
	s.handle(mux, "DELETE "+r+"/{n}/reviews/{id}", s.deleteReview)
	s.handle(mux, "POST "+r+"/{n}/reviews/{id}/events", s.submitReview)
	s.handle(mux, "PUT "+r+"/{n}/reviews/{id}/dismissals", s.dismissReview)
	s.handle(mux, "GET "+r+"/{n}/reviews/{id}/comments", s.listReviewComments)
	s.handle(mux, "GET "+r+"/{n}/comments", s.listPullComments)
	s.handle(mux, "POST "+r+"/{n}/comments", s.createPullComment)
	s.handle(mux, "DELETE "+r+"/comments/{id}", s.deletePullComment)
}

var reviewEvents = map[string]string{"APPROVE": stateApproved, "REQUEST_CHANGES": stateChanges, "COMMENT": stateCommented}

// unprocessable answers 422 in the shape GitHub uses for state conflicts, as
// opposed to field validation: errors are plain strings.
func unprocessable(w http.ResponseWriter, msgs ...string) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"message": "Unprocessable Entity", "errors": msgs, "documentation_url": docURL,
	})
}

// anchorProblem reports why a comment cannot be anchored at path:line on the
// given side of the diff: "path", "line", or "" when it can.
func (p *pull) anchorProblem(path string, line int, side string) string {
	for _, f := range p.Files {
		if f.Filename != path && (side != "LEFT" || f.Previous != path) {
			continue
		}
		left, right := diffLines(f.Patch)
		lines := right
		if side == "LEFT" {
			lines = left
		}
		if lines[line] {
			return ""
		}
		return "line"
	}
	return "path"
}

// anchorError is the REST validation error for an anchoring problem.
func anchorError(problem string) fieldError {
	field := "pull_request_review_thread." + problem
	msg := field + " must be part of the diff"
	if problem == "path" {
		msg = field + " diff must exist"
	}
	return fieldError{Resource: "PullRequestReviewComment", Field: field, Code: "custom", Message: msg}
}

// newThread opens a thread with one comment inside review rv.
func (st *state) newThread(p *pull, rv *review, path string, line, startLine int, side, body string, at time.Time) *reviewComment {
	t := &thread{ID: st.id(), Path: path, Line: line, StartLn: startLine, Side: side}
	c := &reviewComment{User: rv.User, Body: body, Review: rv, Created: at, Updated: at}
	st.appendToThread(t, c)
	p.Threads = append(p.Threads, t)
	return c
}

// verdictProblem is why the viewer cannot give the verdict on their own PR.
func verdictProblem(it *item, state string) string {
	switch {
	case it.Author != viewer:
		return ""
	case state == stateApproved:
		return "Can not approve your own pull request"
	case state == stateChanges:
		return "Can not request changes on your own pull request"
	}
	return ""
}

func (p *pull) commentCount(rv *review) int {
	n := 0
	for _, t := range p.Threads {
		for _, c := range t.Comments {
			if c.Review == rv {
				n++
			}
		}
	}
	return n
}

// reviewOf resolves the PR and the visible review {id} of a route.
func (s *Server) reviewOf(w http.ResponseWriter, r *http.Request) (*repo, *item, *review) {
	rp, it := s.pullOf(w, r)
	if it == nil {
		return nil, nil, nil
	}
	rv := it.PR.review(atoi64(r.PathValue("id")))
	if rv == nil {
		notFound(w)
		return nil, nil, nil
	}
	return rp, it, rv
}

func (s *Server) listReviews(w http.ResponseWriter, r *http.Request) {
	rp, it := s.pullOf(w, r)
	if it == nil {
		return
	}
	paged(s, w, r, it.PR.visibleReviews(), func(rv *review) any { return s.renderReview(rp, it, rv) })
}

func (s *Server) getReview(w http.ResponseWriter, r *http.Request) {
	if rp, it, rv := s.reviewOf(w, r); rv != nil {
		writeJSON(w, http.StatusOK, s.renderReview(rp, it, rv))
	}
}

type reviewCommentIn struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line"`
	Body      string `json:"body"`
}

func (s *Server) createReview(w http.ResponseWriter, r *http.Request) {
	rp, it := s.pullOf(w, r)
	if it == nil {
		return
	}
	var in struct {
		CommitID string            `json:"commit_id"`
		Body     string            `json:"body"`
		Event    string            `json:"event"`
		Comments []reviewCommentIn `json:"comments"`
	}
	if !decode(w, r, &in) {
		return
	}
	p := it.PR
	state := statePending
	if in.Event != "" {
		var ok bool
		if state, ok = reviewEvents[in.Event]; !ok {
			validation(w, fieldError{Resource: "PullRequestReview", Field: "event", Code: "invalid"})
			return
		}
	}
	switch {
	case in.CommitID != "" && in.CommitID != p.HeadSHA:
		validation(w, fieldError{Resource: "PullRequestReview", Field: "commit_id", Code: "invalid"})
		return
	case state == statePending && p.pendingOf(viewer) != nil:
		unprocessable(w, "User can only have one pending review per pull request")
		return
	}
	if msg := verdictProblem(it, state); msg != "" {
		unprocessable(w, msg)
		return
	}
	if state != statePending && state != stateApproved && in.Body == "" && len(in.Comments) == 0 {
		validation(w, fieldError{Resource: "PullRequestReview", Field: "body", Code: "custom", Message: "Body can't be blank"})
		return
	}
	for i := range in.Comments {
		c := &in.Comments[i]
		if c.Side == "" {
			c.Side = "RIGHT"
		}
		if c.Body == "" {
			validation(w, fieldError{Resource: "PullRequestReviewComment", Field: "body", Code: "missing_field"})
			return
		}
		if prob := p.anchorProblem(c.Path, c.Line, c.Side); prob != "" {
			validation(w, anchorError(prob))
			return
		}
	}
	at := s.st.now()
	rv := s.st.addReview(p, viewer, state, in.Body, at)
	for _, c := range in.Comments {
		s.st.newThread(p, rv, c.Path, c.Line, c.StartLine, c.Side, c.Body, at)
	}
	if state != statePending {
		p.Publishes++
	}
	it.Updated = at
	writeJSON(w, http.StatusOK, s.renderReview(rp, it, rv))
}

func (s *Server) updateReview(w http.ResponseWriter, r *http.Request) {
	rp, it, rv := s.reviewOf(w, r)
	if rv == nil {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch {
	case rv.State != statePending:
		unprocessable(w, "Can not update a non-pending pull request review")
	case in.Body == "":
		validation(w, fieldError{Resource: "PullRequestReview", Field: "body", Code: "missing_field"})
	default:
		rv.Body = in.Body
		it.Updated = s.st.now()
		writeJSON(w, http.StatusOK, s.renderReview(rp, it, rv))
	}
}

func (s *Server) deleteReview(w http.ResponseWriter, r *http.Request) {
	rp, it, rv := s.reviewOf(w, r)
	if rv == nil {
		return
	}
	if rv.State != statePending {
		unprocessable(w, "Can not delete a non-pending pull request review")
		return
	}
	out := s.renderReview(rp, it, rv)
	it.PR.dropReview(rv)
	it.Updated = s.st.now()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) submitReview(w http.ResponseWriter, r *http.Request) {
	rp, it, rv := s.reviewOf(w, r)
	if rv == nil {
		return
	}
	var in struct {
		Event string `json:"event"`
		Body  string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	state, ok := reviewEvents[in.Event]
	if !ok {
		validation(w, fieldError{Resource: "PullRequestReview", Field: "event", Code: "invalid"})
		return
	}
	body := rv.Body
	if in.Body != "" {
		body = in.Body
	}
	switch {
	case rv.State != statePending:
		unprocessable(w, "Can not submit a non-pending pull request review")
		return
	case verdictProblem(it, state) != "":
		unprocessable(w, verdictProblem(it, state))
		return
	case state != stateApproved && body == "" && it.PR.commentCount(rv) == 0:
		validation(w, fieldError{Resource: "PullRequestReview", Field: "body", Code: "custom", Message: "Body can't be blank"})
		return
	}
	at := s.st.now()
	rv.Body, rv.State, rv.Submitted = body, state, at
	it.PR.Publishes++
	it.Updated = at
	writeJSON(w, http.StatusOK, s.renderReview(rp, it, rv))
}

func (s *Server) dismissReview(w http.ResponseWriter, r *http.Request) {
	rp, it, rv := s.reviewOf(w, r)
	if rv == nil {
		return
	}
	var in struct {
		Message string `json:"message"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch {
	case in.Message == "":
		validation(w, fieldError{Resource: "PullRequestReview", Field: "message", Code: "missing_field"})
	case rv.State != stateApproved && rv.State != stateChanges:
		unprocessable(w, "Can not dismiss a "+strings.ToLower(rv.State)+" pull request review")
	default:
		rv.State = stateDismissed
		it.Updated = s.st.now()
		writeJSON(w, http.StatusOK, s.renderReview(rp, it, rv))
	}
}

// reviewComments lists the visible comments of a PR in thread order,
// optionally limited to one review.
func (s *Server) reviewComments(rp *repo, it *item, only *review) []any {
	var out []any
	for _, t := range it.PR.Threads {
		for _, c := range t.visible() {
			if only == nil || c.Review == only {
				out = append(out, s.renderReviewComment(rp, it, t, c))
			}
		}
	}
	return out
}

func (s *Server) listReviewComments(w http.ResponseWriter, r *http.Request) {
	rp, it, rv := s.reviewOf(w, r)
	if rv == nil {
		return
	}
	paged(s, w, r, s.reviewComments(rp, it, rv), func(c any) any { return c })
}

func (s *Server) listPullComments(w http.ResponseWriter, r *http.Request) {
	rp, it := s.pullOf(w, r)
	if it == nil {
		return
	}
	paged(s, w, r, s.reviewComments(rp, it, nil), func(c any) any { return c })
}

// createPullComment posts an immediate (non-pending) line comment, opening a
// thread or replying. As on GitHub it is its own one-comment review.
func (s *Server) createPullComment(w http.ResponseWriter, r *http.Request) {
	rp, it := s.pullOf(w, r)
	if it == nil {
		return
	}
	var in struct {
		reviewCommentIn
		CommitID  string `json:"commit_id"`
		InReplyTo int64  `json:"in_reply_to"`
	}
	if !decode(w, r, &in) {
		return
	}
	p := it.PR
	if in.Side == "" {
		in.Side = "RIGHT"
	}
	var t *thread
	switch {
	case in.Body == "":
		validation(w, fieldError{Resource: "PullRequestReviewComment", Field: "body", Code: "missing_field"})
		return
	case in.InReplyTo != 0:
		if t, _ = p.comment(in.InReplyTo); t == nil {
			validation(w, fieldError{Resource: "PullRequestReviewComment", Field: "in_reply_to", Code: "invalid"})
			return
		}
	case in.Path == "" || in.CommitID == "" || in.Line == 0:
		validation(w, fieldError{Resource: "PullRequestReviewComment", Field: "path", Code: "missing_field"})
		return
	case in.CommitID != p.HeadSHA:
		validation(w, fieldError{Resource: "PullRequestReviewComment", Field: "commit_id", Code: "invalid"})
		return
	}
	if t == nil {
		if prob := p.anchorProblem(in.Path, in.Line, in.Side); prob != "" {
			validation(w, anchorError(prob))
			return
		}
	}
	at := s.st.now()
	rv := s.st.addReview(p, viewer, stateCommented, "", at)
	var c *reviewComment
	if t != nil {
		c = &reviewComment{User: viewer, Body: in.Body, Review: rv, Created: at, Updated: at}
		s.st.appendToThread(t, c)
	} else {
		c = s.st.newThread(p, rv, in.Path, in.Line, in.StartLine, in.Side, in.Body, at)
		t = p.Threads[len(p.Threads)-1]
	}
	it.Updated = at
	writeJSON(w, http.StatusCreated, s.renderReviewComment(rp, it, t, c))
}

func (s *Server) deletePullComment(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	id := atoi64(r.PathValue("id"))
	for _, it := range rp.Items {
		if it.PR == nil {
			continue
		}
		t, c := it.PR.comment(id)
		if c == nil {
			continue
		}
		if c.User != viewer {
			apiError(w, http.StatusForbidden, "Must have admin rights to Repository.")
			return
		}
		t.Comments = slices.DeleteFunc(t.Comments, func(x *reviewComment) bool { return x == c })
		it.PR.Threads = slices.DeleteFunc(it.PR.Threads, func(x *thread) bool { return len(x.Comments) == 0 })
		it.Updated = s.st.now()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	notFound(w)
}
