package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

func init() { register(newMRCmd) }

// mrCall is what every mr/review command needs: the parsed ref, the forge
// driver for it, and a writer that audits (or, under --dry-run, only records)
// each write.
type mrCall struct {
	ref forge.Ref
	be  review.Backend
	w   *review.AuditedWriter
	dry bool
}

// openMR parses ref and opens its forge. The --forge and --dry-run flags are
// persistent flags of the parent command.
func openMR(cmd *cobra.Command, refArg string) (*mrCall, error) {
	ref, err := parseMRRef(refArg)
	if err != nil {
		return nil, err
	}
	override, _ := cmd.Flags().GetString("forge")
	dry, _ := cmd.Flags().GetBool("dry-run")
	a := appFrom(cmd)
	be, err := review.OpenBackend(cmd.Context(), a, ref, override)
	if err != nil {
		return nil, err
	}
	return &mrCall{ref: ref, be: be, w: review.Audited(be, a.Audit, dry), dry: dry}, nil
}

func parseMRRef(s string) (forge.Ref, error) {
	ref, err := forge.ParseRef(s)
	if err != nil {
		return ref, err
	}
	if ref.Kind != forge.KindMR {
		return ref, fmt.Errorf("%s is an issue: want a merge request like group/project!3", s)
	}
	return ref, nil
}

// mrFlags adds the flags every mr/review command shares.
func mrFlags(c *cobra.Command) {
	c.PersistentFlags().String("forge", "", "forge (from the user config) serving the MR; default: found from registered projects")
	c.PersistentFlags().Bool("dry-run", false, "show what a write would do; change nothing")
}

func newMRCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mr",
		Short: "Read and act on merge requests (show, diff, reply, resolve, approve, merge, ...)",
		Long: "Scriptable merge-request commands. <ref> is group/project!N. Writes are audited\n" +
			"(forge.* actions) and honour --dry-run; --json prints machine-readable output.",
	}
	mrFlags(cmd)
	cmd.AddCommand(
		newMRShowCmd(), newMRDiffCmd(), newMRReplyCmd(), newMRResolveCmd(),
		newMRApproveCmd("approve", "Approve the MR at its current head", true),
		newMRApproveCmd("unapprove", "Withdraw your approval", false),
		newMRDraftCmd("ready", "Mark the MR ready (remove Draft)", false),
		newMRDraftCmd("draft", "Mark the MR draft", true),
		newMRMergeCmd(), newMRRetryCmd(),
	)
	for _, b := range mrExtraCmds {
		cmd.AddCommand(b())
	}
	return cmd
}

// out prints v as JSON under --json, otherwise the text.
func out(cmd *cobra.Command, v any, format string, args ...any) error {
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), v)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), format+"\n", args...)
	return err
}

// action is the JSON shape of a write's result.
type action struct {
	Ref      forge.Ref `json:"ref"`
	Action   string    `json:"action"`
	DryRun   bool      `json:"dry_run,omitempty"`
	Skipped  string    `json:"skipped,omitempty"`
	Target   string    `json:"target,omitempty"`
	NoteID   int64     `json:"note_id,omitempty"`
	Resolved *bool     `json:"resolved,omitempty"`
	SHA      string    `json:"sha,omitempty"`
	// Message and SquashMessage are the commit messages a merge sends: the
	// squash message carries the commit prefix when one applies.
	Message       string `json:"message,omitempty"`
	SquashMessage string `json:"squash_message,omitempty"`
}

// indent puts two spaces before every line of s.
func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

func verb(dry bool, did, would string) string {
	if dry {
		return "dry-run: would " + would
	}
	return did
}

// bodyArg returns s, or all of stdin when s is "-".
func bodyArg(cmd *cobra.Command, s string) (string, error) {
	if s != "-" {
		return s, nil
	}
	b, err := io.ReadAll(cmd.InOrStdin())
	return strings.TrimRight(string(b), "\n"), err
}

// ---- show ------------------------------------------------------------------

type mrView struct {
	MR      forge.MR           `json:"mr"`
	Threads []forge.Discussion `json:"threads"`
}

func newMRShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <ref>",
		Short: "Summary, review threads and pipeline of an MR",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			mr, err := c.be.MR(cmd.Context(), c.ref)
			if err != nil {
				return err
			}
			ds, err := c.be.Discussions(cmd.Context(), c.ref)
			if err != nil {
				return err
			}
			threads := ds[:0:0]
			for _, d := range ds {
				if len(d.Notes) == 0 || !d.Notes[0].System {
					threads = append(threads, d)
				}
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), mrView{MR: mr, Threads: orEmpty(threads)})
			}
			printMR(cmd.OutOrStdout(), mr, threads)
			return nil
		},
	}
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func printMR(w io.Writer, mr forge.MR, threads []forge.Discussion) {
	state := mr.State
	if mr.Draft {
		state += " · draft"
	}
	fmt.Fprintf(w, "%s · %s\n", mr.Ref, mr.Title)
	fmt.Fprintf(w, "  %s · %s → %s · by %s\n", state, mr.SourceBranch, mr.TargetBranch, mr.Author.Display())
	if mr.DetailedStatus != "" {
		fmt.Fprintf(w, "  merge status: %s\n", mr.DetailedStatus)
	}
	if p := mr.Pipeline; p != nil {
		fmt.Fprintf(w, "  pipeline: %s (#%d)\n", p.Status, p.ID)
	} else {
		fmt.Fprintln(w, "  pipeline: none")
	}
	names := make([]string, len(mr.Approvals.ApprovedBy))
	for i, u := range mr.Approvals.ApprovedBy {
		names[i] = u.Display()
	}
	appr := "none"
	if len(names) > 0 {
		appr = strings.Join(names, ", ")
	}
	if mr.Approvals.Required >= 0 {
		appr += fmt.Sprintf(" (%d required)", mr.Approvals.Required)
	}
	fmt.Fprintf(w, "  approved by: %s\n", appr)
	fmt.Fprintf(w, "  threads: %d unresolved, %d resolved\n", mr.Unresolved, mr.Resolved)
	if mr.WebURL != "" {
		fmt.Fprintf(w, "  %s\n", mr.WebURL)
	}
	var open []forge.Discussion
	for _, d := range threads {
		if d.Resolvable && !d.Resolved {
			open = append(open, d)
		}
	}
	if len(open) == 0 {
		return
	}
	fmt.Fprintln(w, "\nunresolved:")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, d := range open {
		where := "(overview)"
		if p := d.Position; p != nil {
			where = fmt.Sprintf("%s:%d", p.Path, max(p.NewLine, p.OldLine))
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s: %s\n", d.ID, where, d.Notes[0].Author.Display(), oneLine(d.Notes[0].Body, 70))
	}
	tw.Flush()
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// ---- diff ------------------------------------------------------------------

func newMRDiffCmd() *cobra.Command {
	var nameOnly bool
	cmd := &cobra.Command{
		Use:   "diff <ref>",
		Short: "The MR's latest diff (renames resolved)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			d, err := c.be.MRDiff(cmd.Context(), c.ref)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			switch {
			case jsonOut:
				if nameOnly {
					for i := range d.Files {
						d.Files[i].Diff = ""
					}
				}
				d.Files = orEmpty(d.Files)
				return printJSON(w, d)
			case nameOnly:
				for _, f := range d.Files {
					fmt.Fprintln(w, f.NewPath)
				}
			default:
				printDiff(w, d)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&nameOnly, "name-only", false, "list changed file names only")
	return cmd
}

func printDiff(w io.Writer, d forge.Diff) {
	for _, f := range d.Files {
		fmt.Fprintf(w, "diff --ade a/%s b/%s\n", f.OldPath, f.NewPath)
		if f.Renamed {
			fmt.Fprintf(w, "rename from %s\nrename to %s\n", f.OldPath, f.NewPath)
		}
		from, to := "a/"+f.OldPath, "b/"+f.NewPath
		if f.NewFile {
			from = "/dev/null"
		}
		if f.Deleted {
			to = "/dev/null"
		}
		fmt.Fprintf(w, "--- %s\n+++ %s\n", from, to)
		fmt.Fprint(w, f.Diff)
		if f.Diff != "" && !strings.HasSuffix(f.Diff, "\n") {
			fmt.Fprintln(w)
		}
	}
}

// ---- reply / resolve -------------------------------------------------------

func newMRReplyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reply <ref> <discussion> <body>",
		Short: "Reply in a thread (body \"-\" reads stdin)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			body, err := bodyArg(cmd, args[2])
			if err != nil {
				return err
			}
			n, err := c.w.Reply(cmd.Context(), c.ref, args[1], body)
			if err != nil {
				return err
			}
			return out(cmd, action{Ref: c.ref, Action: "reply", DryRun: c.dry, Target: args[1], NoteID: n.ID},
				"%s in thread %s on %s", verb(c.dry, fmt.Sprintf("replied (note %d)", n.ID), "reply"), args[1], c.ref)
		},
	}
}

func newMRResolveCmd() *cobra.Command {
	var undo bool
	cmd := &cobra.Command{
		Use:   "resolve <ref> <discussion>",
		Short: "Resolve a thread (--undo reopens it)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			resolved := !undo
			if err := c.w.Resolve(cmd.Context(), c.ref, args[1], resolved); err != nil {
				return err
			}
			did, would := "resolved", "resolve"
			if undo {
				did, would = "reopened", "reopen"
			}
			return out(cmd, action{Ref: c.ref, Action: "resolve", DryRun: c.dry, Target: args[1], Resolved: &resolved},
				"%s thread %s on %s", verb(c.dry, did, would), args[1], c.ref)
		},
	}
	cmd.Flags().BoolVar(&undo, "undo", false, "reopen the thread instead")
	return cmd
}

// ---- approve / draft -------------------------------------------------------

func newMRApproveCmd(use, short string, approve bool) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <ref>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			var sha string
			if approve {
				mr, err := c.be.MR(cmd.Context(), c.ref)
				if err != nil {
					return err
				}
				sha = mr.HeadSHA // pin: a push since you looked answers 409
				err = c.w.Approve(cmd.Context(), c.ref, sha)
				if err != nil {
					return err
				}
			} else if err := c.w.Unapprove(cmd.Context(), c.ref); err != nil {
				return err
			}
			return out(cmd, action{Ref: c.ref, Action: use, DryRun: c.dry, SHA: sha},
				"%s %s", verb(c.dry, use+"d", use), c.ref)
		},
	}
}

func newMRDraftCmd(use, short string, draft bool) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <ref>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			if err := c.w.SetDraft(cmd.Context(), c.ref, draft); err != nil {
				return err
			}
			did := "marked ready"
			if draft {
				did = "marked draft"
			}
			return out(cmd, action{Ref: c.ref, Action: use, DryRun: c.dry}, "%s: %s", verb(c.dry, did, "mark "+use), c.ref)
		},
	}
}

// ---- merge / retry ---------------------------------------------------------

func newMRMergeCmd() *cobra.Command {
	var (
		o   forge.MergeOpts
		yes bool
	)
	cmd := &cobra.Command{
		Use:   "merge <ref> --yes",
		Short: "Merge the MR at its current head (refuses without --yes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dry, _ := cmd.Flags().GetBool("dry-run"); !yes && !dry {
				return errors.New("refusing to merge without --yes (merging cannot be undone)")
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
				return fmt.Errorf("%s is %s, not open", c.ref, mr.State)
			}
			o.SHA = mr.HeadSHA
			if st, err := appFrom(cmd).Cache(); err == nil {
				o = review.MergeOptsFor(o, review.PrefixForMR(appFrom(cmd), st, c.ref), mr)
			}
			if err := c.w.Merge(cmd.Context(), c.ref, o); err != nil {
				return err
			}
			did, would := "merged", "merge"
			if o.AutoMerge {
				did, would = "set to merge when checks pass:", "set to merge when checks pass:"
			}
			msg := fmt.Sprintf("%s %s at %.8s", verb(c.dry, did, would), c.ref, o.SHA)
			if c.dry && o.Squash && o.SquashMessage != "" {
				msg += "\nsquash message:\n" + indent(o.SquashMessage)
			}
			return out(cmd, action{Ref: c.ref, Action: "merge", DryRun: c.dry, SHA: o.SHA, Message: o.Message, SquashMessage: o.SquashMessage}, "%s", msg)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the merge")
	cmd.Flags().BoolVar(&o.Squash, "squash", false, "squash commits")
	cmd.Flags().BoolVar(&o.AutoMerge, "auto", false, "merge when the pipeline succeeds")
	cmd.Flags().BoolVar(&o.RemoveSourceBranch, "remove-source-branch", false, "delete the source branch after merging")
	return cmd
}

func newMRRetryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry <ref>",
		Short: "Retry the failed jobs of the MR's pipeline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			mr, err := c.be.MR(cmd.Context(), c.ref)
			if err != nil {
				return err
			}
			if mr.Pipeline == nil {
				_ = appFrom(cmd).Audit.Append(audit.Entry{
					Action: "forge.pipeline.retry", Item: c.ref.String(), Outcome: "skipped", Error: "no pipeline", DryRun: c.dry,
				})
				return out(cmd, action{Ref: c.ref, Action: "retry", DryRun: c.dry, Skipped: "no pipeline"}, "%s has no pipeline: nothing to retry", c.ref)
			}
			if err := c.w.RetryPipeline(cmd.Context(), c.ref.Project, mr.Pipeline.ID); err != nil {
				return err
			}
			return out(cmd, action{Ref: c.ref, Action: "retry", DryRun: c.dry, Target: fmt.Sprint(mr.Pipeline.ID)},
				"%s pipeline #%d of %s", verb(c.dry, "retried", "retry"), mr.Pipeline.ID, c.ref)
		},
	}
}
