package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/project"
)

func init() { mrExtraCmds = append(mrExtraCmds, newMRCheckoutCmd) }

// mrCheckedOut is `ade mr checkout --json`.
type mrCheckedOut struct {
	Ref     forge.Ref `json:"ref"`
	Project string    `json:"project"`
	Repo    string    `json:"repo"`
	Branch  string    `json:"branch"`
	// Commit is the MR's head the tree is at.
	Commit   string `json:"commit"`
	Worktree string `json:"worktree"`
	// Action is create (a new tree), update (an existing tree moved to the new
	// head), keep (left as it was: it has changes, or is on the head already).
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

func newMRCheckoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "checkout <ref>",
		Short: "Check out an MR's head as a detached mr-<iid> worktree, with no workspace tab",
		Long: "Fetches the repo and adds the worktree <root>/<project>/<repo>/mr-<iid>, detached at the head\n" +
			"of the merge request, to read, run or test someone else's change without a review tab\n" +
			"(`ade review <ref> --no-zellij` checks out the source branch itself instead). The tree has no\n" +
			"branch, so it never collides with the author's own branch and `ade gc` keeps it. A tree that\n" +
			"exists moves to the MR's new head only while it is clean; it is never touched otherwise.\n" +
			"With --json (or plain output) the path of the worktree is the result: `cd \"$(ade mr checkout\n" +
			"<ref> --json | jq -r .worktree)\"`. Remove it with `git worktree remove` when done.",
		Example: "  ade mr checkout acme/api!12\n  ade mr checkout acme/api!12 --json | jq -r .worktree",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			ctx, a := cmd.Context(), appFrom(cmd)
			mr, err := c.be.MR(ctx, c.ref)
			if err != nil {
				return fmt.Errorf("read %s: %w", c.ref, err)
			}
			t, err := findReviewRepo(ctx, a, c.ref)
			if err != nil {
				return err
			}
			res, err := checkoutMR(ctx, a, t, mr, c.dry)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), res)
			}
			verb := map[string]string{"create": "created", "update": "moved", "keep": "kept"}[res.Action]
			if res.DryRun {
				verb = "dry-run: would " + res.Action
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s at %s (%s)\n", res.Ref, verb, res.Worktree, res.Commit, res.Branch)
			if res.Reason != "" {
				fmt.Fprintln(cmd.OutOrStdout(), "  "+res.Reason)
			}
			return nil
		},
	}
}

// checkoutMR makes (or refreshes) the detached tree of mr.
func checkoutMR(ctx context.Context, a *app.App, t reviewTarget, mr forge.MR, dry bool) (mrCheckedOut, error) {
	res := mrCheckedOut{
		Ref: mr.Ref, Project: t.manifest.Name, Repo: t.repo, Branch: mr.SourceBranch, DryRun: dry,
		Worktree: filepath.Join(a.Root, t.manifest.Name, t.repo, fmt.Sprintf("mr-%d", mr.Ref.IID)),
	}
	if _, err := os.Stat(filepath.Join(t.bare, "HEAD")); err != nil {
		return res, fmt.Errorf("%s is not cloned: run `ade project add` for %s first", t.repo, t.manifest.Name)
	}
	gc, err := project.New(a, "user").GitClient(ctx, t.manifest.Forge, t.bare)
	if err != nil {
		return res, err
	}
	if err := gc.PruneWorktrees(ctx, t.bare); err != nil {
		return res, err
	}
	trees, err := gc.Worktrees(ctx, t.bare)
	if err != nil {
		return res, err
	}
	exists := slices.ContainsFunc(trees, func(w git.Worktree) bool { return filepath.Clean(w.Path) == filepath.Clean(res.Worktree) })
	if !exists {
		if _, err := os.Stat(res.Worktree); err == nil {
			return res, fmt.Errorf("%s exists but is not a worktree of %s", res.Worktree, t.repo)
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
	err = gc.Fetch(ctx, t.bare)
	logWorktree(a, "git.fetch", t.bare, t.manifest.Name, mr.Ref, nil, err)
	if err != nil {
		return res, err
	}
	sha, err := mrHeadCommit(ctx, gc, t.bare, mr)
	if err != nil {
		return res, err
	}
	res.Commit = shortSHA(sha)
	if exists {
		return refreshMRTree(ctx, a, gc, mr, res, sha)
	}
	_, err = gc.Run(ctx, t.bare, "worktree", "add", "--detach", res.Worktree, sha)
	logWorktree(a, "git.worktree.add", res.Worktree, t.manifest.Name, mr.Ref, map[string]any{"detached": sha, "repo": t.repo, "for": "mr checkout"}, err)
	if err != nil {
		return res, fmt.Errorf("check out %s: %s", mr.Ref, git.Reason(err))
	}
	for _, hook := range t.manifest.Repos[t.repo].Hooks.PostWorktree {
		_, herr := git.RunHook(ctx, res.Worktree, hook)
		logWorktree(a, "git.hook", res.Worktree, t.manifest.Name, mr.Ref, map[string]any{"hook": "post_worktree", "cmd": hook}, herr)
	}
	return res, nil
}

// refreshMRTree moves an existing mr-<iid> tree to sha when it is clean.
func refreshMRTree(ctx context.Context, a *app.App, gc git.Client, mr forge.MR, res mrCheckedOut, sha string) (mrCheckedOut, error) {
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
		res.Commit = shortSHA(strings.TrimSpace(head))
		return res, nil
	}
	_, err = gc.Run(ctx, res.Worktree, "checkout", "--detach", "--quiet", sha)
	logWorktree(a, "git.checkout", res.Worktree, res.Project, mr.Ref, map[string]any{"detached": sha, "for": "mr checkout"}, err)
	if err != nil {
		return res, fmt.Errorf("move %s: %s", res.Worktree, git.Reason(err))
	}
	res.Action = "update"
	return res, nil
}

// mrHeadCommit finds the MR's head commit in the clone: the forge's reported
// head when the fetch brought it, else the merge-request ref (a fork's MR has
// no branch in the repo), else the source branch.
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
	if mr.SourceBranch != "" {
		if sha, ok := verify("refs/remotes/origin/" + mr.SourceBranch); ok {
			return sha, nil
		}
	}
	return "", errors.New("the head of the merge request is not in the clone and cannot be fetched")
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
