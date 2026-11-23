package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/editor"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

func init() { mrExtraCmds = append(mrExtraCmds, newMRDescribeCmd) }

// describeAction is the JSON shape of `ade mr describe`.
type describeAction struct {
	Ref    forge.Ref `json:"ref"`
	Action string    `json:"action"`
	DryRun bool      `json:"dry_run,omitempty"`
	// Unchanged is true when the MR already has that description: nothing was
	// written.
	Unchanged bool `json:"unchanged,omitempty"`
	Applied   bool `json:"applied"`
	Chars     int  `json:"chars"`
	// QuickActions are the quick-action commands in the new text that GitLab
	// runs when it saves it.
	QuickActions []string `json:"quick_actions,omitempty"`
}

func newMRDescribeCmd() *cobra.Command {
	var (
		bodyFile string
		edit     bool
		yes      bool
	)
	cmd := &cobra.Command{
		Use:   "describe <ref> (--body-file <file|-> | --edit)",
		Short: "Replace an MR's description with text you wrote (no AI)",
		Long: "Replaces the description of an open merge request with your own text, read from a file\n" +
			"(--body-file; - is stdin) or written in your editor (--edit, seeded with the current\n" +
			"description; host.editor, else $VISUAL/$EDITOR). Only the description is sent: the title,\n" +
			"draft state and everything else are left alone. The change is shown as a diff first and asked\n" +
			"about (--yes or an interactive yes; a script without --yes gets an error, never a silent\n" +
			"write), then audited as forge.mr.description with source manual. --dry-run shows the diff and\n" +
			"writes nothing. Your text is sent as it is: on GitLab, lines such as /close or /assign are\n" +
			"quick actions that GitLab runs when it saves the description, so they are listed in the\n" +
			"preview as a warning, not stripped. `ade ai describe` drafts one with a model instead.",
		Example: "  ade mr describe acme/api!12 --edit\n  ade mr describe acme/api!12 --body-file body.md --yes\n  git log -1 --format=%b | ade mr describe acme/api!12 --body-file - --yes\n  ade mr describe acme/api!12 --body-file body.md --dry-run",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if edit == (bodyFile != "") {
				return errors.New("pass exactly one of --body-file <file|-> (your text) or --edit (write it in your editor)")
			}
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			mr, err := c.be.MR(cmd.Context(), c.ref)
			if err != nil {
				return err
			}
			if mr.State != "opened" {
				return fmt.Errorf("%s is %s, not open: its description is left alone", c.ref, mr.State)
			}
			var text string
			if edit {
				text, err = editDescription(cmd, mr.Description)
			} else {
				text, err = readBodyFile(cmd, bodyFile)
			}
			if err != nil {
				return err
			}
			text = strings.TrimSpace(text)
			act := describeAction{Ref: c.ref, Action: "describe", DryRun: c.dry, Chars: len(text)}
			if text == "" {
				return errors.New("the new description is empty: not clearing the description of " + c.ref.String())
			}
			if act.Unchanged = text == strings.TrimSpace(mr.Description); act.Unchanged {
				return out(cmd, act, "%s already has this description: nothing to write", c.ref)
			}
			if quickActionsRun(cmd, c.ref, args[0]) {
				act.QuickActions = forge.QuickActions(text)
			}
			if !jsonOut {
				printDescribePreview(cmd.OutOrStdout(), c.ref, mr.Description, text, act.QuickActions)
			}
			if !c.dry {
				ok, err := confirmWrite(cmd, "Replace the description of "+c.ref.String()+" with this text?", yes)
				if err != nil {
					return err
				}
				if !ok {
					return out(cmd, act, "not written")
				}
			}
			if err := c.w.SetDescription(cmd.Context(), c.ref, text, "manual"); err != nil {
				return err
			}
			act.Applied = !c.dry
			return out(cmd, act, "%s", verb(c.dry, c.ref.String()+": description updated", "replace the description of "+c.ref.String()))
		},
	}
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "file holding the new description (- reads stdin)")
	cmd.Flags().BoolVar(&edit, "edit", false, "write the new description in your editor, starting from the current one")
	cmd.Flags().BoolVar(&yes, "yes", false, "write without asking")
	return cmd
}

// readBodyFile is --body-file: the named file, or stdin for -.
func readBodyFile(cmd *cobra.Command, name string) (string, error) {
	var b []byte
	var err error
	if name == "-" {
		b, err = io.ReadAll(cmd.InOrStdin())
	} else {
		b, err = os.ReadFile(name)
	}
	if err != nil {
		return "", fmt.Errorf("reading the new description: %w", err)
	}
	return string(b), nil
}

// editDescription is --edit: current in a temporary Markdown file the user's
// editor opens, and what the editor leaves behind.
func editDescription(cmd *cobra.Command, current string) (string, error) {
	f, err := os.CreateTemp("", "ade-mr-description-*.md")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(current); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ed := editor.Resolve(appFrom(cmd).Config.Host.Editor, editor.FromOS())
	c := ed.Command(f.Name(), 0, true)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, cmd.ErrOrStderr(), os.Stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("editor %s: %w", ed, err)
	}
	b, err := os.ReadFile(f.Name())
	return string(b), err
}

// quickActionsRun reports whether the forge serving ref runs quick actions in
// a description it stores: GitLab does, GitHub has none.
func quickActionsRun(cmd *cobra.Command, ref forge.Ref, arg string) bool {
	override, _ := cmd.Flags().GetString("forge")
	a := appFrom(cmd)
	name, err := review.ForgeFor(cmd.Context(), a, ref, override)
	if err != nil {
		return false
	}
	return a.Config.Forges[name].Kind == "gitlab"
}

func printDescribePreview(w io.Writer, ref forge.Ref, old, text string, quick []string) {
	fmt.Fprintf(w, "%s: description\n", ref)
	for _, ln := range lineDiff(old, text) {
		fmt.Fprintln(w, ln)
	}
	if len(quick) > 0 {
		fmt.Fprintf(w, "\nwarning: %s run as quick actions when GitLab saves this description; they are sent as written\n", strings.Join(quick, ", "))
	}
	fmt.Fprintln(w)
}

// confirmWrite asks before a forge write: --yes, or an interactive yes. A
// script that did not say --yes gets an error, never a silent write.
func confirmWrite(cmd *cobra.Command, question string, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	if f, ok := cmd.InOrStdin().(*os.File); !ok || !isTerminal(f) {
		return false, withCode(exitNeedsConfirm, fmt.Errorf("not writing without a confirmation: pass --yes (%s)", question))
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", question)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}

// lineDiff is a unified-style line diff of old to new: "+ " lines are added,
// "- " removed, "  " kept, with kept runs of more than six lines folded to
// their first and last three. Descriptions are short, so the LCS table is
// quadratic; a pair too large for it is shown as one removal and one addition.
func lineDiff(old, text string) []string {
	a, b := splitLines(old), splitLines(text)
	if len(a)*len(b) > 4_000_000 {
		var out []string
		for _, l := range a {
			out = append(out, "- "+l)
		}
		for _, l := range b {
			out = append(out, "+ "+l)
		}
		return out
	}
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			out = append(out, "  "+a[i])
			i++
			j++
		case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
			out = append(out, "+ "+b[j])
			j++
		default:
			out = append(out, "- "+a[i])
			i++
		}
	}
	return foldKept(out)
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// foldKept shortens each run of more than six unchanged lines to its first
// and last three around a "  …" line.
func foldKept(lines []string) []string {
	var out []string
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "  ") {
			out = append(out, lines[i])
			i++
			continue
		}
		j := i
		for j < len(lines) && strings.HasPrefix(lines[j], "  ") {
			j++
		}
		if n := j - i; n > 6 {
			out = append(out, lines[i:i+3]...)
			out = append(out, fmt.Sprintf("  … %d unchanged lines", n-6))
			out = append(out, lines[j-3:j]...)
		} else {
			out = append(out, lines[i:j]...)
		}
		i = j
	}
	return out
}
