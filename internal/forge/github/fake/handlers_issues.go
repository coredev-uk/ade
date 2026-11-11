package fake

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

func (s *Server) issueRoutes(mux *http.ServeMux) {
	const r = "/repos/{owner}/{repo}"
	s.handle(mux, "GET "+r+"/issues", s.listIssues)
	s.handle(mux, "POST "+r+"/issues", s.createIssue)
	s.handle(mux, "GET "+r+"/issues/{n}", s.getIssue)
	s.handle(mux, "POST "+r+"/issues/{n}/labels", s.addLabels)
	s.handle(mux, "DELETE "+r+"/issues/{n}/labels/{name}", s.removeLabel)
	s.handle(mux, "GET "+r+"/issues/{n}/sub_issues", s.listSubIssues)
	s.handle(mux, "POST "+r+"/issues/{n}/sub_issues", s.addSubIssue)
	s.handle(mux, "GET "+r+"/issues/{n}/parent", s.getParent)
	s.handle(mux, "GET "+r+"/issues/{n}/comments", s.listComments)
	s.handle(mux, "POST "+r+"/issues/{n}/comments", s.createComment)
	s.handle(mux, "GET "+r+"/issues/{n}/events", s.listIssueEvents)
	s.handle(mux, "GET "+r+"/events", s.listFeed)
}

// issueOf resolves {owner}/{repo}/issues/{n}; PR numbers resolve too, as on
// GitHub, where every PR is also an issue.
func (s *Server) issueOf(w http.ResponseWriter, r *http.Request) (*repo, *item) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return nil, nil
	}
	n, _ := strconv.Atoi(r.PathValue("n"))
	it := rp.item(n)
	if it == nil {
		notFound(w)
		return nil, nil
	}
	return rp, it
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	since, _ := time.Parse(time.RFC3339, q.Get("since"))
	labels := queryList(r, "labels")
	assignee, milestone := q.Get("assignee"), q.Get("milestone")
	var out []*item
	for _, it := range rp.Items {
		switch {
		case state != "all" && it.State != state:
		case assignee == "none" && len(it.Assignees) > 0:
		case assignee == "*" && len(it.Assignees) == 0:
		case assignee != "" && assignee != "none" && assignee != "*" && !slices.Contains(it.Assignees, assignee):
		case milestone == "none" && it.Milestone != 0:
		case milestone == "*" && it.Milestone == 0:
		case milestone != "" && milestone != "none" && milestone != "*" && strconv.Itoa(it.Milestone) != milestone:
		case q.Get("creator") != "" && it.Author != q.Get("creator"):
		case !since.IsZero() && it.Updated.Before(since):
		case slices.ContainsFunc(labels, func(l string) bool { return !slices.Contains(it.Labels, l) }):
		default:
			out = append(out, it)
		}
	}
	slices.SortStableFunc(out, func(a, b *item) int {
		var c int
		switch q.Get("sort") {
		case "updated":
			c = a.Updated.Compare(b.Updated)
		case "comments":
			c = len(a.Comments) - len(b.Comments)
		default:
			c = a.Created.Compare(b.Created)
		}
		if c == 0 {
			c = a.Number - b.Number
		}
		return c
	})
	if q.Get("direction") != "asc" {
		slices.Reverse(out)
	}
	paged(s, w, r, out, func(it *item) any { return s.renderIssue(rp, it) })
}

func (s *Server) getIssue(w http.ResponseWriter, r *http.Request) {
	if rp, it := s.issueOf(w, r); it != nil {
		writeJSON(w, http.StatusOK, s.renderIssue(rp, it))
	}
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	var in struct {
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		Labels    []any    `json:"labels"`
		Assignees []string `json:"assignees"`
		Milestone any      `json:"milestone"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Title) == "" {
		validation(w, fieldError{Resource: "Issue", Field: "title", Code: "missing_field"})
		return
	}
	var ms *milestone
	if in.Milestone != nil {
		n, _ := in.Milestone.(float64)
		if ms = rp.milestone(int(n)); ms == nil {
			validation(w, fieldError{Resource: "Issue", Field: "milestone", Code: "invalid"})
			return
		}
	}
	at := s.st.now()
	it := s.st.addItem(rp, in.Title, in.Body, viewer, at)
	s.st.setLabels(rp, it, viewer, labelNames(in.Labels), at)
	for _, a := range in.Assignees { // unknown logins are dropped, as on GitHub
		if u := s.st.user(a); u != nil && u.Type != "Organization" && !slices.Contains(it.Assignees, u.Login) {
			it.Assignees = append(it.Assignees, u.Login)
			s.st.addEvent(it, "assigned", viewer, at).Assignee = u.Login
		}
	}
	if ms != nil {
		it.Milestone = ms.Number
		s.st.addEvent(it, "milestoned", viewer, at).Milestone = ms.Title
	}
	s.st.feedItem(rp, it, "opened", viewer, at)
	writeJSON(w, http.StatusCreated, s.renderIssue(rp, it))
}

// labelNames reads label names given as strings or {"name": ...} objects.
func labelNames(in []any) []string {
	var out []string
	for _, v := range in {
		switch l := v.(type) {
		case string:
			out = append(out, l)
		case map[string]any:
			if n, ok := l["name"].(string); ok {
				out = append(out, n)
			}
		}
	}
	return out
}

func (s *Server) addLabels(w http.ResponseWriter, r *http.Request) {
	rp, it := s.issueOf(w, r)
	if it == nil {
		return
	}
	// The body is {"labels": [...]} or a bare array.
	var raw any
	if !decode(w, r, &raw) {
		return
	}
	var names []string
	switch v := raw.(type) {
	case map[string]any:
		list, _ := v["labels"].([]any)
		names = labelNames(list)
	case []any:
		names = labelNames(v)
	}
	if len(names) == 0 {
		validation(w, fieldError{Resource: "Issue", Field: "labels", Code: "missing_field"})
		return
	}
	at := s.st.now()
	if len(s.st.setLabels(rp, it, viewer, names, at)) > 0 {
		it.Updated = at
	}
	writeJSON(w, http.StatusOK, s.renderLabels(rp, it.Labels))
}

func (s *Server) removeLabel(w http.ResponseWriter, r *http.Request) {
	rp, it := s.issueOf(w, r)
	if it == nil {
		return
	}
	name := r.PathValue("name")
	i := slices.IndexFunc(it.Labels, func(l string) bool { return strings.EqualFold(l, name) })
	if i < 0 {
		apiError(w, http.StatusNotFound, "Label does not exist")
		return
	}
	at := s.st.now()
	s.st.addEvent(it, "unlabeled", viewer, at).Label = it.Labels[i]
	it.Labels = slices.Delete(it.Labels, i, i+1)
	it.Updated = at
	writeJSON(w, http.StatusOK, s.renderLabels(rp, it.Labels))
}

func (s *Server) listSubIssues(w http.ResponseWriter, r *http.Request) {
	_, it := s.issueOf(w, r)
	if it == nil {
		return
	}
	paged(s, w, r, it.Subs, func(k itemKey) any {
		rp, sub := s.st.lookup(k)
		return s.renderIssue(rp, sub)
	})
}

func (s *Server) addSubIssue(w http.ResponseWriter, r *http.Request) {
	rp, parent := s.issueOf(w, r)
	if parent == nil {
		return
	}
	var in struct {
		SubIssueID    int64 `json:"sub_issue_id"`
		ReplaceParent bool  `json:"replace_parent"`
	}
	if !decode(w, r, &in) {
		return
	}
	subRepo, sub := s.st.itemByID(in.SubIssueID)
	if sub == nil {
		notFound(w)
		return
	}
	self := itemKey{rp.Name, parent.Number}
	key := itemKey{subRepo.Name, sub.Number}
	fail := func(msg string) {
		validation(w, fieldError{Resource: "Issue", Field: "sub_issue_id", Code: "custom", Message: msg})
	}
	switch {
	case sub.PR != nil || parent.PR != nil:
		fail("Pull requests cannot be sub-issues")
		return
	case key == self:
		fail("An issue cannot be its own sub-issue")
		return
	case sub.Parent != nil && *sub.Parent == self:
		fail("Issue may not contain duplicate sub-issues")
		return
	case sub.Parent != nil && !in.ReplaceParent:
		fail("Only one parent issue allowed")
		return
	}
	at := s.st.now()
	if sub.Parent != nil {
		if _, old := s.st.lookup(*sub.Parent); old != nil {
			old.Subs = slices.DeleteFunc(old.Subs, func(k itemKey) bool { return k == key })
			old.Updated = at
		}
	}
	sub.Parent = &self
	parent.Subs = append(parent.Subs, key)
	parent.Updated, sub.Updated = at, at
	writeJSON(w, http.StatusCreated, s.renderIssue(rp, parent))
}

func (s *Server) getParent(w http.ResponseWriter, r *http.Request) {
	_, it := s.issueOf(w, r)
	if it == nil {
		return
	}
	if it.Parent == nil {
		notFound(w)
		return
	}
	prp, parent := s.st.lookup(*it.Parent)
	writeJSON(w, http.StatusOK, s.renderIssue(prp, parent))
}

func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	rp, it := s.issueOf(w, r)
	if it == nil {
		return
	}
	paged(s, w, r, it.Comments, func(c *comment) any { return s.renderComment(rp, it, c) })
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	rp, it := s.issueOf(w, r)
	if it == nil {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Body) == "" {
		validation(w, fieldError{Resource: "IssueComment", Field: "body", Code: "missing_field"})
		return
	}
	c := s.st.addComment(it, viewer, in.Body, s.st.now())
	s.st.feedComment(rp, it, c)
	writeJSON(w, http.StatusCreated, s.renderComment(rp, it, c))
}

func (s *Server) listIssueEvents(w http.ResponseWriter, r *http.Request) {
	rp, it := s.issueOf(w, r)
	if it == nil {
		return
	}
	evs := slices.Clone(it.Events)
	slices.SortStableFunc(evs, func(a, b *issueEvent) int { return a.Created.Compare(b.Created) })
	paged(s, w, r, evs, func(ev *issueEvent) any { return s.renderIssueEvent(rp, ev) })
}

func (s *Server) listFeed(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	evs := slices.Clone(rp.Feed)
	slices.SortStableFunc(evs, func(a, b *feedEvent) int {
		if c := b.Created.Compare(a.Created); c != 0 {
			return c
		}
		return int(b.ID - a.ID)
	})
	paged(s, w, r, evs, func(ev *feedEvent) any { return s.renderFeedEvent(rp, ev) })
}
