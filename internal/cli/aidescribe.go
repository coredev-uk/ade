package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// describeOut is the JSON shape of `ade ai describe`.
type describeOut struct {
	aiOut
	// Description is the text that --apply writes: the model's answer with
	// quick actions stripped and the links to work items kept.
	Description string `json:"description,omitempty"`
	// Unchanged is true when the MR already has that description.
	Unchanged bool `json:"unchanged,omitempty"`
	Applied   bool `json:"applied"`
}

func newAIDescribeCmd() *cobra.Command {
	var (
		o        aiOpts
		apply    bool
		template string
	)
	cmd := &cobra.Command{
		Use:   "describe <mr-ref>",
		Short: "Draft an MR description from its template, commits and diff; --apply writes it to the MR after a confirmation",
		Long: "Reads the MR's diff and commits, the issue it closes and the project's MR template (--template,\n" +
			"else conventions.mr.template of the manifest, else \"Default\", else the only one), and drafts\n" +
			"the description. The draft is printed; nothing is written without --apply. --apply shows\n" +
			"what it will replace, asks (--yes or an interactive yes) and then updates only the\n" +
			"description of the MR, audited as forge.mr.description. Quick actions are stripped from the\n" +
			"draft and the lines linking work items (Closes #4, Parent ...) of the current description\n" +
			"are kept. Your private notes are not sent: the description is posted to the forge. The\n" +
			"result is cached by head SHA, commits and template, so an unchanged MR costs $0 the second\n" +
			"time (--refresh buys a new draft). A draft the model did not finish is never applied.\n" +
			"With ai.mode = \"off\" the command refuses and nothing else changes.",
		Example: "  ade ai describe acme/api!12\n  ade ai describe acme/api!12 --apply --yes\n  ade ai describe acme/api!12 --dry-run --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseMRRef(args[0])
			if err != nil {
				return err
			}
			e, err := openAI(cmd, ref, o.forge)
			if err != nil {
				return err
			}
			defer e.stop()
			in, err := ai.GatherDescribe(cmd.Context(), e.a, e.be, ref, template)
			if err != nil {
				return err
			}
			job := ai.DescribeJob(e.forgeName, in)
			res, out, err := e.runJob(cmd, job, o)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if o.dryRun {
				return emit(cmd, out)
			}
			do := describeOut{aiOut: out}
			do.Description = ai.PrepareDescription(res.Text, in.MR.Description)
			do.Text = do.Description
			do.Unchanged = strings.TrimSpace(in.MR.Description) == do.Description
			if !jsonOut {
				printDescription(w, ref, in.MR.Description, do)
			}
			if apply && !res.Truncated && !do.Unchanged && do.Description != "" {
				ok, err := confirmDescribe(cmd, ref, o.yes)
				if err != nil {
					return err
				}
				if ok {
					aw := review.Audited(e.be, e.a.Audit, false)
					if err := aw.SetDescription(cmd.Context(), ref, do.Description, "ai-describe"); err != nil {
						return err
					}
					do.Applied = true
					if !jsonOut {
						fmt.Fprintf(w, "%s: description updated\n", ref)
					}
				} else if !jsonOut {
					fmt.Fprintln(w, "not applied")
				}
			} else if apply && res.Truncated && !jsonOut {
				fmt.Fprintln(w, "not applied: the draft is cut short; run again with --refresh")
			}
			if jsonOut {
				return printJSON(w, do)
			}
			return nil
		},
	}
	o.bind(cmd)
	cmd.Flags().BoolVar(&apply, "apply", false, "write the draft to the MR (asks first; --yes skips the question)")
	cmd.Flags().StringVar(&template, "template", "", "MR template name (file in .gitlab/merge_request_templates, without .md)")
	return cmd
}

func printDescription(w io.Writer, ref forge.Ref, current string, o describeOut) {
	state := "draft, not applied"
	if o.Unchanged {
		state = "the MR already has this description"
	}
	fmt.Fprintf(w, "%s: description (%s)\n", ref, state)
	if strings.TrimSpace(current) != "" && !o.Unchanged {
		fmt.Fprintf(w, "  it would replace the current %d-line description\n", strings.Count(strings.TrimSpace(current), "\n")+1)
	}
	fmt.Fprintf(w, "\n%s\n\n", o.Description)
	printFooter(w, o.aiOut)
}

// confirmDescribe asks before the description is replaced: --yes, or an
// interactive yes. A script that did not say --yes gets an error, never a
// silent write.
func confirmDescribe(cmd *cobra.Command, ref forge.Ref, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	if f, ok := cmd.InOrStdin().(*os.File); !ok || !isTerminal(f) {
		return false, withCode(exitNeedsConfirm, fmt.Errorf("not replacing the description of %s without a confirmation: pass --yes", ref))
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Replace the description of %s with this draft? [y/N] ", ref)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}
