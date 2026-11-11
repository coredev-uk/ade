package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/weight"
)

func init() { register(newIssueCmd) }

func newIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Explicit writes to a tracker issue (weight)",
	}
	cmd.AddCommand(newIssueWeightCmd())
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
