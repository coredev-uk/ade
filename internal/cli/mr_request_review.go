package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

func init() { mrExtraCmds = append(mrExtraCmds, newMRRequestReviewCmd) }

// requestReviewView is `ade mr request-review --json`.
type requestReviewView struct {
	Ref     forge.Ref             `json:"ref"`
	Action  string                `json:"action"`
	DryRun  bool                  `json:"dry_run,omitempty"`
	Again   bool                  `json:"again,omitempty"`
	Skipped string                `json:"skipped,omitempty"`
	Targets []review.ReviewTarget `json:"targets"`
}

func newMRRequestReviewCmd() *cobra.Command {
	var again bool
	cmd := &cobra.Command{
		Use:   "request-review <ref> [@user...]",
		Short: "Ask people to review the MR, or to review it again after you pushed fixes",
		Long: "Asks the named users to review the merge request. A user who is not a reviewer yet is added\n" +
			"(the reviewers already on the MR stay); one who already is one is asked again: their state goes\n" +
			"back to requested and they are notified, whatever they said before.\n" +
			"--again also asks every current reviewer, those who approved, commented or requested changes\n" +
			"included; with no users named it asks only them. Without users and without --again there is\n" +
			"nobody to ask, so it refuses. Usernames may be given with or without @.\n" +
			"The command prints whom it asks, and what happens to each, before it writes; --dry-run stops there.\n" +
			"Audited as forge.mr.request_review.",
		Example: "  ade mr request-review acme/web!12 @rae --dry-run\n" +
			"  ade mr request-review acme/web!12 --again\n" +
			"  ade mr request-review acme/web!12 @rae @sam --again",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			named := review.ParseUsernames(strings.Join(args[1:], " "))
			if len(named) == 0 && !again {
				return errors.New("name whom to ask (@user ...) or pass --again to ask every current reviewer")
			}
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			mr, err := c.be.MR(ctx, c.ref)
			if err != nil {
				return err
			}
			if mr.State != "opened" {
				return fmt.Errorf("%s is %s, not open", c.ref, mr.State)
			}
			current, err := c.w.Reviewers(ctx, c.ref)
			if err != nil {
				return err
			}
			targets := review.PlanReviewRequest(current, named, again)
			v := requestReviewView{Ref: c.ref, Action: "request-review", DryRun: c.dry, Again: again, Targets: targets}
			if len(targets) == 0 {
				v.Skipped = "no reviewers"
				_ = appFrom(cmd).Audit.Append(audit.Entry{
					Action: "forge.mr.request_review", Item: c.ref.String(), Outcome: "skipped", Error: "no reviewers", DryRun: c.dry,
					Params: map[string]any{"users": []string{}, "again": again},
				})
				return out(cmd, v, "%s has no reviewers to ask again: name one with @user", c.ref)
			}
			if err := c.w.RequestReview(ctx, c.ref, review.Usernames(targets), again); err != nil {
				return err
			}
			head := verb(c.dry, "asked for a review on", "ask for a review on")
			return out(cmd, v, "%s %s:\n%s", head, c.ref, reviewPlanText(targets))
		},
	}
	cmd.Flags().BoolVar(&again, "again", false, "also ask every current reviewer to review again")
	return cmd
}

// reviewPlanText is the plan, one line per user.
func reviewPlanText(ts []review.ReviewTarget) string {
	lines := make([]string, len(ts))
	for i, t := range ts {
		lines[i] = "  " + reviewTargetLine(t)
	}
	return strings.Join(lines, "\n")
}

func reviewTargetLine(t review.ReviewTarget) string {
	if t.Action == review.RequestAdd {
		return "@" + t.Username + "  not a reviewer yet: added"
	}
	return "@" + t.Username + "  " + strings.ReplaceAll(t.Was, "_", " ") + ": asked again"
}
