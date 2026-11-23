package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/forge"
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
	// SkippedHooks are the post_worktree hooks a fork MR's tree did not run.
	SkippedHooks []string `json:"skipped_hooks,omitempty"`
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
			if n := project.SkippedHooksNotice(res.Worktree, res.SkippedHooks); n != "" {
				fmt.Fprintln(cmd.OutOrStdout(), "  "+n)
			}
			return nil
		},
	}
}

// checkoutMR makes (or refreshes) the detached tree of mr.
func checkoutMR(ctx context.Context, a *app.App, t reviewTarget, mr forge.MR, dry bool) (mrCheckedOut, error) {
	tree, err := project.CheckoutMR(ctx, a, t.manifest, t.repo, mr, dry)
	return mrCheckedOut{
		Ref: mr.Ref, Project: t.manifest.Name, Repo: t.repo, Branch: mr.SourceBranch, DryRun: dry,
		SkippedHooks: tree.SkippedHooks, Worktree: tree.Worktree, Commit: tree.Commit, Action: tree.Action, Reason: tree.Reason,
	}, err
}
