package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
)

// MRTree is the detached worktree of a merge request's head.
type MRTree struct {
	Worktree string
	// Commit is the (short) head the tree is at.
	Commit string
	// Action is create (a new tree), update (an existing tree moved to the new
	// head), keep (left as it was: it has changes, or is on the head already).
	Action string
	Reason string
	DryRun bool
}

// MRTreeDir is where an MR's detached tree lives: <root>/<project>/<repo>/mr-<iid>.
func MRTreeDir(root, proj, repo string, iid int) string {
	return filepath.Join(root, proj, repo, fmt.Sprintf("mr-%d", iid))
}

// CheckoutMR makes (or refreshes) the detached tree of mr in repo of m: the
// worktree is at the MR's head commit and has no branch, so it never collides
// with the author's own branch and cannot be pushed. This is how a fork MR
// (whose source branch is not in the clone's origin) is checked out. A tree
// that exists moves to the MR's new head only while it is clean.
func CheckoutMR(ctx context.Context, a *app.App, m *config.Manifest, repo string, mr forge.MR, dry bool) (MRTree, error) {
	bare := filepath.Join(a.Root, m.Name, repo, ".bare")
	res := MRTree{DryRun: dry, Worktree: MRTreeDir(a.Root, m.Name, repo, mr.Ref.IID)}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		return res, fmt.Errorf("%s is not cloned: run `ade project add` for %s first", repo, m.Name)
	}
	gc, err := New(a, "user").GitClient(ctx, m.Forge, bare)
	if err != nil {
		return res, err
	}
	if err := gc.PruneWorktrees(ctx, bare); err != nil {
		return res, err
	}
	trees, err := gc.Worktrees(ctx, bare)
	if err != nil {
		return res, err
	}
	exists := slices.ContainsFunc(trees, func(w git.Worktree) bool { return filepath.Clean(w.Path) == filepath.Clean(res.Worktree) })
	if !exists {
		if _, err := os.Stat(res.Worktree); err == nil {
			return res, fmt.Errorf("%s exists but is not a worktree of %s", res.Worktree, repo)
		}
	}
	res.Action = "create"
	if exists {
		res.Action = "update"
	}
	if dry {
		res.Commit = mr.HeadSHA
		return res, nil
	}
	err = gc.Fetch(ctx, bare)
	logGit(a, "git.fetch", bare, m.Name, mr.Ref, nil, err)
	if err != nil {
		return res, err
	}
	sha, err := mrHeadCommit(ctx, gc, bare, mr)
	if err != nil {
		return res, err
	}
	res.Commit = ShortSHA(sha)
	if exists {
		return refreshMRTree(ctx, a, gc, m.Name, mr, res, sha)
	}
	_, err = gc.Run(ctx, bare, "worktree", "add", "--detach", res.Worktree, sha)
	logGit(a, "git.worktree.add", res.Worktree, m.Name, mr.Ref, map[string]any{"detached": sha, "repo": repo, "for": "mr checkout"}, err)
	if err != nil {
		return res, fmt.Errorf("check out %s: %s", mr.Ref, git.Reason(err))
	}
	for _, hook := range m.Repos[repo].Hooks.PostWorktree {
		_, herr := git.RunHook(ctx, res.Worktree, hook)
		logGit(a, "git.hook", res.Worktree, m.Name, mr.Ref, map[string]any{"hook": "post_worktree", "cmd": hook}, herr)
	}
	return res, nil
}

// refreshMRTree moves an existing mr-<iid> tree to sha when it is clean.
func refreshMRTree(ctx context.Context, a *app.App, gc git.Client, proj string, mr forge.MR, res MRTree, sha string) (MRTree, error) {
	res.Action = "keep"
	head, err := gc.Run(ctx, res.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return res, fmt.Errorf("read %s: %s", res.Worktree, git.Reason(err))
	}
	if strings.TrimSpace(head) == sha {
		return res, nil
	}
	if clean, err := gc.IsClean(ctx, res.Worktree); err != nil || !clean {
		res.Reason = "has uncommitted changes, so it stays at its commit"
		res.Commit = ShortSHA(strings.TrimSpace(head))
		return res, nil
	}
	_, err = gc.Run(ctx, res.Worktree, "checkout", "--detach", "--quiet", sha)
	logGit(a, "git.checkout", res.Worktree, proj, mr.Ref, map[string]any{"detached": sha, "for": "mr checkout"}, err)
	if err != nil {
		return res, fmt.Errorf("move %s: %s", res.Worktree, git.Reason(err))
	}
	res.Action = "update"
	return res, nil
}

// mrHeadCommit finds the MR's head commit in the clone: the forge's reported
// head when the fetch brought it, else the merge-request ref (a fork's MR has
// no branch in the repo), else the source branch of a same-project MR.
func mrHeadCommit(ctx context.Context, gc git.Client, bare string, mr forge.MR) (string, error) {
	verify := func(rev string) (string, bool) {
		out, err := gc.Run(ctx, bare, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
		return strings.TrimSpace(out), err == nil && strings.TrimSpace(out) != ""
	}
	if mr.HeadSHA != "" {
		if sha, ok := verify(mr.HeadSHA); ok {
			return sha, nil
		}
	}
	for _, spec := range []string{
		fmt.Sprintf("refs/merge-requests/%d/head", mr.Ref.IID), // GitLab
		fmt.Sprintf("refs/pull/%d/head", mr.Ref.IID),           // GitHub
	} {
		local := fmt.Sprintf("refs/ade/mr/%d", mr.Ref.IID)
		if _, err := gc.Run(ctx, bare, "fetch", "--quiet", "origin", "+"+spec+":"+local); err == nil {
			if sha, ok := verify(local); ok {
				return sha, nil
			}
		}
	}
	// A fork's branch name may collide with a branch of the base repo: never
	// stand in for the fork's head.
	if mr.SourceBranch != "" && !mr.Fork {
		if sha, ok := verify("refs/remotes/origin/" + mr.SourceBranch); ok {
			return sha, nil
		}
	}
	return "", errors.New("the head of the merge request is not in the clone and cannot be fetched")
}

// ShortSHA is the 12-character form of a commit id.
func ShortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func logGit(a *app.App, action, target, proj string, ref forge.Ref, params map[string]any, err error) {
	e := audit.Entry{Action: action, Target: target, Item: ref.String(), Project: proj, Params: params, Outcome: "ok"}
	if err != nil {
		e.Outcome, e.Error = "error", err.Error()
	}
	_ = a.Audit.Append(e)
}
