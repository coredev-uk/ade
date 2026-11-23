package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/stagemove"
	"github.com/coredev-uk/ade/internal/startwork"
)

// stageOut is `ade issue stage --json`.
type stageOut struct {
	Ref     string          `json:"ref"`
	DryRun  bool            `json:"dry_run,omitempty"`
	Move    *startwork.Move `json:"move,omitempty"`
	Applied bool            `json:"applied"`
	Why     string          `json:"why,omitempty"` // nothing to move: the reason
	Stage   string          `json:"stage,omitempty"`
}

func newIssueStageCmd() *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "stage <ref>",
		Short: "Move a work item's workflow label to match its stage (audited as forge.issue.labels)",
		Long: "With labels.workflow.sync = \"ask\" (the default) a sync only reports that an item's stage moved on\n" +
			"(its MR went to review, was merged) and its workflow label did not. This makes that move: it\n" +
			"reads the tracker issue again, shows the label change (add workflow::in review, remove\n" +
			"workflow::in progress), and writes it on --yes or a yes at the prompt; --dry-run only shows it.\n" +
			"It works under sync = \"off\" too, and under \"auto\" when a sync has not got there yet. Nothing\n" +
			"moves when the label already matches, says blocked (somebody set it deliberately) or the item is\n" +
			"done: the reason is printed and the exit code is 0. ADE never closes issues.\n" +
			"<ref> is the tracker issue, a child issue or an MR of the item, or N when one project is registered.",
		Example: "  ade issue stage acme/tracker#15 --dry-run\n  ade issue stage 15 --yes --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			ctx := cmd.Context()
			ref, err := itemRef(cmd, args[0])
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
			t, err := stagemove.Resolve(ctx, a, nil, results, ref)
			if err != nil {
				return err
			}
			out := stageOut{Ref: t.Item.Ref.String(), DryRun: dryRun, Why: t.Why, Stage: t.Item.Stage.String()}
			w := cmd.OutOrStdout()
			if !t.Has {
				if jsonOut {
					return printJSON(w, out)
				}
				fmt.Fprintf(w, "%s: nothing to move: %s\n", out.Ref, t.Why)
				return nil
			}
			mv := t.Move
			out.Move = &mv
			if dryRun {
				if jsonOut {
					return printJSON(w, out)
				}
				fmt.Fprintf(w, "would write: %s (%s)\n", mv, mv.Writes())
				return nil
			}
			if !jsonOut {
				fmt.Fprintf(w, "%s (%s)\n", mv, mv.Writes())
			}
			if ok, err := confirmStage(cmd, mv.String(), yes); err != nil || !ok {
				if err == nil {
					err = errors.New("cancelled: nothing was written")
				}
				return err
			}
			stop, err := notify.Attach(ctx, a, notify.Channels{})
			if err != nil {
				return err
			}
			_, aerr := t.Apply(ctx, a)
			stop()
			out.Applied = aerr == nil
			if jsonOut {
				if err := printJSON(w, out); err != nil {
					return err
				}
				return aerr
			}
			if aerr == nil {
				fmt.Fprintf(w, "done: %s\n", mv)
			}
			return aerr
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the label change; write nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "write without asking")
	return cmd
}

// confirmStage asks before the label is written: --yes, or an interactive yes.
// A script that did not say --yes gets an error, never a silent write.
func confirmStage(cmd *cobra.Command, what string, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	f, ok := cmd.InOrStdin().(*os.File)
	if jsonOut || !ok || !isTerminal(f) || isDevNull(f) {
		return false, withCode(exitNeedsConfirm, errors.New("not moving the label without a confirmation: pass --yes to write it, or --dry-run to only look"))
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Move the label (%s)? [y/N] ", what)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}
