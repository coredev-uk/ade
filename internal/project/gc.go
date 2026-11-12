package project

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
)

// MRLookup finds the merge requests whose source branch is branch in the
// forge project repoPath (every state). The manifest says which forge to ask.
type MRLookup func(ctx context.Context, m *config.Manifest, repoPath, branch string) ([]forge.MR, error)

// Tree is one feature worktree and the verdict `ade gc` reaches on it.
type Tree struct {
	Project string `json:"project"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	Bare    string `json:"-"`
	// MR names the merge request that decided the verdict ("api!1 merged").
	MR string `json:"mr,omitempty"`
	// Keep lists why the worktree stays; empty means it can be removed.
	Keep []string `json:"keep,omitempty"`
	// Pushed says what proves the commits are on the remote ("HEAD is
	// api!7's head", "on origin/develop"); set when no guard failed.
	Pushed string `json:"pushed,omitempty"`
}

// Removable reports whether every guard passed.
func (t Tree) Removable() bool { return len(t.Keep) == 0 }

// Label is "project/repo/branch-dir", how a worktree is named to the user.
func (t Tree) Label() string { return t.Project + "/" + t.Repo + "/" + filepath.Base(t.Path) }

// RemoveCommand is the exact command that removes the worktree by hand.
func (t Tree) RemoveCommand() string {
	return fmt.Sprintf("git -C %s worktree remove %s", shellQuote(t.Bare), shellQuote(t.Path))
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Trees inspects the feature worktrees of project name (every project when
// empty) and decides for each whether it can go: its merge requests are all
// merged or closed (at least one), it has no uncommitted changes, and
// everything committed is on the remote. The MR lookup uses the network, and
// so does one fetch per repo when a branch without upstream needs its commits
// checked against the remote.
func (s *Service) Trees(ctx context.Context, name string, lookup MRLookup) ([]Tree, error) {
	sts, err := s.Status(ctx, name)
	if err != nil {
		return nil, err
	}
	var out []Tree
	fetch := s.fetcher()
	for _, ps := range sts {
		m, err := LoadManifest(ps.Dir)
		if err != nil {
			return nil, fmt.Errorf("project %s: %w", ps.Name, err)
		}
		for _, rs := range ps.Repos {
			bare := filepath.Join(ps.Dir, rs.Name, ".bare")
			for _, ts := range rs.Worktrees {
				t := Tree{Project: ps.Name, Repo: rs.Name, Branch: ts.Branch, Path: ts.Path, Bare: bare}
				s.judge(ctx, &t, ts, m, lookup, fetch)
				out = append(out, t)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Label() < out[j].Label() })
	return out, nil
}

// fetcher returns a function that runs `git fetch --prune` in a bare clone
// with the forge's credentials, once per clone and run.
func (s *Service) fetcher() func(ctx context.Context, forgeName, bare string) error {
	done := map[string]error{}
	return func(ctx context.Context, forgeName, bare string) error {
		if err, ok := done[bare]; ok {
			return err
		}
		c, err := s.GitClient(ctx, forgeName, bare)
		if err == nil {
			err = c.Fetch(ctx, bare)
		}
		done[bare] = err
		return err
	}
}

// judge fills t.Keep, t.MR and t.Pushed.
func (s *Service) judge(ctx context.Context, t *Tree, ts TreeStatus, m *config.Manifest, lookup MRLookup, fetch func(ctx context.Context, forgeName, bare string) error) {
	if ts.Branch == "" {
		t.Keep = append(t.Keep, "detached HEAD: no branch to match a merge request to")
		return
	}
	var heads []mrHead
	repo, ok := m.Repos[t.Repo]
	switch {
	case !ok:
		t.Keep = append(t.Keep, "repo "+t.Repo+" is no longer in the manifest, so its merge requests cannot be looked up")
	case lookup == nil:
		t.Keep = append(t.Keep, "no forge to ask about the merge request")
	default:
		mrs, err := lookup(ctx, m, repo.Path, ts.Branch)
		var why string
		why, heads = mrVerdict(t, mrs, err)
		if why != "" {
			t.Keep = append(t.Keep, why)
		}
	}
	ev := pushEvidence{heads: heads, refresh: func(ctx context.Context) error { return fetch(ctx, m.Forge, t.Bare) }}
	keep, pushed := localGuard(ctx, git.Client{}, ts, ev)
	t.Keep = append(t.Keep, keep...)
	if len(t.Keep) == 0 {
		t.Pushed = pushed
	}
}

// mrVerdict says why the MRs of a branch keep the worktree ("" when they
// allow removal) and returns the head commits they ended at.
func mrVerdict(t *Tree, mrs []forge.MR, err error) (why string, heads []mrHead) {
	if err != nil {
		return "could not read merge requests: " + err.Error(), nil
	}
	if len(mrs) == 0 {
		return "no merge request for this branch", nil
	}
	var done *forge.MR
	for i, mr := range mrs {
		switch mr.State {
		case "opened":
			return fmt.Sprintf("merge request %s is still open", mrLabel(mr.Ref)), nil
		case "merged":
			done = &mrs[i]
		case "closed":
			if done == nil {
				done = &mrs[i]
			}
		}
		if mr.HeadSHA != "" {
			heads = append(heads, mrHead{sha: mr.HeadSHA, mr: mrLabel(mr.Ref)})
		}
	}
	if done == nil {
		return "merge request state unknown", nil
	}
	t.MR = fmt.Sprintf("%s %s", mrLabel(done.Ref), done.State)
	return "", heads
}

func mrLabel(ref forge.Ref) string {
	proj := ref.Project
	if i := strings.LastIndex(proj, "/"); i >= 0 {
		proj = proj[i+1:]
	}
	return fmt.Sprintf("%s!%d", proj, ref.IID)
}

// mrHead is the final head commit of a merge request, which the forge keeps.
type mrHead struct{ sha, mr string }

// pushEvidence is what a tree's commits are checked against besides its
// upstream.
type pushEvidence struct {
	// heads are the final head commits of the branch's merge requests; a branch
	// deleted after the merge has no remote ref left, but a tree sitting on such
	// a commit has lost nothing.
	heads []mrHead
	// refresh fetches and prunes the remote-tracking refs, so that a ref found
	// to contain HEAD still exists on the remote. Nil checks the refs as they
	// are, which is for callers that must not touch the network.
	refresh func(context.Context) error
}

// localGuard returns what makes a worktree unsafe to discard whatever the
// forge says: uncommitted changes, or commits that no remote branch has. When
// nothing does, pushed says what proves the commits are on the remote.
func localGuard(ctx context.Context, c git.Client, ts TreeStatus, ev pushEvidence) (keep []string, pushed string) {
	switch {
	case ts.StatusError != "":
		keep = append(keep, "cannot read the worktree status: "+ts.StatusError)
	case ts.Dirty:
		keep = append(keep, "uncommitted changes")
	}
	why, proof := unpushed(ctx, c, ts, ev)
	if why != "" {
		keep = append(keep, why)
	}
	return keep, proof
}

// unpushed explains why the tree's commits are not all on the remote, or ""
// with the proof that they are. A branch with an upstream is pushed when it is
// not ahead of it. Without one, HEAD must be the head of one of the branch's
// merge requests or sit on a remote-tracking ref that survives a fetch --prune.
func unpushed(ctx context.Context, c git.Client, ts TreeStatus, ev pushEvidence) (why, proof string) {
	if ts.HasUpstream {
		if ts.Ahead > 0 {
			return fmt.Sprintf("%d %s not pushed", ts.Ahead, commits(ts.Ahead)), ""
		}
		up, err := c.Upstream(ctx, ts.Path)
		if err != nil {
			up = "its upstream"
		}
		return "", "not ahead of " + up
	}
	head, err := c.Run(ctx, ts.Path, "rev-parse", "HEAD")
	if err != nil {
		return "cannot tell whether commits are pushed: " + git.Reason(err), ""
	}
	head = strings.TrimSpace(head)
	for _, h := range ev.heads {
		if h.sha == head {
			return "", "HEAD is " + h.mr + "'s head"
		}
	}
	if ev.refresh != nil {
		if err := ev.refresh(ctx); err != nil {
			return "cannot tell whether commits are pushed: fetching the remote failed: " + git.Reason(err), ""
		}
	}
	out, err := c.Run(ctx, ts.Path, "for-each-ref", "--contains", head, "--format=%(refname)", "refs/remotes")
	if err != nil {
		return "cannot tell whether commits are pushed: " + git.Reason(err), ""
	}
	for _, ref := range strings.Fields(out) {
		if name := strings.TrimPrefix(ref, "refs/remotes/"); !strings.HasSuffix(name, "/HEAD") {
			return "", "on " + name
		}
	}
	return "commits not on any remote branch (never pushed)", ""
}

func commits(n int) string {
	if n == 1 {
		return "commit"
	}
	return "commits"
}

// RemoveTree removes a worktree that Trees judged removable, and its local
// branch when git agrees the branch is merged. It never forces: git itself
// refuses a worktree that turned dirty since the judgement. branchKept is true
// when the branch had to stay.
func (s *Service) RemoveTree(ctx context.Context, t Tree) (branchKept bool, err error) {
	if !t.Removable() {
		return false, fmt.Errorf("%s is kept: %s", t.Label(), strings.Join(t.Keep, "; "))
	}
	var c git.Client
	_, err = c.Run(ctx, t.Bare, "worktree", "remove", t.Path)
	e := audit.Entry{
		Action: "worktree.remove", Target: t.Path, Project: t.Project,
		Params: map[string]any{"repo": t.Repo, "branch": t.Branch, "mr": t.MR}, Outcome: outcome(err),
	}
	if err != nil {
		e.Error = git.Reason(err)
		s.audit(e)
		return false, fmt.Errorf("remove %s: %s", t.Label(), git.Reason(err))
	}
	s.audit(e)
	if _, berr := c.Run(ctx, t.Bare, "branch", "-d", t.Branch); berr != nil {
		return true, nil
	}
	return false, nil
}

// TreeOf finds the worktree a merge request was made from, judged as Trees
// does with mr taken as the branch's one merge request. ok is false when
// the project has no such worktree on disk.
func (s *Service) TreeOf(ctx context.Context, name string, mr forge.MR) (t Tree, ok bool, err error) {
	sts, err := s.Status(ctx, name)
	if err != nil {
		return Tree{}, false, err
	}
	known := func(context.Context, *config.Manifest, string, string) ([]forge.MR, error) {
		return []forge.MR{mr}, nil
	}
	fetch := s.fetcher()
	for _, ps := range sts {
		m, err := LoadManifest(ps.Dir)
		if err != nil {
			return Tree{}, false, fmt.Errorf("project %s: %w", ps.Name, err)
		}
		for _, rs := range ps.Repos {
			if repo, in := m.Repos[rs.Name]; !in || repo.Path != mr.Ref.Project {
				continue
			}
			for _, ts := range rs.Worktrees {
				if ts.Branch != mr.SourceBranch || ts.Branch == "" {
					continue
				}
				t = Tree{Project: ps.Name, Repo: rs.Name, Branch: ts.Branch, Path: ts.Path, Bare: filepath.Join(ps.Dir, rs.Name, ".bare")}
				s.judge(ctx, &t, ts, m, known, fetch)
				return t, true, nil
			}
		}
	}
	return Tree{}, false, nil
}
