package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/workspace"
)

func newWsInstallCmd() *cobra.Command {
	var (
		tf    treeFlags
		force bool
	)
	cmd := &cobra.Command{
		Use:   "install [worktree]",
		Short: "Run the install hooks of a worktree's workspace",
		Long: `Runs [repos.<r>.hooks] install in every worktree the workspace uses (the
worktree itself, then the trees its required repos and services run from), each
inside its repo's dev shell. A worktree whose commands already succeeded is
skipped: the result is remembered per worktree and re-run only when the
commands change. --force runs them again.

The workspace's services tab runs this first; its service panes wait for it.
Each command is audited as git.hook with hook=install.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			trees, err := tf.options()
			if err != nil {
				return err
			}
			a := appFrom(cmd)
			loc, err := workspace.Locate(a.Root, worktreeArg(args))
			if err != nil {
				return err
			}
			mgr, err := workspace.NewManager(a)
			if err != nil {
				return err
			}
			var plan *workspace.Plan
			if _, ok := loc.Manifest.Workspaces[tf.workspace]; ok {
				if plan, err = mgr.Plan(cmd.Context(), loc, workspace.PlanOptions{Workspace: tf.workspace, Trees: trees}); err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				out = cmd.ErrOrStderr() // stdout carries the JSON result
			}
			results := installTargets(cmd.Context(), a, mgr, mgr.InstallTargets(loc, plan, true), loc.Project, force, out)
			if jsonOut {
				if perr := printJSON(cmd.OutOrStdout(), results); perr != nil {
					return perr
				}
			} else if len(results) == 0 {
				fmt.Fprintln(out, "no install hooks for this workspace")
			}
			return installError(results)
		},
	}
	tf.add(cmd)
	cmd.Flags().BoolVar(&force, "force", false, "run the commands again even if they already succeeded")
	return cmd
}

// installTargets installs each target, continuing past a failed one, and
// reports every result on out.
func installTargets(ctx context.Context, a *app.App, mgr *workspace.Manager, targets []workspace.InstallTarget, project string, force bool, out io.Writer) []workspace.InstallResult {
	results := make([]workspace.InstallResult, 0, len(targets))
	for _, t := range targets {
		fmt.Fprintf(out, "install %s (%s)\n", t.Repo, t.Tree)
		res := mgr.Install(ctx, t, workspace.InstallOptions{
			Force: force, Out: out,
			Audit: func(t workspace.InstallTarget, command string, err error) {
				e := audit.Entry{
					Action: "git.hook", Target: t.Tree, Project: project, Outcome: "ok",
					Params: map[string]any{"hook": "install", "cmd": command},
				}
				if err != nil {
					e.Outcome, e.Error = "error", err.Error()
				}
				_ = a.Audit.Append(e) // an unwritable audit log must not undo an install
			},
		})
		results = append(results, res)
		line := "  " + string(res.Outcome)
		if res.Detail != "" {
			line += ": " + res.Detail
		}
		fmt.Fprintln(out, line)
	}
	return results
}

func installError(results []workspace.InstallResult) error {
	for _, r := range results {
		if r.Outcome == workspace.InstallFailed {
			return withCode(1, errors.New("install failed in "+r.Tree))
		}
	}
	return nil
}
