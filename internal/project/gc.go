package project

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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

	// merged holds the head commit of each merged MR of the branch (the sha is
	// empty when the forge gave none), and forgeName the manifest's forge: what
	// DeleteBranch needs to tell a squash-merged branch from an unmerged one.
	merged    []mrHead
	forgeName string
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
	return s.trees(ctx, name, lookup, nil)
}

// TreesAt judges only the worktrees at the given paths (an item's), with the
// same checks as Trees. A path that is no worktree of project name is absent
// from the result.
func (s *Service) TreesAt(ctx context.Context, name string, paths []string, lookup MRLookup) ([]Tree, error) {
	want := map[string]bool{}
	for _, p := range paths {
		want[filepath.Clean(p)] = true
	}
	return s.trees(ctx, name, lookup, func(ts TreeStatus) bool { return want[filepath.Clean(ts.Path)] })
}

func (s *Service) trees(ctx context.Context, name string, lookup MRLookup, only func(TreeStatus) bool) ([]Tree, error) {
	sts, err := s.Status(ctx, name)
	if err != nil {
		return nil, err
	}
	var out []Tree
	fetch := s.NewFetcher()
	for _, ps := range sts {
		m, err := LoadManifest(ps.Dir)
		if err != nil {
			return nil, fmt.Errorf("project %s: %w", ps.Name, err)
		}
		for _, rs := range ps.Repos {
			bare := filepath.Join(ps.Dir, rs.Name, ".bare")
			for _, ts := range rs.Worktrees {
				if only != nil && !only(ts) {
					continue
				}
				t := Tree{Project: ps.Name, Repo: rs.Name, Branch: ts.Branch, Path: ts.Path, Bare: bare}
				s.judge(ctx, &t, ts, m, lookup, fetch)
				out = append(out, t)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Label() < out[j].Label() })
	return out, nil
}

// Fetcher runs `git fetch --prune` in a bare clone.
type Fetcher func(ctx context.Context, forgeName, bare string) error

// NewFetcher returns a Fetcher that uses the forge's credentials and fetches
// each clone once, however often it is called.
func (s *Service) NewFetcher() Fetcher {
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
func (s *Service) judge(ctx context.Context, t *Tree, ts TreeStatus, m *config.Manifest, lookup MRLookup, fetch Fetcher) {
	if ts.Branch == "" {
		t.Keep = append(t.Keep, "detached HEAD: no branch to match a merge request to")
		return
	}
	var heads []mrHead
	t.forgeName = m.Forge
	repo, ok := m.Repos[t.Repo]
	switch {
	case !ok:
		t.Keep = append(t.Keep, "repo "+t.Repo+" is no longer in the manifest, so its merge requests cannot be looked up")
	case lookup == nil:
		t.Keep = append(t.Keep, "no forge to ask about the merge request")
	default:
		mrs, err := lookup(ctx, m, repo.Path, ts.Branch)
		var why string
		why, heads, t.merged = mrVerdict(t, mrs, err)
		if why != "" {
			t.Keep = append(t.Keep, why)
		}
	}
	ev := pushEvidence{heads: heads}
	if len(t.Keep) == 0 {
		// A tree already kept needs no proof, so skip the fetch.
		ev.refresh = func(ctx context.Context) error { return fetch(ctx, m.Forge, t.Bare) }
	}
	keep, pushed := localGuard(ctx, git.Client{}, ts, ev)
	t.Keep = append(t.Keep, keep...)
	if len(t.Keep) == 0 {
		t.Pushed = pushed
	}
}

// mrVerdict says why the MRs of a branch keep the worktree ("" when they
// allow removal) and returns the head commits they ended at, and which of
// them are the merged MRs'.
func mrVerdict(t *Tree, mrs []forge.MR, err error) (why string, heads, merged []mrHead) {
	if err != nil {
		return "could not read merge requests: " + err.Error(), nil, nil
	}
	if len(mrs) == 0 {
		return "no merge request for this branch", nil, nil
	}
	var done *forge.MR
	for i, mr := range mrs {
		switch mr.State {
		case "opened":
			return fmt.Sprintf("merge request %s is still open", mrLabel(mr.Ref)), nil, nil
		case "merged":
			done = &mrs[i]
			merged = append(merged, mrHead{sha: mr.HeadSHA, mr: mrLabel(mr.Ref)})
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
		return "merge request state unknown", nil, nil
	}
	t.MR = fmt.Sprintf("%s %s", mrLabel(done.Ref), done.State)
	return "", heads, merged
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
// not ahead of it, when that is a remote-tracking branch (a local branch says
// nothing about the remote). Without one, HEAD must be the head of one of the branch's
// merge requests or sit on a remote-tracking ref that survives a fetch --prune.
func unpushed(ctx context.Context, c git.Client, ts TreeStatus, ev pushEvidence) (why, proof string) {
	if ts.HasUpstream && remoteUpstream(ctx, c, ts.Path) {
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

// remoteUpstream reports whether the upstream of the tree's branch is a
// remote-tracking ref. An unreadable upstream counts as not, which sends the
// tree through the stricter no-upstream checks.
func remoteUpstream(ctx context.Context, c git.Client, tree string) bool {
	full, err := c.Run(ctx, tree, "rev-parse", "--symbolic-full-name", "@{u}")
	return err == nil && strings.HasPrefix(strings.TrimSpace(full), "refs/remotes/")
}

func commits(n int) string {
	if n == 1 {
		return "commit"
	}
	return "commits"
}

// RemoveTree removes a worktree that Trees judged removable, and its local
// branch when it is merged: git sees it so (`git branch -d`), or its commits
// all sit in the head of a merged MR (a squash merge: `git branch -D`). It
// never forces the worktree: git itself refuses one that turned dirty since the
// judgement. The BranchFate says what became of the branch.
func (s *Service) RemoveTree(ctx context.Context, t Tree) (BranchFate, error) {
	if err := s.RemoveWorktree(ctx, t); err != nil {
		return BranchFate{}, err
	}
	return s.DeleteBranch(ctx, t), nil
}

// RemoveWorktree removes the worktree of a tree that Trees judged removable,
// leaving its branch. It never forces.
func (s *Service) RemoveWorktree(ctx context.Context, t Tree) error {
	if !t.Removable() {
		return fmt.Errorf("%s is kept: %s", t.Label(), strings.Join(t.Keep, "; "))
	}
	var c git.Client
	_, err := c.Run(ctx, t.Bare, "worktree", "remove", t.Path)
	e := audit.Entry{
		Action: "worktree.remove", Target: t.Path, Project: t.Project,
		Params: map[string]any{"repo": t.Repo, "branch": t.Branch, "mr": t.MR}, Outcome: outcome(err),
	}
	if err != nil {
		e.Error = git.Reason(err)
		s.audit(e)
		return fmt.Errorf("remove %s: %s", t.Label(), git.Reason(err))
	}
	s.audit(e)
	return nil
}

// BranchFate is what became of the local branch of a removed tree.
type BranchFate struct {
	Deleted bool
	// Forced means `git branch -D` deleted it, because git did not see it
	// merged but its commits are in a merged MR's head.
	Forced bool
	// Why says what the decision rests on: the proof for a forced deletion, the
	// reason a branch stayed.
	Why string
}

// DeleteBranch deletes the local branch of a removed tree. `git branch -d`
// goes first, as it deletes whatever git sees as merged. When it refuses, the
// branch is still deleted with `git branch -D` if PlanBranch finds all its
// commits in a merged MR's head (a squash merge leaves nothing for git to
// see); otherwise it stays, with the reason.
func (s *Service) DeleteBranch(ctx context.Context, t Tree) BranchFate {
	var c git.Client
	e := audit.Entry{
		Action: "git.branch.delete", Target: t.Branch, Project: t.Project,
		Params: map[string]any{"repo": t.Repo, "mr": t.MR},
	}
	if _, err := c.Run(ctx, t.Bare, "branch", "-d", t.Branch); err == nil {
		e.Outcome = "ok"
		s.audit(e)
		return BranchFate{Deleted: true}
	}
	contained, why := s.PlanBranch(ctx, t)
	if !contained {
		return BranchFate{Why: why}
	}
	e.Params["forced"] = true
	e.Params["why"] = why
	_, err := c.Run(ctx, t.Bare, "branch", "-D", t.Branch)
	e.Outcome = outcome(err)
	if err != nil {
		e.Error = err.Error()
		s.audit(e)
		return BranchFate{Why: err.Error()}
	}
	s.audit(e)
	return BranchFate{Deleted: true, Forced: true, Why: why}
}

// PlanBranch reports whether the tree's local branch may be deleted with `git
// branch -D` although git does not see it merged: every commit on it is in the
// head of a merged MR of the branch. The default branch never is. why gives the
// proof, or the reason it is not. It deletes nothing; it may fetch the MR's
// head commit when the clone lacks it.
func (s *Service) PlanBranch(ctx context.Context, t Tree) (contained bool, why string) {
	var c git.Client
	def, err := c.DefaultBranch(ctx, t.Bare)
	if err == nil && def == t.Branch {
		return false, "it is the default branch"
	}
	tip, err := c.Run(ctx, t.Bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+t.Branch)
	if err != nil {
		return false, "the branch does not exist"
	}
	tip = strings.TrimSpace(tip)
	if len(t.merged) == 0 {
		return false, "its merge request was closed, not merged"
	}
	var unknown, absent []string
	for _, h := range t.merged {
		switch h.sha {
		case "":
			unknown = append(unknown, h.mr)
			continue
		case tip:
			return true, "its tip is " + h.mr + "'s head"
		}
		if !s.haveCommit(ctx, t, h.sha) {
			absent = append(absent, h.mr)
			continue
		}
		_, err := c.Run(ctx, t.Bare, "merge-base", "--is-ancestor", tip, h.sha)
		var ee *exec.ExitError
		switch {
		case err == nil:
			return true, "its commits are all in " + h.mr + "'s head"
		case !errors.As(err, &ee) || ee.ExitCode() != 1:
			return false, "cannot compare it with " + h.mr + ": " + err.Error()
		}
	}
	switch {
	case len(absent) > 0:
		return false, absent[0] + "'s head commit is not in the clone and could not be fetched"
	case len(unknown) > 0:
		return false, "the forge gave no head commit for " + unknown[0]
	}
	return false, "local commits not in the merged MR " + t.merged[0].mr
}

// haveCommit reports whether the bare clone has commit sha, fetching it from
// the remote when not.
func (s *Service) haveCommit(ctx context.Context, t Tree, sha string) bool {
	var c git.Client
	if _, err := c.Run(ctx, t.Bare, "cat-file", "-e", sha+"^{commit}"); err == nil {
		return true
	}
	gc, err := s.GitClient(ctx, t.forgeName, t.Bare)
	if err != nil {
		return false
	}
	if _, err := gc.Run(ctx, t.Bare, "fetch", "--quiet", "origin", sha); err != nil {
		return false
	}
	_, err = c.Run(ctx, t.Bare, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// TreeOf finds the worktree a merge request was made from, judged as Trees
// does with mr taken as the branch's one merge request. fetch comes from
// NewFetcher, which callers share across calls to fetch each repo once. ok is
// false when the project has no such worktree on disk.
func (s *Service) TreeOf(ctx context.Context, name string, mr forge.MR, fetch Fetcher) (t Tree, ok bool, err error) {
	sts, err := s.Status(ctx, name)
	if err != nil {
		return Tree{}, false, err
	}
	known := func(context.Context, *config.Manifest, string, string) ([]forge.MR, error) {
		return []forge.MR{mr}, nil
	}
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
