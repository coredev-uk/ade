package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/forge"
)

func init() {
	mrExtraCmds = append(mrExtraCmds,
		func() *cobra.Command { return newMRStateCmd(true) },
		func() *cobra.Command { return newMRStateCmd(false) },
	)
}

// newMRStateCmd builds `ade mr close` (closing = true) or `ade mr reopen`.
// Closing needs --yes (or --dry-run): other people see the MR go. Reopening
// puts it back and needs none. A merged MR does neither; the state is read
// first so the refusal is plain, before any write.
func newMRStateCmd(closing bool) *cobra.Command {
	var yes bool
	use, short, from, did, would, event := "reopen <ref>", "Reopen a closed MR", "closed", "reopened", "reopen", "reopen"
	if closing {
		use, short, from, did, would, event = "close <ref> --yes", "Close an open MR without merging it (refuses without --yes)", "opened", "closed", "close", "close"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: short + ". Works on GitLab and GitHub; a merged MR can be neither closed nor reopened.\n" +
			"Reopening is refused while another open MR has the same source and target branch.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dry, _ := cmd.Flags().GetBool("dry-run"); closing && !yes && !dry {
				return errors.New("refusing to close without --yes")
			}
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			mr, err := c.be.MR(cmd.Context(), c.ref)
			if err != nil {
				return err
			}
			if mr.State != from {
				state := mr.State
				if state == "opened" {
					state = "open"
				}
				return fmt.Errorf("%s is %s, not %s: nothing to %s", c.ref, state, map[string]string{"opened": "open", "closed": "closed"}[from], would)
			}
			if closing {
				err = c.w.CloseMR(cmd.Context(), c.ref)
			} else {
				err = c.w.ReopenMR(cmd.Context(), c.ref)
			}
			if errors.Is(err, forge.ErrExists) {
				return fmt.Errorf("%s was not reopened: another open MR already has the branch %s → %s (%w)", c.ref, mr.SourceBranch, mr.TargetBranch, err)
			}
			if err != nil {
				return err
			}
			return out(cmd, action{Ref: c.ref, Action: event, DryRun: c.dry}, "%s %s", verb(c.dry, did, would), c.ref)
		},
	}
	if closing {
		cmd.Flags().BoolVar(&yes, "yes", false, "confirm closing the MR")
	}
	return cmd
}
