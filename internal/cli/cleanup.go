package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/overview"
	"github.com/coredev-uk/ade/internal/project"
)

func init() { register(newCleanupCmd) }

// cleanupOut is `ade cleanup --json`.
type cleanupOut struct {
	Item      string             `json:"item"`
	DryRun    bool               `json:"dry_run,omitempty"`
	Trees     []gcRow            `json:"trees"`
	Baselines []project.Baseline `json:"baselines"`
}

func newCleanupCmd() *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "cleanup <ref>",
		Short: "Clean up one finished item: remove its worktrees, fast-forward the default branch, delete the local branches",
		Long: `Per-item version of ade gc. For the work item that <ref> (its tracker issue, a child issue
or an MR) belongs to, in the cache, it judges each of the item's worktrees exactly as ade gc
does: the merge requests of its branch must all be merged or closed (at least one), the tree
must be clean and everything committed pushed. Then, for the trees that pass, it removes the
worktree, fast-forwards the default-branch baseline tree of the repo (after a fetch; only a clean
tree with nothing of its own moves) and deletes the local branch with git branch -d, which keeps
a branch git does not see as merged (a squash merge). Nothing is ever forced.

A tree that fails a check stays, with the reason, and the command exits 2. --dry-run only
reports (it still fetches). Without --yes it asks first and refuses to run unattended.
<ref> is group/project#N or group/project!N, or N when one project is registered.`,
		Example: "  ade cleanup acme/tracker#128\n  ade cleanup acme/api!12 --dry-run --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCleanup(cmd, args[0], dryRun, yes)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be removed and why the rest stays; touch no worktree or branch (remote-tracking refs are still fetched)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "remove without asking")
	return cmd
}

func runCleanup(cmd *cobra.Command, arg string, dryRun, yes bool) error {
	a := appFrom(cmd)
	ctx := cmd.Context()
	ref, err := itemRef(cmd, arg)
	if err != nil {
		return err
	}
	st, err := a.Cache()
	if err != nil {
		return err
	}
	results, err := forgesync.Load(st)
	if err != nil {
		return err
	}
	_, wi, ok := overview.Find(results, ref)
	if !ok {
		return fmt.Errorf("%s is not in the cache: run `ade sync` first", ref)
	}
	var paths []string
	for _, t := range newTreeFinder(a.Root).of(ctx, wi) {
		paths = append(paths, t.Path)
	}
	res := cleanupOut{Item: wi.Ref.String(), DryRun: dryRun, Trees: []gcRow{}, Baselines: []project.Baseline{}}
	w := cmd.OutOrStdout()
	if len(paths) == 0 {
		if jsonOut {
			return printJSON(w, res)
		}
		fmt.Fprintf(w, "%s has no worktrees\n", wi.Ref)
		return nil
	}
	ps := project.New(a, "user")
	trees, err := ps.TreesAt(ctx, wi.Project, paths, forgeLookup(a))
	if err != nil {
		return err
	}
	var doomed []int
	for i, t := range trees {
		res.Trees = append(res.Trees, gcRow{Tree: t, Action: "keep"})
		if t.Removable() {
			res.Trees[i].Action = "remove"
			doomed = append(doomed, i)
		}
	}
	if !jsonOut {
		printGC(w, res.Trees)
	}
	failed := 0
	if len(doomed) > 0 {
		proceed := dryRun
		if !dryRun {
			if proceed, err = confirmCleanup(cmd, len(doomed), yes); err != nil {
				return err
			}
		}
		if proceed {
			failed = cleanTrees(ctx, ps, &res, doomed, dryRun)
			if !jsonOut {
				printCleanup(w, res, doomed)
			}
		}
	}
	if jsonOut {
		if err := printJSON(w, res); err != nil {
			return err
		}
	}
	if dryRun {
		return nil
	}
	kept := len(res.Trees) - len(doomed)
	switch {
	case failed > 0:
		return withCode(2, fmt.Errorf("%d of %d worktrees could not be removed", failed, len(doomed)))
	case kept > 0:
		return withCode(2, fmt.Errorf("%s kept: %s", plural(kept, "worktree"), keptWhy(res.Trees)))
	}
	return nil
}

func confirmCleanup(cmd *cobra.Command, n int, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	return askYes(cmd, fmt.Sprintf("remove %s and clean up?", plural(n, "worktree")))
}

func keptWhy(rows []gcRow) string {
	var parts []string
	for _, r := range rows {
		if !r.Removable() {
			parts = append(parts, r.Label()+": "+strings.Join(r.Keep, "; "))
		}
	}
	return strings.Join(parts, " | ")
}

// cleanTrees removes the worktrees, fast-forwards each repo's baseline once,
// then deletes the branches, which the fast-forward lets git see as merged. A
// dry run only fills in the baselines' plan. It returns how many removals failed.
func cleanTrees(ctx context.Context, ps *project.Service, res *cleanupOut, doomed []int, dry bool) (failed int) {
	var removed []int
	for _, i := range doomed {
		if dry {
			continue
		}
		if err := ps.RemoveWorktree(ctx, res.Trees[i].Tree); err != nil {
			res.Trees[i].Error = err.Error()
			failed++
			continue
		}
		res.Trees[i].Removed = true
		removed = append(removed, i)
	}
	if dry {
		removed = doomed
	}
	var seen []string
	for _, i := range removed {
		t := res.Trees[i].Tree
		if key := t.Project + "/" + t.Repo; !slices.Contains(seen, key) {
			seen = append(seen, key)
			res.Baselines = append(res.Baselines, ps.FastForwardBaseline(ctx, t.Project, t.Repo, dry))
		}
	}
	if !dry {
		for _, i := range removed {
			res.Trees[i].BranchKept = !ps.DeleteBranch(ctx, res.Trees[i].Tree)
		}
	}
	return failed
}

func printCleanup(w io.Writer, res cleanupOut, doomed []int) {
	for _, i := range doomed {
		r := res.Trees[i]
		switch {
		case res.DryRun:
		case r.Error != "":
			fmt.Fprintf(w, "failed  %s  %s\n", r.Label(), r.Error)
		case r.BranchKept:
			fmt.Fprintf(w, "removed %s (branch %s kept: git does not consider it merged; `git branch -D` deletes it)\n", r.Label(), r.Branch)
		default:
			fmt.Fprintf(w, "removed %s, deleted branch %s\n", r.Label(), r.Branch)
		}
	}
	for _, b := range res.Baselines {
		name := b.Project + "/" + b.Repo + "/" + b.Branch
		switch b.Action {
		case "fast-forward":
			verb := "fast-forwarded"
			if res.DryRun {
				verb = "would fast-forward"
			}
			fmt.Fprintf(w, "%s %s (%d new)\n", verb, name, b.Behind)
		case "current":
			fmt.Fprintf(w, "%s is up to date\n", name)
		default:
			fmt.Fprintf(w, "%s not updated: %s\n", name, b.Reason)
		}
	}
}
