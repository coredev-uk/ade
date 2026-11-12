package fake

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

func (s *Server) pullRoutes(mux *http.ServeMux) {
	const r = "/repos/{owner}/{repo}"
	s.handle(mux, "GET "+r+"/pulls", s.listPulls)
	s.handle(mux, "POST "+r+"/pulls", s.createPull)
	s.handle(mux, "GET "+r+"/pulls/{n}", s.getPull)
	s.handle(mux, "GET "+r+"/pulls/{n}/files", s.listPullFiles)
	s.handle(mux, "PUT "+r+"/pulls/{n}/merge", s.mergePull)
	s.handle(mux, "POST "+r+"/pulls/{n}/requested_reviewers", s.requestReviewers)
}

// pullOf resolves {owner}/{repo}/pulls/{n}; an issue number is a 404 here.
func (s *Server) pullOf(w http.ResponseWriter, r *http.Request) (*repo, *item) {
	rp, it := s.issueOf(w, r)
	if it == nil {
		return nil, nil
	}
	if it.PR == nil {
		notFound(w)
		return nil, nil
	}
	return rp, it
}

func (s *Server) listPulls(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	headOwner, headBranch, hasOwner := strings.Cut(q.Get("head"), ":")
	if !hasOwner {
		headOwner, headBranch = "", q.Get("head")
	}
	var out []*item
	for _, it := range rp.Items {
		p := it.PR
		switch {
		case p == nil:
		case state != "all" && it.State != state:
		case headBranch != "" && p.Head != headBranch:
		case headOwner != "" && !strings.EqualFold(headOwner, rp.Owner):
		case q.Get("base") != "" && p.Base != q.Get("base"):
		default:
			out = append(out, it)
		}
	}
	slices.SortStableFunc(out, func(a, b *item) int {
		c := a.Created.Compare(b.Created)
		if q.Get("sort") == "updated" {
			c = a.Updated.Compare(b.Updated)
		}
		if c == 0 {
			c = a.Number - b.Number
		}
		return c
	})
	if q.Get("direction") != "asc" {
		slices.Reverse(out)
	}
	paged(s, w, r, out, func(it *item) any { return s.renderPull(rp, it, false) })
}

func (s *Server) getPull(w http.ResponseWriter, r *http.Request) {
	if rp, it := s.pullOf(w, r); it != nil {
		writeJSON(w, http.StatusOK, s.renderPull(rp, it, true))
	}
}

func (s *Server) createPull(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	var in struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
		Draft bool   `json:"draft"`
	}
	if !decode(w, r, &in) {
		return
	}
	head := in.Head
	if _, b, ok := strings.Cut(in.Head, ":"); ok {
		head = b
	}
	headSHA, baseSHA := rp.Branches[head], rp.Branches[in.Base]
	switch {
	case strings.TrimSpace(in.Title) == "":
		validation(w, fieldError{Resource: "PullRequest", Field: "title", Code: "missing_field"})
		return
	case headSHA == "":
		validation(w, fieldError{Resource: "PullRequest", Field: "head", Code: "invalid"})
		return
	case baseSHA == "":
		validation(w, fieldError{Resource: "PullRequest", Field: "base", Code: "invalid"})
		return
	}
	for _, it := range rp.Items {
		if p := it.PR; p != nil && it.State == "open" && p.Head == head && p.Base == in.Base {
			custom(w, "PullRequest", fmt.Sprintf("A pull request already exists for %s:%s.", rp.Owner, head))
			return
		}
	}
	if headSHA == baseSHA {
		custom(w, "PullRequest", fmt.Sprintf("No commits between %s and %s", in.Base, head))
		return
	}
	at := s.st.now()
	it := s.st.addItem(rp, in.Title, in.Body, viewer, at)
	it.PR = &pull{
		Draft: in.Draft, Head: head, Base: in.Base, HeadSHA: headSHA, BaseSHA: baseSHA,
		Files: diffCommits(rp.Commits[baseSHA], rp.Commits[headSHA]),
	}
	s.st.feedItem(rp, it, "opened", viewer, at)
	writeJSON(w, http.StatusCreated, s.renderPull(rp, it, true))
}

func (s *Server) listPullFiles(w http.ResponseWriter, r *http.Request) {
	rp, it := s.pullOf(w, r)
	if it == nil {
		return
	}
	paged(s, w, r, it.PR.Files, func(f *prFile) any { return s.renderFile(rp, f) })
}

const notMergeable = "Pull Request is not mergeable"

func (s *Server) mergePull(w http.ResponseWriter, r *http.Request) {
	rp, it := s.pullOf(w, r)
	if it == nil {
		return
	}
	var in struct {
		SHA           string `json:"sha"`
		MergeMethod   string `json:"merge_method"`
		CommitTitle   string `json:"commit_title"`
		CommitMessage string `json:"commit_message"`
	}
	if !decode(w, r, &in) {
		return
	}
	p := it.PR
	if in.MergeMethod == "" {
		in.MergeMethod = "merge"
	}
	switch {
	case it.State != "open" || p.Merged || p.Draft:
		apiError(w, http.StatusMethodNotAllowed, notMergeable)
		return
	case !slices.Contains([]string{"merge", "squash", "rebase"}, in.MergeMethod):
		validation(w, fieldError{Resource: "PullRequest", Field: "merge_method", Code: "invalid"})
		return
	case in.SHA != "" && in.SHA != p.HeadSHA:
		apiError(w, http.StatusConflict, "Head branch was modified. Review and try the merge again.")
		return
	}
	params := map[string]any{"merge_method": in.MergeMethod}
	for k, v := range map[string]string{"sha": in.SHA, "commit_title": in.CommitTitle, "commit_message": in.CommitMessage} {
		if v != "" {
			params[k] = v
		}
	}
	at := s.st.now()
	msg := in.CommitTitle
	if msg == "" {
		msg = fmt.Sprintf("Merge pull request #%d from %s/%s", it.Number, rp.Owner, p.Head)
	}
	tip := rp.Branches[p.Base]
	if tip == "" {
		tip = p.BaseSHA
	}
	merge := s.st.commit(rp, msg, at, s.mergedFiles(rp, p), tip, p.HeadSHA)
	if _, ok := rp.Branches[p.Base]; ok {
		rp.Branches[p.Base] = merge.SHA
	}
	p.Merged, p.MergedAt, p.MergeSHA, p.MergeParams, p.AutoMerge = true, at, merge.SHA, params, nil
	it.State, it.Closed, it.Updated = "closed", at, at
	s.st.addEvent(it, "merged", viewer, at)
	s.st.addEvent(it, "closed", viewer, at)
	s.st.feedItem(rp, it, "closed", viewer, at)
	writeJSON(w, http.StatusOK, map[string]any{"sha": merge.SHA, "merged": true, "message": "Pull Request successfully merged"})
}

// mergedFiles is the base branch's tree with the PR's changes applied.
func (s *Server) mergedFiles(rp *repo, p *pull) map[string]string {
	base := rp.resolveRef(p.Base)
	if base == nil {
		base = rp.Commits[p.BaseSHA]
	}
	files := maps.Clone(base.Files)
	head := rp.Commits[p.HeadSHA]
	for _, f := range p.Files {
		switch f.Status {
		case "removed":
			delete(files, f.Filename)
		case "renamed":
			delete(files, f.Previous)
			files[f.Filename] = head.Files[f.Filename]
		default:
			files[f.Filename] = head.Files[f.Filename]
		}
	}
	return files
}

// atoi64 parses a path value as an id, 0 when malformed.
func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// requestReviewers adds team reviewers. Like GitHub it refuses a team the
// organisation does not have (see AddTeam).
func (s *Server) requestReviewers(w http.ResponseWriter, r *http.Request) {
	rp, it := s.pullOf(w, r)
	if it == nil {
		return
	}
	var in struct {
		Reviewers     []string `json:"reviewers"`
		TeamReviewers []string `json:"team_reviewers"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Reviewers)+len(in.TeamReviewers) == 0 {
		validation(w, fieldError{Resource: "PullRequest", Field: "reviewers", Code: "missing"})
		return
	}
	for _, slug := range in.TeamReviewers {
		if !slices.Contains(s.st.teams, slug) {
			custom(w, "PullRequest", "Reviews may only be requested from collaborators. One or more of the teams you specified is not a collaborator of the repository.")
			return
		}
	}
	p := it.PR
	for _, slug := range in.TeamReviewers {
		if !slices.Contains(p.RequestedTeams, slug) {
			p.RequestedTeams = append(p.RequestedTeams, slug)
		}
	}
	for _, login := range in.Reviewers {
		if !slices.Contains(p.Reviewers, login) {
			p.Reviewers = append(p.Reviewers, login)
		}
	}
	it.Updated = s.st.now()
	writeJSON(w, http.StatusCreated, s.renderPull(rp, it, true))
}
