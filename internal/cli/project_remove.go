package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/project"
)

func newProjectRemoveCmd() *cobra.Command {
	var dryRun, yes, force bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Unregister a project and delete its clones",
		Long: `Removes the project from your config and deletes <root>/<name>: its bare clones,
baseline trees and feature worktrees. It refuses when a worktree has uncommitted
changes or commits that no remote branch has (or a local branch nobody pushed),
and says where; --force deletes anyway. Item records and the audit log are kept.

--dry-run only reports. Without --yes it asks first (and refuses to run unattended).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			ps := project.New(a, "user")
			plan, err := ps.PlanRemove(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			blocked := len(plan.Blockers) > 0 && !force
			if jsonOut {
				if dryRun || blocked {
					if err := printJSON(w, plan); err != nil {
						return err
					}
				}
			} else {
				printRemovePlan(w, plan, force)
			}
			if blocked {
				return withCode(2, fmt.Errorf("project %s holds work that exists only here (use --force to delete it anyway)", plan.Name))
			}
			if dryRun {
				return nil
			}
			if !yes {
				if ok, err := askYes(cmd, "delete "+plan.Dir+"?"); err != nil || !ok {
					return err
				}
			}
			if err := ps.Remove(cmd.Context(), plan, force); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(w, plan)
			}
			fmt.Fprintf(w, "removed project %s\n", plan.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be deleted; change nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking")
	cmd.Flags().BoolVar(&force, "force", false, "delete even when worktrees hold uncommitted or unpushed work")
	return cmd
}

func printRemovePlan(w io.Writer, plan project.RemovePlan, force bool) {
	fmt.Fprintf(w, "project %s: %s (%d repos)\n", plan.Name, plan.Dir, plan.Repos)
	for _, b := range plan.Blockers {
		verb := "keeps"
		if force {
			verb = "would lose"
		}
		fmt.Fprintf(w, "  %s %s: %s\n", verb, b.Where, strings.Join(b.Reasons, "; "))
	}
	if len(plan.Blockers) == 0 {
		fmt.Fprintln(w, "  every tree is clean and pushed")
	}
	if !plan.Registered {
		fmt.Fprintln(w, "  not in the user config: only the directory is deleted")
	}
}
