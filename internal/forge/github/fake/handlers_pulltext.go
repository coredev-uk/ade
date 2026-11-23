package fake

import (
	"net/http"
	"slices"
)

// The two pull-request routes behind forge.MRTextWriter: the commits a PR
// carries (GET /pulls/{n}/commits, oldest first) and PATCH /pulls/{n}, which
// rewrites the body (and the title when one is sent) and nothing else.
func (s *Server) pullTextRoutes(mux *http.ServeMux) {
	const r = "/repos/{owner}/{repo}"
	s.handle(mux, "GET "+r+"/pulls/{n}/commits", s.listPullCommits)
	s.handle(mux, "PATCH "+r+"/pulls/{n}", s.updatePull)
}

func (s *Server) listPullCommits(w http.ResponseWriter, r *http.Request) {
	rp, it := s.pullOf(w, r)
	if it == nil {
		return
	}
	paged(s, w, r, rp.prCommits(it.PR), func(c *commit) any { return s.renderCommit(rp, c) })
}

// prCommits lists the commits of p's head that the base does not have, oldest
// first.
func (rp *repo) prCommits(p *pull) []*commit {
	var out []*commit
	seen := map[string]bool{}
	var walk func(sha string)
	walk = func(sha string) {
		c := rp.Commits[sha]
		if c == nil || seen[sha] || rp.isAncestor(sha, p.BaseSHA) {
			return
		}
		seen[sha] = true
		out = append(out, c)
		for _, parent := range c.Parents {
			walk(parent)
		}
	}
	walk(p.HeadSHA)
	slices.Reverse(out) // the walk goes from the head back
	slices.SortStableFunc(out, func(a, b *commit) int { return a.Created.Compare(b.Created) })
	return out
}

func (s *Server) updatePull(w http.ResponseWriter, r *http.Request) {
	rp, it := s.pullOf(w, r)
	if it == nil {
		return
	}
	var in struct {
		Title *string `json:"title"`
		Body  *string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Title != nil && *in.Title == "" {
		validation(w, fieldError{Resource: "PullRequest", Field: "title", Code: "missing_field"})
		return
	}
	if in.Title != nil {
		it.Title = *in.Title
	}
	if in.Body != nil {
		it.Body = *in.Body
		it.PR.DescWrites++
	}
	it.Updated = s.st.now()
	writeJSON(w, http.StatusOK, s.renderPull(rp, it, true))
}
