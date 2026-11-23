package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/commitprefix"
)

func newWsHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "The commit-prefix git hook of a worktree",
	}
	cmd.AddCommand(newWsHookInstallCmd(), newWsHookRemoveCmd())
	return cmd
}

// hookOut is `ade ws hook install|remove --json`.
type hookOut struct {
	commitprefix.HookPlan
	DryRun  bool   `json:"dry_run,omitempty"`
	Outcome string `json:"outcome"` // installed | unchanged | removed | absent | refused
	Detail  string `json:"detail,omitempty"`
}

func newWsHookInstallCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "install [worktree]",
		Short: "Install the commit-msg hook that prefixes the MR's first commit",
		Long: `Writes a commit-msg hook that prepends the worktree's ade.commitPrefix (the
manifest's conventions.commit_prefix, recorded per worktree by start work and
adopt) to the first commit of the MR, so commits you make yourself carry it too.
It leaves a message that already starts with the prefix, merge, squash, revert
and fixup messages, and every commit after the first (a branch already ahead of
ade.target) as they are.

The hook lives in <git dir>/ade-hooks of the worktree and is switched on by that
worktree's own core.hooksPath (extensions.worktreeConfig): no other worktree and
no shared git config changes. The other hooks of the hooks directory git used
before (the repo's, or a shared core.hooksPath) keep running through forwarding
stubs ade writes next to the hook; run this again after adding one.

A commit-msg hook of your own, in that directory, is never overwritten: the
command says where it is and stops (exit 2). Running it again is a no-op.
--dry-run reports the plan and writes nothing. The manifest key
conventions.commit_prefix_hook = true does this on start work.`,
		Example: "  ade ws hook install\n  ade ws hook install ~/ade/acme/api/42-fix-export --dry-run",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWsHook(cmd, worktreeArg(args), dryRun, false)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be written; touch nothing")
	return cmd
}

func newWsHookRemoveCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "remove [worktree]",
		Short: "Remove the commit-msg hook ade installed",
		Long: `Unsets the worktree's core.hooksPath when it is ade's hooks directory and deletes
the files ade wrote there (the hook and its forwarding stubs). Files of your own
are never touched. --dry-run reports and writes nothing.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWsHook(cmd, worktreeArg(args), dryRun, true)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be removed; touch nothing")
	return cmd
}

func runWsHook(cmd *cobra.Command, tree string, dryRun, remove bool) error {
	a := appFrom(cmd)
	ctx := cmd.Context()
	plan, err := commitprefix.PlanHook(ctx, tree)
	if err != nil {
		return err
	}
	res := hookOut{HookPlan: plan, DryRun: dryRun}
	action := "git.hook.install"
	switch {
	case remove:
		action = "git.hook.remove"
		res.Outcome = "absent"
		if dryRun {
			if plan.Present {
				res.Outcome = "removed"
			}
		} else if _, changed, rerr := commitprefix.RemoveHook(ctx, tree); rerr != nil {
			err = rerr
		} else if changed {
			res.Outcome = "removed"
		}
	case plan.Foreign != "":
		res.Outcome, res.Detail = "refused", (&commitprefix.ForeignHookError{Path: plan.Foreign}).Error()
		err = withCode(2, errors.New(res.Detail))
	case commitprefix.Read(ctx, plan.Worktree) == "":
		err = commitprefix.ErrNoPrefix
	case plan.Installed:
		res.Outcome = "unchanged"
	case dryRun:
		res.Outcome = "installed"
	default:
		if err = plan.Apply(ctx); err == nil {
			res.Outcome = "installed"
		}
	}
	if err == nil || res.Outcome == "refused" {
		auditHook(a.Audit, action, res, err, dryRun)
	}
	if jsonOut {
		if perr := printJSON(cmd.OutOrStdout(), res); perr != nil {
			return perr
		}
		return err
	}
	if res.Outcome != "refused" && err == nil {
		fmt.Fprintln(cmd.OutOrStdout(), hookLine(res))
	}
	return err
}

// hookLine is the one-line report of an install or removal.
func hookLine(r hookOut) string {
	would := ""
	if r.DryRun {
		would = "would be "
	}
	switch r.Outcome {
	case "installed":
		s := fmt.Sprintf("commit-prefix hook %sinstalled in %s (core.hooksPath %s)", would, r.Worktree, r.Dir)
		if len(r.Forward) > 0 {
			s += fmt.Sprintf("; %d other hook(s) of %s keep running", len(r.Forward), r.Prev)
		}
		return s
	case "unchanged":
		return "commit-prefix hook already installed in " + r.Worktree
	case "removed":
		return fmt.Sprintf("commit-prefix hook %sremoved from %s", would, r.Worktree)
	}
	return "no commit-prefix hook of ade's in " + r.Worktree
}

// auditHook records an install or removal; a dry run and a no-op leave a skipped
// or ok entry like the other local writes.
func auditHook(log *audit.Log, action string, r hookOut, err error, dryRun bool) {
	if dryRun || r.Outcome == "unchanged" || r.Outcome == "absent" {
		return
	}
	e := audit.Entry{
		Action: action, Target: r.Worktree, Outcome: "ok",
		Params: map[string]any{"hook": "commit-msg", "hooks_dir": r.Dir, "forwarded": r.Forward},
	}
	if err != nil {
		e.Outcome, e.Error = "error", err.Error()
	}
	_ = log.Append(e)
}
