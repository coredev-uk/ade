package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/sprintmove"
	"github.com/coredev-uk/ade/internal/weight"
)

func init() { register(newIssueCmd) }

func newIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Explicit writes to a tracker issue (weight, sprint)",
	}
	cmd.AddCommand(newIssueWeightCmd())
	cmd.AddCommand(newIssueMoveCmd())
	return cmd
}

func newIssueWeightCmd() *cobra.Command {
	var dryRun, noChild bool
	cmd := &cobra.Command{
		Use:   "weight <tracker-ref> <n|none>",
		Short: "Set or clear an issue's weight (audited as forge.issue.weight)",
		Long: "Changing a weight is always an explicit action; ADE never re-weights silently (PLAN 3).\n" +
			"Where the forge licenses issue weight (GitLab Premium) the weight field is written; otherwise\n" +
			"the weight label (tracker.weight.pattern, default weight::{value}) is swapped and every old\n" +
			"weight label is removed explicitly. The child issue start work made for the item gets the\n" +
			"same weight unless --no-child. <n> is 1 or more; `none` clears it.\n" +
			"<tracker-ref> is group/project#N, or N when one project is registered.",
		Example: "  ade issue weight acme/tracker#15 2\n  ade issue weight 15 none --dry-run --json",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			to, err := parseWeight(args[1])
			if err != nil {
				return err
			}
			a := appFrom(cmd)
			ctx := cmd.Context()
			t, err := weight.Resolve(ctx, a, nil, args[0])
			if err != nil {
				return err
			}
			plans, err := t.Plans(to, !noChild)
			if err != nil {
				return err
			}
			var results []weight.Result
			var werr error
			if dryRun {
				for _, p := range plans {
					results = append(results, weight.Result{Plan: p})
				}
			} else {
				results, werr = weight.Apply(ctx, t.Forge, a, t.Project, "", plans)
			}
			w := cmd.OutOrStdout()
			if jsonOut {
				if err := printJSON(w, map[string]any{"ref": t.Issue.Ref.String(), "dry_run": dryRun, "results": results}); err != nil {
					return err
				}
				return werr
			}
			for _, r := range results {
				verb := "would write: "
				switch {
				case r.Plan.Unchanged:
					verb = ""
				case r.Applied:
					verb = "done: "
				case !dryRun:
					verb = "failed: "
				}
				fmt.Fprintf(w, "%s%s\n", verb, r.Plan.Describe())
				for _, n := range r.Plan.Notes {
					fmt.Fprintf(w, "  note: %s\n", n)
				}
			}
			return werr
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the writes; change nothing")
	cmd.Flags().BoolVar(&noChild, "no-child", false, "leave the child issue's weight alone")
	return cmd
}

// parseWeight reads "3" or "none" (nil: clear).
func parseWeight(s string) (*int, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "clear", "-":
		return nil, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("weight %q: want a whole number or none", s)
	}
	if n < 1 {
		return nil, errors.New("a weight is 1 or more; use none to clear it")
	}
	return &n, nil
}

// issueMoveNow is the clock `ade issue move` plans against; tests set it.
var issueMoveNow = time.Now

func newIssueMoveCmd() *cobra.Command {
	var to string
	var dryRun, withChildren, noChildren bool
	cmd := &cobra.Command{
		Use:   "move <tracker-ref>",
		Short: "Move an issue to another sprint, by default the next (audited as forge.issue.sprint)",
		Long: "Carries unfinished work into the next sprint. ADE moves issues between sprints but never\n" +
			"closes them: the project lead closes issues. Where the forge licenses iterations (GitLab\n" +
			"Premium) the iteration is set; where sprints are milestones (GitLab Free, GitHub) the\n" +
			"milestone is; where they are numbered labels the old sprint label is removed and the new\n" +
			"one added. --to next (the default) is the first sprint starting after the issue's own\n" +
			"sprint, which for an issue in the current sprint is the next one of the sprint window;\n" +
			"ADE creates no sprint, and when there is none the error says what to create. --to also\n" +
			"takes a sprint's title or key. Child issues start work made follow when they carry the same\n" +
			"sprint as the tracker issue; --with-children moves every open child, --no-children none.\n" +
			"The move raises issue.iteration_changed at once rather than waiting for the next sync.\n" +
			"<tracker-ref> is group/project#N, or N when one project is registered.",
		Example: "  ade issue move acme/tracker#15\n  ade issue move 15 --to \"Sprint 44\" --dry-run --json\n  ade issue move 15 --with-children",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if withChildren && noChildren {
				return errors.New("--with-children and --no-children contradict each other")
			}
			ch := sprintmove.ChildrenSame
			switch {
			case withChildren:
				ch = sprintmove.ChildrenAll
			case noChildren:
				ch = sprintmove.ChildrenNone
			}
			a := appFrom(cmd)
			ctx := cmd.Context()
			t, err := sprintmove.Resolve(ctx, a, nil, args[0], issueMoveNow())
			if err != nil {
				return err
			}
			dest, err := t.Find(to)
			if err != nil {
				return err
			}
			plans, err := t.Plans(dest, ch)
			if err != nil {
				return err
			}
			var results []sprintmove.Result
			var merr error
			if dryRun {
				for _, p := range plans {
					results = append(results, sprintmove.Result{Plan: p})
				}
			} else {
				stop, err := notify.Attach(ctx, a, notify.Channels{})
				if err != nil {
					return err
				}
				results, merr = sprintmove.Apply(ctx, t.Forge, a, t.Project, "", plans)
				stop()
			}
			w := cmd.OutOrStdout()
			if jsonOut {
				if err := printJSON(w, map[string]any{"ref": t.Issue.Ref.String(), "to": dest, "dry_run": dryRun, "results": results}); err != nil {
					return err
				}
				return merr
			}
			for _, r := range results {
				verb := "would write: "
				switch {
				case r.Plan.Unchanged:
					verb = ""
				case r.Applied:
					verb = "done: "
				case !dryRun:
					verb = "failed: "
				}
				fmt.Fprintf(w, "%s%s\n", verb, r.Plan.Describe())
				for _, n := range r.Plan.Notes {
					fmt.Fprintf(w, "  note: %s\n", n)
				}
				if r.Warning != "" {
					fmt.Fprintf(w, "  warning: %s\n", r.Warning)
				}
			}
			return merr
		},
	}
	cmd.Flags().StringVar(&to, "to", "next", "destination: next, or a sprint's title or key")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the writes; change nothing")
	cmd.Flags().BoolVar(&withChildren, "with-children", false, "move every open child issue, even one in another sprint")
	cmd.Flags().BoolVar(&noChildren, "no-children", false, "leave the child issues alone")
	return cmd
}
