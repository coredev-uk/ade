package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/workspace"
)

func init() { register(newPushCmd) }

// pushOut is `ade push --json`.
type pushOut struct {
	project.PushPlan
	DryRun bool `json:"dry_run,omitempty"`
	Pushed bool `json:"pushed"`
}

func newPushCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "push [worktree]",
		Short: "Push a worktree's branch to origin, showing the commits first (audited as git.push)",
		Long: "Pushes the branch of a worktree of an ADE project (the current directory, else the one named) to\n" +
			"origin and makes it track the remote branch, with the forge's write token: the same push `ade agent push`\n" +
			"makes for an agent's commits, for your own work. The merge request that tracks the branch updates by\n" +
			"itself. It first prints the branch, the remote branch it updates, how many commits are ahead (read from\n" +
			"the refs of the last fetch) and their subjects; --dry-run stops there.\n\n" +
			"It never forces. It refuses, changing nothing, the repo's default branch, a detached HEAD and a branch\n" +
			"that diverged from its remote branch (update it first, e.g. git pull --rebase). A branch with nothing\n" +
			"ahead says so and exits 0. Uncommitted changes are not pushed; the output warns when there are some.\n" +
			"It refuses to run inside an agent run (ADE_AGENT_RUN=1): ADE pushes an agent's work after you reviewed\n" +
			"the diff, with `ade agent push`.",
		Example: "  ade push\n  ade push ~/ade/acme/api/42-limit --dry-run --json",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv(workspace.AgentRunEnv) == "1" {
				return errors.New("ade push is not for agents: this is an agent run, and ADE pushes an agent's work after the user reviewed the diff (ade agent push)")
			}
			a := appFrom(cmd)
			ctx := cmd.Context()
			ps := project.New(a, "user")
			dir := worktreeArg(args)
			if abs, err := filepath.Abs(dir); err == nil {
				dir = abs
			}
			plan, err := ps.PushPlan(ctx, dir)
			if err != nil {
				return err
			}
			out := pushOut{PushPlan: plan, DryRun: dryRun}
			w := cmd.OutOrStdout()
			if !jsonOut {
				printPushPlan(w, plan, dryRun)
			}
			if !plan.Pushable() {
				if jsonOut {
					if err := printJSON(w, out); err != nil {
						return err
					}
				}
				if plan.Nothing {
					return nil
				}
				return withCode(2, errors.New(plan.Refuse))
			}
			if dryRun {
				if jsonOut {
					return printJSON(w, out)
				}
				return nil
			}
			perr := ps.Push(ctx, plan)
			out.Pushed = perr == nil
			if jsonOut {
				if err := printJSON(w, out); err != nil {
					return err
				}
				return perr
			}
			if perr == nil {
				fmt.Fprintf(w, "pushed %s to %s\n", plan.Label(), plan.Upstream)
			}
			return perr
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the commits that would be pushed; push nothing")
	return cmd
}

func printPushPlan(w io.Writer, p project.PushPlan, dry bool) {
	fmt.Fprintf(w, "%s  %s", p.Label(), p.Branch)
	if p.Upstream != "" {
		fmt.Fprintf(w, " → %s", p.Upstream)
	}
	if p.NewBranch {
		fmt.Fprint(w, " (new branch)")
	}
	fmt.Fprintln(w)
	if p.Refuse != "" {
		fmt.Fprintf(w, "  %s\n", p.Refuse)
		return
	}
	verb := "pushing"
	if dry {
		verb = "would push"
	}
	fmt.Fprintf(w, "  %s %s", verb, plural(p.Ahead, "commit"))
	if p.SetUpstream && !p.NewBranch {
		fmt.Fprint(w, " and set it as the upstream")
	}
	fmt.Fprintln(w)
	for _, c := range p.Commits {
		fmt.Fprintf(w, "    %s\n", c)
	}
	if p.Ahead > len(p.Commits) {
		fmt.Fprintf(w, "    … and %d more\n", p.Ahead-len(p.Commits))
	}
	if p.Dirty {
		fmt.Fprintln(w, "  note: uncommitted changes stay behind: a push sends commits only")
	}
}
