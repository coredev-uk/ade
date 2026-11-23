package fake

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/coredev-uk/ade/internal/forge"
)

// pendingUpdate is an accepted update-branch that has not landed yet: GitHub
// answers 202 and merges in the background, so the head moves a few reads later.
type pendingUpdate struct {
	remaining int // pull reads still showing the old head
	merge     *commit
}

// MoveBase commits files on the PR's base branch, as another merged PR would,
// so that the PR is behind it: mergeable_state turns "behind", or "dirty" when
// a file the PR changed differs.
func (s *Server) MoveBase(ref forge.Ref, files map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, it, err := s.item(ref)
	if err != nil || it.PR == nil {
		return fmt.Errorf("no pull request %s", ref)
	}
	p := it.PR
	tip := rp.resolveRef(p.Base)
	all := cloneFiles(tip.Files)
	for k, v := range files {
		all[k] = v
	}
	c := s.st.commit(rp, "Move "+p.Base, s.st.now(), all, tip.SHA)
	rp.Branches[p.Base] = c.SHA
	p.BaseMoved = true
	return nil
}

// SetUpdateDelay makes the next update-branch land only after n reads of the
// pull request, as GitHub's background job does.
func (s *Server) SetUpdateDelay(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateDelay = n
}

// Updates is how many update-branch requests the PR accepted.
func (s *Server) Updates(ref forge.Ref) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.pull(ref)
	if err != nil {
		return 0, err
	}
	return p.Updates, nil
}

// contains reports whether the commit sha has ancestor among its ancestors
// (or is it).
func (rp *repo) contains(sha, ancestor string) bool {
	seen := map[string]bool{}
	todo := []string{sha}
	for len(todo) > 0 {
		cur := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if cur == ancestor {
			return true
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		if c := rp.Commits[cur]; c != nil {
			todo = append(todo, c.Parents...)
		}
	}
	return false
}

// threeWay merges theirs into ours over their common base, file by file. A path
// both sides changed differently is a conflict.
func threeWay(base, ours, theirs map[string]string) (map[string]string, []string) {
	out := cloneFiles(ours)
	var conflicts []string
	paths := map[string]bool{}
	for _, m := range []map[string]string{base, ours, theirs} {
		for k := range m {
			paths[k] = true
		}
	}
	for path := range paths {
		b, inB := base[path]
		o, inO := ours[path]
		t, inT := theirs[path]
		switch {
		case inO == inT && o == t:
		case inB == inO && b == o: // only theirs changed
			if inT {
				out[path] = t
			} else {
				delete(out, path)
			}
		case inB == inT && b == t: // only ours changed
		default:
			conflicts = append(conflicts, path)
		}
	}
	slices.Sort(conflicts)
	return out, conflicts
}

// updateBranch handles PUT .../pulls/{n}/update-branch (merge the base into the
// head), as documented: 202 with a message, 422 for a stale expected_head_sha,
// for a branch with nothing to merge, and for a merge conflict.
func (s *Server) updateBranch(w http.ResponseWriter, r *http.Request) {
	rp, it := s.pullOf(w, r)
	if it == nil {
		return
	}
	var in struct {
		ExpectedHeadSHA string `json:"expected_head_sha"`
	}
	if !decode(w, r, &in) {
		return
	}
	p := it.PR
	tip := rp.resolveRef(p.Base)
	switch {
	case it.State != "open" || p.Merged:
		custom(w, "PullRequest", "Pull request is not open.")
		return
	case in.ExpectedHeadSHA != "" && in.ExpectedHeadSHA != p.HeadSHA:
		custom(w, "PullRequest", "Expected head sha didn't match current head ref.")
		return
	case p.Update != nil:
		apiError(w, http.StatusConflict, "A pull request update is already in progress.")
		return
	case rp.contains(p.HeadSHA, tip.SHA):
		custom(w, "PullRequest", "There are no new commits on the base branch.")
		return
	}
	merged, conflicts := threeWay(rp.Commits[p.BaseSHA].Files, rp.Commits[p.HeadSHA].Files, tip.Files)
	if len(conflicts) > 0 {
		custom(w, "PullRequest", "merge conflict between base and head")
		return
	}
	at := s.st.now()
	msg := fmt.Sprintf("Merge branch '%s' into %s", p.Base, p.Head)
	p.Updates++
	p.Update = &pendingUpdate{remaining: s.updateDelay, merge: s.st.commit(rp, msg, at, merged, p.HeadSHA, tip.SHA)}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"message": "Updating pull request branch.",
		"url":     fmt.Sprintf("%s/repos/%s/%s/pulls/%d", s.URL+apiPath, rp.Owner, rp.Name, it.Number),
	})
}

// settleUpdate lets a pending update land once enough reads have seen the old
// head: the head branch moves to the merge commit and the PR's files follow.
func (s *Server) settleUpdate(rp *repo, it *item) {
	p := it.PR
	u := p.Update
	if u == nil {
		return
	}
	if u.remaining > 0 {
		u.remaining--
		return
	}
	p.Update = nil
	tip := rp.resolveRef(p.Base)
	rp.Branches[p.Head] = u.merge.SHA
	p.HeadSHA, p.BaseSHA = u.merge.SHA, tip.SHA
	p.Files = diffCommits(tip, u.merge)
	it.Updated = u.merge.Created
	s.st.feedPush(rp, p.Head, u.merge, viewer)
}
