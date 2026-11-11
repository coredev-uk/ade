package cli

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

func init() { register(newReviewCmd) }

func newReviewCmd() *cobra.Command {
	var (
		noZellij bool
		session  string
	)
	cmd := &cobra.Command{
		Use:   "review <ref> | review add|list|rm|submit",
		Short: "Open an MR in a review workspace tab; collect line comments and submit them as one batch",
		Long: "`ade review <ref>` checks out the MR's source branch as a worktree (made when it\n" +
			"does not exist) and opens it in the `review` layout: the diff against the target\n" +
			"branch through delta, lazygit and a shell, in a tab of the ade zellij session.\n" +
			"--no-zellij opens no tab: it prints the worktree and the command that shows the diff.\n\n" +
			"Comments collect as local drafts (kept across runs) and nothing is posted until\n" +
			"`ade review submit`, which sends them as one GitLab draft-note batch: one\n" +
			"notification for the author. <ref> is group/project!N.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return runReviewOpen(cmd, args[0], noZellij, session)
		},
	}
	mrFlags(cmd)
	cmd.Flags().BoolVar(&noZellij, "no-zellij", false, "open no tab: print the worktree and the command that shows the diff")
	cmd.Flags().StringVar(&session, "session", "", "zellij session to open the tab in (default: ade; for tests)")
	_ = cmd.Flags().MarkHidden("session")
	cmd.AddCommand(newReviewAddCmd(), newReviewListCmd(), newReviewRemoveCmd(), newReviewSubmitCmd())
	return cmd
}

func reviewStore(cmd *cobra.Command) (*review.Store, error) {
	st, err := appFrom(cmd).Cache()
	if err != nil {
		return nil, err
	}
	return review.NewStore(st), nil
}

func newReviewAddCmd() *cobra.Command {
	var (
		d   review.Draft
		old bool
	)
	cmd := &cobra.Command{
		Use:   "add <ref> --path p --line n [--old] --body b [--severity s]",
		Short: "Add a line comment to the pending review (nothing is posted)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			if d.Body, err = bodyArg(cmd, d.Body); err != nil {
				return err
			}
			if old {
				d.Side = review.SideOld
			}
			st, err := reviewStore(cmd)
			if err != nil {
				return err
			}
			sess, err := review.Open(cmd.Context(), c.be, st, c.ref)
			if err != nil {
				return err
			}
			if c.dry { // validate against the diff, store nothing
				nd, err := review.Normalize(d, time.Now())
				if err != nil {
					return err
				}
				if _, err := review.PositionFor(sess.Diff, nd); err != nil {
					return err
				}
				return out(cmd, nd, "dry-run: would add a comment on %s:%d (%s)", nd.Path, nd.Line, nd.Side)
			}
			added, err := sess.Add(d)
			if err != nil {
				return err
			}
			return out(cmd, added, "added draft %s on %s:%d (%s); %d pending on %s", added.ID, added.Path, added.Line, added.Side, len(sess.Drafts), c.ref)
		},
	}
	f := cmd.Flags()
	f.StringVar(&d.Path, "path", "", "file path (its new path, or old path for a removed line)")
	f.IntVar(&d.Line, "line", 0, "line number in the new file (in the old file with --old)")
	f.BoolVar(&old, "old", false, "the line number counts in the old version of the file")
	f.StringVar(&d.Body, "body", "", "comment text (\"-\" reads stdin)")
	f.Var((*severityFlag)(&d.Severity), "severity", "blocking | suggestion | question | nit")
	_ = cmd.MarkFlagRequired("path")
	_ = cmd.MarkFlagRequired("line")
	_ = cmd.MarkFlagRequired("body")
	return cmd
}

// severityFlag validates --severity at parse time.
type severityFlag review.Severity

func (s *severityFlag) String() string { return string(*s) }
func (s *severityFlag) Type() string   { return "severity" }
func (s *severityFlag) Set(v string) error {
	switch sev := review.Severity(v); sev {
	case review.SeverityBlocking, review.SeveritySuggestion, review.SeverityQuestion, review.SeverityNit:
		*s = severityFlag(sev)
		return nil
	}
	return fmt.Errorf("want blocking, suggestion, question or nit")
}

func newReviewListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <ref>",
		Short: "Show the pending review's drafts (no network)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseMRRef(args[0])
			if err != nil {
				return err
			}
			st, err := reviewStore(cmd)
			if err != nil {
				return err
			}
			ds, err := st.List(ref)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), orEmpty(ds))
			}
			printDrafts(cmd.OutOrStdout(), ref, ds)
			return nil
		},
	}
}

func printDrafts(w io.Writer, ref forge.Ref, ds []review.Draft) {
	if len(ds) == 0 {
		fmt.Fprintf(w, "no pending review on %s\n", ref)
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, d := range ds {
		where := "(overview)"
		if !d.Overview() {
			where = fmt.Sprintf("%s:%d %s", d.Path, d.Line, d.Side)
		}
		status := string(d.Source) + "/" + string(d.State)
		switch {
		case d.Published:
			status += " published"
		case d.RemoteID != 0:
			status += " uploaded"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", d.ID, where, d.Severity, status, oneLine(d.Body, 60))
	}
	tw.Flush()
}

func newReviewRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <ref> <draft-id>",
		Short: "Drop one pending draft",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseMRRef(args[0])
			if err != nil {
				return err
			}
			st, err := reviewStore(cmd)
			if err != nil {
				return err
			}
			ds, err := st.List(ref)
			if err != nil {
				return err
			}
			found := false
			for _, d := range ds {
				found = found || d.ID == args[1]
			}
			if !found {
				return fmt.Errorf("no draft %q on %s", args[1], ref)
			}
			if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
				return out(cmd, action{Ref: ref, Action: "review.rm", DryRun: true, Target: args[1]}, "dry-run: would drop draft %s", args[1])
			}
			if err := st.Delete(ref, args[1]); err != nil {
				return err
			}
			return out(cmd, action{Ref: ref, Action: "review.rm", Target: args[1]}, "dropped draft %s", args[1])
		},
	}
}

func newReviewSubmitCmd() *cobra.Command {
	var (
		approve bool
		summary string
	)
	cmd := &cobra.Command{
		Use:   "submit <ref> [--approve] [--summary s]",
		Short: "Send the pending review as one batch (one notification)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			if summary, err = bodyArg(cmd, summary); err != nil {
				return err
			}
			st, err := reviewStore(cmd)
			if err != nil {
				return err
			}
			sess, err := review.Open(cmd.Context(), c.be, st, c.ref)
			if err != nil {
				return err
			}
			if !approve && summary == "" && !anyToSend(sess.Drafts) {
				return errors.New("nothing to submit: add comments with `ade review add`, or pass --summary / --approve")
			}
			a := appFrom(cmd)
			opts := []review.Option{review.WithAudit(a.Audit), review.WithBus(a.Bus)}
			if c.dry {
				opts = append(opts, review.WithDryRun())
			}
			res, err := sess.Submit(cmd.Context(), c.w, summary, approve, opts...)
			if err != nil {
				return fmt.Errorf("%w (progress is saved: run the same command again to resume)", err)
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), res)
			}
			printSubmit(cmd.OutOrStdout(), c.ref, res)
			return nil
		},
	}
	cmd.Flags().BoolVar(&approve, "approve", false, "also approve the MR at the reviewed head")
	cmd.Flags().StringVar(&summary, "summary", "", "overall comment sent with the review (\"-\" reads stdin)")
	return cmd
}

func anyToSend(ds []review.Draft) bool {
	for _, d := range ds {
		if d.Selected() && !d.Published {
			return true
		}
	}
	return false
}

func printSubmit(w io.Writer, ref forge.Ref, r review.Result) {
	prefix := ""
	if r.DryRun {
		prefix = "dry-run: would have "
	}
	fmt.Fprintf(w, "%ssubmitted %s on %s in one batch (%d uploaded, %d resumed)\n", prefix, plural(r.Published, "comment"), ref, r.Uploaded, r.Resumed)
	if r.Approved {
		fmt.Fprintf(w, "%sapproved %s\n", prefix, ref)
	}
	if r.Foreign > 0 {
		fmt.Fprintf(w, "note: %s of yours started elsewhere went out in the same batch\n", plural(r.Foreign, "draft note"))
	}
}
