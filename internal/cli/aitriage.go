package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// longDraft is the length past which a draft is flagged `long` in the triage
// list, so it gets trimmed before posting (docs/PLAN.md §6.9).
const longDraft = 280

func newAITriageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "triage <mr-ref>",
		Short: "List an MR's pending drafts; accept, edit or deny the agent's (every decision is audited)",
		Long: "Agent drafts start pending and are never posted until accepted. Denied drafts are\n" +
			"never posted. Accepted and edited drafts go out together with `ade review submit`.",
		Args: cobra.ExactArgs(1),
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
				return printJSON(cmd.OutOrStdout(), emptyIfNil(ds))
			}
			printTriage(cmd.OutOrStdout(), ref, ds)
			return nil
		},
	}
	cmd.AddCommand(newTriageAcceptCmd(), newTriageDenyCmd(), newTriageEditCmd())
	return cmd
}

func printTriage(w io.Writer, ref forge.Ref, ds []review.Draft) {
	if len(ds) == 0 {
		fmt.Fprintf(w, "%s: no drafts\n", ref)
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, d := range ds {
		flags := ""
		if len(d.Body) > longDraft {
			flags = " long"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s:%d\t%s%s\n", d.ID, d.Source, d.State, nonEmpty(string(d.Severity), "-"), d.Path, d.Line, firstLine(d.Body), flags)
	}
	tw.Flush()
}

// triageTarget finds the drafts a command names, by id or unique id prefix.
// Only agent drafts are triaged here; your own are accepted as written.
func triageTarget(ds []review.Draft, ids []string, all bool) ([]review.Draft, error) {
	var out []review.Draft
	if all {
		for _, d := range ds {
			if d.Source == review.SourceAgent && d.State == review.StatePending {
				out = append(out, d)
			}
		}
		return out, nil
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("name at least one draft id (see `ade ai triage <mr-ref>`), or use --all")
	}
	for _, id := range ids {
		var hits []review.Draft
		for _, d := range ds {
			if d.ID == id {
				hits = []review.Draft{d}
				break
			}
			if strings.HasPrefix(d.ID, id) {
				hits = append(hits, d)
			}
		}
		switch {
		case len(hits) == 0:
			return nil, fmt.Errorf("no draft %q on this MR", id)
		case len(hits) > 1:
			return nil, fmt.Errorf("%q matches %d drafts: give more of the id", id, len(hits))
		case hits[0].Source != review.SourceAgent:
			return nil, fmt.Errorf("draft %s is your own: it is accepted as written (remove it with `ade review remove`)", hits[0].ID)
		}
		out = append(out, hits[0])
	}
	return out, nil
}

func newTriageAcceptCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "accept <mr-ref> [<draft-id>…]",
		Short: "Accept agent drafts so `ade review submit` sends them (--all: every pending one)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return decide(cmd, args, all, review.StateAccepted, ai.Accept, "")
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "accept every pending agent draft")
	return cmd
}

func newTriageDenyCmd() *cobra.Command {
	var (
		all    bool
		reason string
	)
	cmd := &cobra.Command{
		Use:   "deny <mr-ref> [<draft-id>…]",
		Short: "Deny agent drafts: they are never posted (--reason: one word, audited)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return decide(cmd, args, all, review.StateDenied, ai.Deny, reason)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "deny every pending agent draft")
	cmd.Flags().StringVar(&reason, "reason", "", "one word saying why (noisy, wrong, nit…)")
	return cmd
}

func decide(cmd *cobra.Command, args []string, all bool, state review.State, d ai.Decision, reason string) error {
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
	targets, err := triageTarget(ds, args[1:], all)
	if err != nil {
		return err
	}
	for _, t := range targets {
		t.State = state
		if err := st.Put(ref, t); err != nil {
			return err
		}
		if err := ai.AuditDecision(appFrom(cmd).Audit, ref, t.ID, d, reason); err != nil {
			return fmt.Errorf("audit: %w", err)
		}
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), map[string]any{"ref": ref.String(), "decision": string(d), "drafts": ids(targets)})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %s\n", ref, verbPast(d), plural(len(targets), "draft"))
	return nil
}

func verbPast(d ai.Decision) string {
	switch d {
	case ai.Accept:
		return "accepted"
	case ai.Edit:
		return "edited and accepted"
	}
	return "denied"
}

func ids(ds []review.Draft) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.ID
	}
	return out
}

func newTriageEditCmd() *cobra.Command {
	var (
		body string
		sev  severityFlag
	)
	cmd := &cobra.Command{
		Use:   "edit <mr-ref> <draft-id> --body <text|->  [--severity s]",
		Short: "Edit an agent draft and accept it: the edited text is what gets posted",
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
			targets, err := triageTarget(ds, args[1:], false)
			if err != nil {
				return err
			}
			t := targets[0]
			if t.RemoteID != 0 {
				return fmt.Errorf("draft %s was already uploaded for submit: deny it, then add your own comment", t.ID)
			}
			if body != "" || cmd.Flags().Changed("body") {
				if t.Body, err = bodyArg(cmd, body); err != nil {
					return err
				}
			}
			if cmd.Flags().Changed("severity") {
				t.Severity = review.Severity(sev)
			}
			t.State = review.StateAccepted
			if t, err = review.Normalize(t, time.Now()); err != nil {
				return err
			}
			if err := st.Put(ref, t); err != nil {
				return err
			}
			if err := ai.AuditDecision(appFrom(cmd).Audit, ref, t.ID, ai.Edit, ""); err != nil {
				return fmt.Errorf("audit: %w", err)
			}
			return out(cmd, t, "%s: edited and accepted draft %s", ref, t.ID)
		},
	}
	cmd.Flags().StringVar(&body, "body", "", "the new comment text; - reads stdin")
	cmd.Flags().Var(&sev, "severity", "blocking | suggestion | question | nit")
	return cmd
}
