package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/forge"
)

// --- digest ------------------------------------------------------------------

// digestOut is the JSON shape of `ade ai digest`.
type digestOut struct {
	aiOut
	NewComments int        `json:"new_comments"`
	Omitted     int        `json:"omitted,omitempty"`
	Since       *time.Time `json:"since,omitempty"` // the marker the digest started from; absent on a first look
	Until       *time.Time `json:"until,omitempty"` // the newest comment of the item
	Source      string     `json:"source,omitempty"`
	// Marked: the last-seen marker moved to Until.
	Marked bool `json:"marked"`
}

func newAIDigestCmd() *cobra.Command {
	var (
		o     aiOpts
		since string
		peek  bool
		noAI  bool
	)
	cmd := &cobra.Command{
		Use:   "digest <ref>",
		Short: "Digest of the comments that are new on an MR or issue since you last looked",
		Long: "Summarises only the comments that arrived after the item's last-seen marker (kept in its\n" +
			"record, on this machine), then moves the marker to the newest comment, so the next digest\n" +
			"starts where this one ended. Your own comments and system notes (label changes) are not\n" +
			"news and are left out. The first digest of an item covers every comment. With nothing new\n" +
			"no call is made and nothing is spent.\n\n" +
			"The marker only moves when the digest was delivered: a refused or failed call, a declined\n" +
			"confirmation, --peek and --dry-run leave the same comments new. --since looks back over a\n" +
			"window instead (30m, 12h, 2d, 1w or an RFC3339 time): it never moves the marker backwards. With ai.mode =\n" +
			"\"off\", no profile, or --no-ai the new comments are listed instead of summarised.\n" +
			"The answer is cached by the new comments' text, so asking twice costs $0.",
		Example: "  ade ai digest acme/api!2\n  ade ai digest acme/tracker#15 --peek\n  ade ai digest acme/api!2 --since 2d --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := itemRef(cmd, args[0])
			if err != nil {
				return err
			}
			now := time.Now()
			var from time.Time
			if since != "" {
				if from, err = parseSince(since, now); err != nil {
					return err
				}
			}
			e, err := openAI(cmd, ref, o.forge)
			if err != nil {
				return err
			}
			defer e.stop()
			in, err := ai.LoadDigestInput(cmd.Context(), e.be, ref)
			if err != nil {
				return err
			}
			res, err := e.svc.Digest(cmd.Context(), e.forgeName, in, ai.DigestOpts{
				Profile: o.profile, Refresh: o.refresh, Override: o.override, Assume: o.yes,
				Confirm: confirmPrompt(cmd), NoAI: noAI, Since: from, Peek: peek, DryRun: o.dryRun,
			})
			if err != nil {
				return aiError(err)
			}
			out := digestOutOf(res)
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), out)
			}
			printDigest(cmd.OutOrStdout(), res, in.Title, peek || o.dryRun, out.aiOut)
			return nil
		},
	}
	o.bind(cmd)
	cmd.Flags().StringVar(&since, "since", "", "look back over this window instead of from the marker: 30m, 12h, 2d, 1w or an RFC3339 time")
	cmd.Flags().BoolVar(&peek, "peek", false, "do not move the last-seen marker: the same comments stay new")
	cmd.Flags().BoolVar(&noAI, "no-ai", false, "list the new comments instead of summarising them: no model, no cost")
	return cmd
}

// digestOutOf is the JSON shape of a digest, and the footer's numbers.
func digestOutOf(res ai.DigestResult) digestOut {
	out := digestOut{
		aiOut:       aiOut{Feature: "digest", Ref: res.Ref.String(), Text: res.Text, Preflight: res.Preflight, DryRun: res.Preflight != nil, Cached: res.Cached},
		NewComments: res.Count, Omitted: res.Omitted, Source: res.Source, Marked: res.Marked,
	}
	if !res.Since.IsZero() {
		t := res.Since
		out.Since = &t
	}
	if !res.Until.IsZero() {
		t := res.Until
		out.Until = &t
	}
	if r := res.Run; r != nil {
		out.CacheHit, out.CostUSD, out.SavedUSD = r.CacheHit, r.CostUSD, r.SavedUSD
		out.Profile, out.Model, out.Artifact, out.Truncated = r.Profile, r.Model, r.Artifact, r.Truncated
		if r.Preflight.Model != "" {
			out.Preflight = &r.Preflight
		}
	}
	return out
}

func printDigest(w io.Writer, res ai.DigestResult, title string, kept bool, o aiOut) {
	since := "the beginning (the first digest of this item)"
	if !res.Since.IsZero() {
		since = res.Since.Local().Format("2 Jan 15:04")
	}
	switch res.Count {
	case 0:
		fmt.Fprintf(w, "%s %s\nno new comments since %s: nothing was sent\n", res.Ref, title, since)
	default:
		fmt.Fprintf(w, "%s %s\n%s since %s\n\n", res.Ref, title, countWordCLI(res.Count, "new comment"), since)
		if res.Text != "" {
			fmt.Fprintln(w, res.Text)
			fmt.Fprintln(w)
		}
		if res.Source == ai.DigestSourceList {
			fmt.Fprintln(w, "— the new comments listed as they are: no model was asked (ai.mode off, no profile, or --no-ai)")
		} else {
			printFooter(w, o)
		}
	}
	switch {
	case kept && res.Preflight == nil:
		fmt.Fprintln(w, "last-seen marker unchanged (--peek)")
	case res.Preflight != nil:
		fmt.Fprintln(w, "last-seen marker unchanged (dry run)")
	case res.Marked:
		fmt.Fprintf(w, "marked seen up to %s\n", res.Until.Local().Format("2 Jan 15:04"))
	case res.Count > 0 && res.Source == ai.DigestSourceAI && res.Run != nil && res.Run.Truncated:
		fmt.Fprintln(w, "last-seen marker unchanged: the answer was cut short, so the same comments stay new")
	}
}

func countWordCLI(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// --- stand-in ----------------------------------------------------------------

// standInOut is the JSON shape of `ade ai standin`.
type standInOut struct {
	aiOut
	// Needed is false when the issue has a description of its own.
	Needed bool `json:"needed"`
	// LocalOnly is always true: the stand-in is never sent to the forge.
	LocalOnly bool `json:"local_only"`
}

func newAIStandInCmd() *cobra.Command {
	var o aiOpts
	cmd := &cobra.Command{
		Use:   "standin <issue-ref>",
		Short: "A stand-in description for an issue that has none (shown here only; never posted to the forge)",
		Long: "Writes a short stand-in from the issue's title, labels and the issues it is linked to, for\n" +
			"an issue whose description is empty. It is marked as a stand-in wherever it is shown and\n" +
			"is kept only in the item's record: nothing is ever written to the issue. An issue that\n" +
			"has a description needs none (no call is made). The answer is cached by its input, so\n" +
			"asking again about an unchanged issue costs $0. It needs AI: with ai.mode = \"off\" the\n" +
			"command refuses.",
		Example: "  ade ai standin acme/api#12\n  ade ai standin acme/api#12 --dry-run",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := itemRef(cmd, args[0])
			if err != nil {
				return err
			}
			if ref.Kind != forge.KindIssue {
				return fmt.Errorf("%s is a merge request: want an issue like group/project#3", args[0])
			}
			e, err := openAI(cmd, ref, o.forge)
			if err != nil {
				return err
			}
			defer e.stop()
			in, err := ai.LoadStandInInput(cmd.Context(), e.be, ref)
			if err != nil {
				return err
			}
			job, err := ai.StandInJob(e.forgeName, in)
			w := cmd.OutOrStdout()
			if errors.Is(err, ai.ErrHasDescription) {
				if jsonOut {
					return printJSON(w, standInOut{aiOut: aiOut{Feature: "standin", Ref: ref.String()}, Needed: false, LocalOnly: true})
				}
				fmt.Fprintf(w, "%s has a description: no stand-in needed (nothing was sent)\n", ref)
				return nil
			}
			if err != nil {
				return err
			}
			_, out, err := e.runJob(cmd, job, o)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(w, standInOut{aiOut: out, Needed: true, LocalOnly: true})
			}
			if out.Text != "" {
				fmt.Fprintf(w, "%s %s\n%s — %s\n\n", ref, in.Issue.Title, ai.StandInLabel, ai.StandInNote)
			}
			return emit(cmd, out)
		},
	}
	o.bind(cmd)
	return cmd
}
