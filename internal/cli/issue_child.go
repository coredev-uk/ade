package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// newIssueChildCmd is `ade issue child`: the child-issue steps of `ade start`
// on their own, with no branch, MR, worktree or workflow move.
func newIssueChildCmd() *cobra.Command {
	var f startFlags
	cmd := &cobra.Command{
		Use:   "child <tracker-ref> [--repo <repo>]",
		Short: "Create the child issue of a tracker issue in a code repo, without starting work",
		Long: "Does what the first steps of `ade start` do and nothing else: creates the child issue in the\n" +
			"repo (copying type, assignee, milestone and iteration from the tracker issue), links it\n" +
			"relates_to the tracker issue, and places it under it in the forge's hierarchy where the forge\n" +
			"has one. No branch, MR, worktree, workflow label move or workspace. It is idempotent: an\n" +
			"existing child is found and reused. The default repos are the ones the app labels route to.\n" +
			"Writes are audited (forge.issue.create, .link, .parent); --dry-run shows them first.\n" +
			"<tracker-ref> is group/project#N, or N when one project is registered.",
		Example: "  ade issue child acme/tracker#128 --repo api\n  ade issue child 128 --repo api --dry-run --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.dryRun, _ = cmd.Flags().GetBool("dry-run")
			f.onlyChild = true
			return runStart(cmd, args[0], f, false)
		},
	}
	fl := cmd.Flags()
	fl.StringSliceVar(&f.repos, "repo", nil, "manifest repo to create the child in (repeatable); default: the repos the app labels route to")
	fl.Bool("dry-run", false, "show every write; change nothing")
	return cmd
}

// assignOut is `ade issue assign --json`.
type assignOut struct {
	Ref       forge.Ref `json:"ref"`
	Action    string    `json:"action"` // assign | unchanged
	DryRun    bool      `json:"dry_run,omitempty"`
	Assignees []string  `json:"assignees"`
}

func newIssueAssignCmd() *cobra.Command {
	var (
		dryRun, replace bool
		forgeFlag       string
	)
	cmd := &cobra.Command{
		Use:   "assign <ref>",
		Short: "Assign an issue to yourself (audited as forge.issue.assign)",
		Long: "Adds you to the assignees of the issue, keeping the others; --replace makes you the only\n" +
			"assignee. An issue you are assigned to already is left alone. <ref> is group/project#N (any\n" +
			"issue on a configured forge, the tracker's or a child's), or N when one project is registered.",
		Example: "  ade issue assign acme/api#31\n  ade issue assign 15 --replace --dry-run --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := itemRef(cmd, args[0])
			if err != nil {
				return err
			}
			if ref.Kind != forge.KindIssue {
				return fmt.Errorf("%s is a merge request: assign takes an issue", ref)
			}
			a := appFrom(cmd)
			ctx := cmd.Context()
			be, err := review.OpenBackend(ctx, a, ref, forgeFlag)
			if err != nil {
				return err
			}
			aw, ok := forge.Forge(be).(forge.AssigneeWriter)
			if !ok {
				return fmt.Errorf("this forge cannot change assignees: %w", forge.ErrUnavailable)
			}
			me, err := be.Me(ctx)
			if err != nil {
				return fmt.Errorf("who am I: %w", err)
			}
			is, err := be.Issue(ctx, ref)
			if err != nil {
				return fmt.Errorf("%s: %w", ref, err)
			}
			want := []forge.User{me}
			if !replace {
				want = slices.Clone(is.Assignees)
				if !slices.ContainsFunc(want, func(u forge.User) bool { return u.ID == me.ID }) {
					want = append(want, me)
				}
			}
			names := make([]string, len(want))
			for i, u := range want {
				names[i] = u.Username
			}
			res := assignOut{Ref: ref, Action: "assign", DryRun: dryRun, Assignees: names}
			if sameUsers(is.Assignees, want) {
				res.Action = "unchanged"
				return out(cmd, res, "%s is assigned to %s already", ref, strings.Join(names, ", "))
			}
			if dryRun {
				_ = a.Audit.Append(audit.Entry{
					Action: "forge.issue.assign", Target: ref.String(), Item: ref.String(),
					Params: map[string]any{"assignees": names}, DryRun: true, Outcome: "skipped",
				})
				return out(cmd, res, "dry-run: would assign %s to %s", ref, strings.Join(names, ", "))
			}
			err = aw.SetAssignees(ctx, ref, want)
			e := audit.Entry{
				Action: "forge.issue.assign", Target: ref.String(), Item: ref.String(),
				Params: map[string]any{"assignees": names, "replace": replace}, Outcome: "ok", URL: is.WebURL,
			}
			if err != nil {
				e.Outcome, e.Error = "error", err.Error()
			}
			_ = a.Audit.Append(e)
			if err != nil {
				return fmt.Errorf("assign %s: %w", ref, err)
			}
			return out(cmd, res, "assigned %s to %s", ref, strings.Join(names, ", "))
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the write; change nothing")
	cmd.Flags().BoolVar(&replace, "replace", false, "make you the only assignee instead of adding you")
	cmd.Flags().StringVar(&forgeFlag, "forge", "", "forge (from the user config) serving the issue; default: found from registered projects")
	return cmd
}

func sameUsers(a, b []forge.User) bool {
	if len(a) != len(b) {
		return false
	}
	for _, u := range a {
		if !slices.ContainsFunc(b, func(v forge.User) bool { return v.ID == u.ID }) {
			return false
		}
	}
	return true
}
