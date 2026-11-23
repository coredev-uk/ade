package fake

import (
	"net/http"
	"slices"
)

// The commits a pull request carries (GET /pulls/{n}/commits, oldest first),
// read by forge.MRTextWriter. Its write, PATCH /pulls/{n}, is patchPull's.
func (s *Server) pullTextRoutes(mux *http.ServeMux) {
	const r = "/repos/{owner}/{repo}"
	s.handle(mux, "GET "+r+"/pulls/{n}/commits", s.listPullCommits)
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
