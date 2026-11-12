package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/agile"
	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/budget"
	"github.com/coredev-uk/ade/internal/clipboard"
	"github.com/coredev-uk/ade/internal/notify"
)

func init() {
	register(newStandupCmd)
	register(newSprintCmd)
}

// exportFlags are the output controls shared by the agile commands.
type exportFlags struct {
	format string
	md     bool
	copy   bool
}

func (e *exportFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&e.format, "format", "text", "output format: text, md or json")
	cmd.Flags().BoolVar(&e.md, "md", false, "shorthand for --format md")
	cmd.Flags().BoolVar(&e.copy, "copy", false, "also copy the output to the clipboard (OSC 52, via the terminal)")
}

// resolve returns the format: --json wins, then --md, then --format.
func (e *exportFlags) resolve() (string, error) {
	switch {
	case jsonOut:
		return "json", nil
	case e.md:
		return "md", nil
	}
	switch e.format {
	case "text", "md", "json":
		return e.format, nil
	}
	return "", fmt.Errorf("unknown --format %q (text, md or json)", e.format)
}

// emit prints the report in the chosen format and copies it when asked. render
// returns the text for the text and md formats; v is what json encodes.
func (e *exportFlags) emit(cmd *cobra.Command, v any, render func(agile.Format) string) error {
	format, err := e.resolve()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	var body string
	switch format {
	case "json":
		if err := printJSON(out, v); err != nil {
			return err
		}
		if e.copy {
			var b bytes.Buffer
			if err := printJSON(&b, v); err != nil {
				return err
			}
			body = b.String()
		}
	case "md":
		body = render(agile.Markdown)
		fmt.Fprint(out, body)
	default:
		body = render(agile.Text)
		fmt.Fprint(out, body)
	}
	if e.copy {
		return copyToClipboard(body)
	}
	return nil
}

// copyToClipboard sends an OSC 52 request to the controlling terminal (never
// stdout, which may be piped).
func copyToClipboard(text string) error {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return errors.New("--copy needs a terminal to receive the clipboard request")
	}
	defer tty.Close()
	return clipboard.Copy(tty, text)
}

// narrateFlags are the options of the AI narrative (--ai).
type narrateFlags struct {
	on       bool
	profile  string
	refresh  bool
	yes      bool
	override bool
}

func (f *narrateFlags) bind(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.on, "ai", false, "add an AI narrative (cached, audited and capped; skipped with a warning when AI is off)")
	cmd.Flags().StringVar(&f.profile, "profile", "", "AI profile for --ai (default: ai.profile)")
	cmd.Flags().BoolVar(&f.refresh, "refresh", false, "with --ai: ignore the cached narrative and buy a new one")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "with --ai: go ahead without the pre-flight confirmation")
	cmd.Flags().BoolVar(&f.override, "override-budget", false, "with --ai: go over the local caps (audited as budget.override)")
}

// narrator builds the AI narrator; stop releases the notifier it attached.
func (f *narrateFlags) narrator(cmd *cobra.Command) (n *agile.AINarrator, stop func(), err error) {
	a := appFrom(cmd)
	svc, err := ai.NewService(a)
	if err != nil {
		return nil, nil, err
	}
	svc.SprintStart = budget.SprintStart(a)
	stop, err = notify.Attach(cmd.Context(), a, notify.Channels{})
	if err != nil {
		return nil, nil, err
	}
	confirm := confirmPrompt(cmd)
	return &agile.AINarrator{Svc: svc, Tune: func(j *ai.Job) {
		j.Profile, j.Refresh, j.Override, j.Assume, j.Confirm = f.profile, f.refresh, f.override, f.yes, confirm
	}}, stop, nil
}

// report prints the cost line of a narrative, or says why there is none. The
// report itself is still produced without it.
func narrateReport(cmd *cobra.Command, n *agile.AINarrator, feature string, err error) []string {
	w := cmd.ErrOrStderr()
	if err != nil {
		err = aiError(err)
		fmt.Fprintf(w, "warning: AI narrative skipped: %v\n", err)
		return []string{"AI narrative skipped: " + err.Error()}
	}
	r := n.Last
	printFooter(w, aiOut{Feature: feature, Text: r.Text, CacheHit: r.CacheHit, CostUSD: r.CostUSD, SavedUSD: r.SavedUSD, Profile: r.Profile, Model: r.Model, Artifact: r.Artifact, Truncated: r.Truncated})
	return nil
}

func newStandupCmd() *cobra.Command {
	var (
		ex   exportFlags
		nf   narrateFlags
		date string
	)
	cmd := &cobra.Command{
		Use:   "standup",
		Short: "What changed since the previous working day, what is yours today, what blocks you",
		Long: "Builds the standup from the sync cache, the forge activity feed and the audit log.\n" +
			"Yesterday is the previous working day (weekends and user.holidays skipped; its\n" +
			"window runs to this morning, so weekend work is included). Run `ade sync` first\n" +
			"for fresh items.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := agile.NewCollector(appFrom(cmd))
			var day time.Time
			if date != "" {
				cal, err := agile.NewCalendar(appFrom(cmd).Config.User)
				if err != nil {
					return err
				}
				if day, err = time.ParseInLocation("2006-01-02", date, cal.Loc); err != nil {
					return fmt.Errorf("--date %q: want YYYY-MM-DD", date)
				}
			}
			s, err := c.Standup(cmd.Context(), day)
			if err != nil {
				return err
			}
			if nf.on {
				n, stop, err := nf.narrator(cmd)
				if err != nil {
					return err
				}
				defer stop()
				var nerr error
				if s, nerr = agile.WithNarrative(cmd.Context(), n, s); nerr != nil || s.Narrative == "" {
					s.Warnings = append(s.Warnings, narrateReport(cmd, n, "standup", nerr)...)
				} else {
					narrateReport(cmd, n, "standup", nil)
				}
			}
			return ex.emit(cmd, s, func(f agile.Format) string { return agile.RenderStandup(s, f) })
		},
	}
	ex.bind(cmd)
	nf.bind(cmd)
	cmd.Flags().StringVar(&date, "date", "", "standup for this day, YYYY-MM-DD (default today)")
	return cmd
}

func newSprintCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "sprint", Short: "Sprint review and velocity"}
	cmd.AddCommand(newSprintReviewCmd(), newSprintVelocityCmd())
	return cmd
}

type sprintFlags struct {
	project  string
	sprint   string
	velocity int
	mine     bool
}

func (s *sprintFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&s.project, "project", "", "ADE project (default: the only one, else the first with sprints)")
	cmd.Flags().StringVar(&s.sprint, "sprint", "", "sprint title or key (default: the current sprint)")
	cmd.Flags().IntVarP(&s.velocity, "last", "n", agile.DefaultVelocitySprints, "closed sprints in the velocity")
	cmd.Flags().BoolVar(&s.mine, "mine", false, "only issues assigned to you")
}

func (s *sprintFlags) options() agile.ReviewOptions {
	return agile.ReviewOptions{Project: s.project, Sprint: s.sprint, Velocity: s.velocity, Mine: s.mine}
}

func newSprintReviewCmd() *cobra.Command {
	var (
		ex      exportFlags
		sf      sprintFlags
		nf      narrateFlags
		parents bool
	)
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Committed, done and carried work of a sprint, by count and weight",
		Long: "Reads the sprint's issues and their label and sprint history from the forge. Done means\n" +
			"closed, or kept open with the manifest's labels.workflow.done label. Weight totals count\n" +
			"only weighted issues; unweighted ones are counted separately and never add zero. Cycle time is\n" +
			"assigned → merged per item (from the forge's assignment note, else the issue's creation\n" +
			"time, marked as such), with the median in the summary; an item without a merged MR has none.\n" +
			"--ai-parents adds a short AI paragraph under every parent with merged MRs in the Markdown\n" +
			"export (--md); each is cached by content, audited and capped like any AI call.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := agile.NewCollector(appFrom(cmd)).Review(cmd.Context(), sf.options())
			if err != nil {
				return err
			}
			if nf.on || parents {
				n, stop, err := nf.narrator(cmd)
				if err != nil {
					return err
				}
				defer stop()
				if nf.on {
					var nerr error
					if r, nerr = agile.WithReviewNarrative(cmd.Context(), n, r); nerr != nil {
						r.Warnings = append(r.Warnings, narrateReport(cmd, n, "sprint-review", nerr)...)
					} else {
						narrateReport(cmd, n, "sprint-review", nil)
					}
				}
				if parents {
					var (
						count int
						nerr  error
					)
					if r, count, nerr = agile.WithParentNarratives(cmd.Context(), n, r); nerr != nil {
						r.Warnings = append(r.Warnings, narrateReport(cmd, n, "sprint-parent", fmt.Errorf("after %d of the parents: %w", count, nerr))...)
					} else {
						fmt.Fprintf(cmd.ErrOrStderr(), "narrated %s\n", plural(count, "parent"))
					}
				}
			}
			return ex.emit(cmd, r, func(f agile.Format) string { return agile.RenderReview(r, f) })
		},
	}
	ex.bind(cmd)
	sf.bind(cmd)
	nf.bind(cmd)
	cmd.Flags().BoolVar(&parents, "ai-parents", false, "add an AI paragraph per parent with merged MRs (shown in the Markdown export; JSON carries it as narrative)")
	return cmd
}

func newSprintVelocityCmd() *cobra.Command {
	var (
		ex exportFlags
		sf sprintFlags
	)
	cmd := &cobra.Command{
		Use:   "velocity",
		Short: "Throughput over the last closed sprints, in weight (items when nothing is weighted)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := agile.NewCollector(appFrom(cmd)).Review(cmd.Context(), sf.options())
			if err != nil {
				return err
			}
			for _, w := range r.Warnings { // the project chosen without --project
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			return ex.emit(cmd, r.Velocity, func(f agile.Format) string { return agile.RenderVelocity(r.Velocity, f) })
		},
	}
	ex.bind(cmd)
	sf.bind(cmd)
	return cmd
}
