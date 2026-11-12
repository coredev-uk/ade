package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/budget"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/review"
	"github.com/coredev-uk/ade/internal/startwork"
)

func init() { register(newAICmd) }

// Exit codes of the ai commands, so scripts can tell a budget stop from a
// failure.
const (
	exitBudgetStop   = 4
	exitNeedsConfirm = 5
)

// aiOpts are the flags every AI call shares.
type aiOpts struct {
	profile  string
	forge    string
	refresh  bool
	yes      bool
	override bool
	dryRun   bool
	noNotes  bool
}

func (o *aiOpts) bind(c *cobra.Command) {
	c.Flags().StringVar(&o.profile, "profile", "", "AI profile to use (default: ai.profile)")
	c.Flags().StringVar(&o.forge, "forge", "", "forge (from the user config) serving the ref; default: found from registered projects")
	c.Flags().BoolVar(&o.refresh, "refresh", false, "ignore the cached answer and buy a new one")
	c.Flags().BoolVarP(&o.yes, "yes", "y", false, "go ahead without the pre-flight confirmation")
	c.Flags().BoolVar(&o.override, "override-budget", false, "go over the local daily/weekly/sprint caps (audited as budget.override)")
	c.Flags().BoolVar(&o.dryRun, "dry-run", false, "show the pre-flight estimate and whether the answer is cached; send nothing")
}

func newAICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai",
		Short: "AI helpers: summarize, classify, draft-reply, review (every call is audited and capped)",
		Long: "Each command prints a pre-flight estimate before it spends anything. Results are cached by\n" +
			"content hash, so an unchanged MR is never summarised twice (cache hits cost $0 and are\n" +
			"audited). Budget caps live under [budget] in the config; a breach stops the call (exit 4).\n" +
			"With ai.mode = \"off\" these commands refuse; nothing else changes.",
	}
	cmd.AddCommand(newAISummarizeCmd(), newAIClassifyCmd(), newAIReplyCmd(), newAIReviewCmd(), newAITriageCmd(), newAIModelsCmd())
	return cmd
}

// aiRun is a resolved forge, the service, and the MR/issue under work.
type aiEnv struct {
	a         *app.App
	svc       *ai.Service
	be        review.Backend
	forgeName string
	stop      func()
}

func openAI(cmd *cobra.Command, ref forge.Ref, forgeFlag string) (*aiEnv, error) {
	a := appFrom(cmd)
	svc, err := ai.NewService(a)
	if err != nil {
		return nil, err
	}
	svc.SprintStart = budget.SprintStart(a)
	name, err := review.ForgeFor(cmd.Context(), a, ref, forgeFlag)
	if err != nil {
		return nil, err
	}
	be, err := review.Dial(cmd.Context(), name, a.Config.Forges[name])
	if err != nil {
		return nil, err
	}
	stop, err := notify.Attach(cmd.Context(), a, notify.Channels{})
	if err != nil {
		return nil, err
	}
	return &aiEnv{a: a, svc: svc, be: be, forgeName: name, stop: stop}, nil
}

// confirmPrompt asks on stderr when stdin is a terminal; otherwise nil, so
// the service reports that confirmation is needed instead of hanging.
func confirmPrompt(cmd *cobra.Command) func(ai.Preflight) (bool, error) {
	if f, ok := cmd.InOrStdin().(*os.File); !ok || !isTerminal(f) {
		return nil
	}
	return func(pf ai.Preflight) (bool, error) {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", pf.Describe())
		for _, c := range pf.Confirm {
			fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", c)
		}
		fmt.Fprint(cmd.ErrOrStderr(), "Send it? [y/N] ")
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		a := strings.ToLower(strings.TrimSpace(line))
		return a == "y" || a == "yes", nil
	}
}

// aiOut is the JSON shape of a finished AI command.
type aiOut struct {
	Feature   string        `json:"feature"`
	Ref       string        `json:"ref"`
	Text      string        `json:"text,omitempty"`
	CacheHit  bool          `json:"cache_hit"`
	CostUSD   float64       `json:"cost_usd"`
	SavedUSD  float64       `json:"saved_usd,omitempty"`
	Profile   string        `json:"profile,omitempty"`
	Model     string        `json:"model,omitempty"`
	Artifact  string        `json:"artifact,omitempty"`
	Preflight *ai.Preflight `json:"preflight,omitempty"`
	DryRun    bool          `json:"dry_run,omitempty"`
	Cached    bool          `json:"cached,omitempty"` // dry-run: an answer is already cached
	// IncludesNotes: the prompt carries the user's notes (--no-notes leaves them out).
	IncludesNotes bool `json:"includes_notes,omitempty"`
	// Truncated: the model reached its output limit, so Text is cut short.
	Truncated bool `json:"truncated,omitempty"`
}

// runJob executes (or, with --dry-run, previews) a job and maps budget stops
// and confirmation needs to distinct exit codes.
func (e *aiEnv) runJob(cmd *cobra.Command, j ai.Job, o aiOpts) (ai.Result, aiOut, error) {
	j.Profile, j.Refresh, j.Override, j.Assume = o.profile, o.refresh, o.override, o.yes
	j.Confirm = confirmPrompt(cmd)
	out := aiOut{Feature: j.Feature, Ref: j.Ref.String(), IncludesNotes: j.Notes}
	if o.dryRun {
		pf, cached, err := e.svc.Preview(cmd.Context(), j)
		if err != nil {
			return ai.Result{}, out, aiError(err)
		}
		out.DryRun, out.Cached, out.Preflight = true, cached, &pf
		return ai.Result{}, out, nil
	}
	res, err := e.svc.Run(cmd.Context(), j)
	if err != nil {
		return res, out, aiError(err)
	}
	out.Text, out.CacheHit, out.CostUSD, out.SavedUSD = res.Text, res.CacheHit, res.CostUSD, res.SavedUSD
	out.Profile, out.Model, out.Artifact, out.Truncated = res.Profile, res.Model, res.Artifact, res.Truncated
	if res.Preflight.Model != "" {
		out.Preflight = &res.Preflight
	}
	return res, out, nil
}

func aiError(err error) error {
	var nc *ai.ErrNeedsConfirm
	if errors.As(err, &nc) {
		return withCode(exitNeedsConfirm, fmt.Errorf("%s\n  %s", nc, nc.Preflight.Describe()))
	}
	if _, ok := ai.AsStop(err); ok {
		return withCode(exitBudgetStop, err)
	}
	return err
}

// printFooter writes the one-line cost summary under an AI answer.
func printFooter(w io.Writer, o aiOut) {
	switch {
	case o.DryRun:
		state := "not cached: a call would be made"
		if o.Cached {
			state = "cached: $0, no call"
		}
		pf := o.Preflight
		fmt.Fprintf(w, "dry run · %s · %s\n", state, pf.Describe())
		for _, c := range pf.Confirm {
			fmt.Fprintf(w, "  would ask first: %s\n", c)
		}
		if pf.Refuse != "" {
			fmt.Fprintf(w, "  would be refused: %s\n", pf.Refuse)
		}
	case o.CacheHit:
		fmt.Fprintf(w, "— cached · $0.00 (saved $%.4f) · %s · %s%s\n", o.SavedUSD, o.Model, o.Artifact, notesMark(o)+truncMark(o))
	default:
		fmt.Fprintf(w, "— $%.4f · %s · %s · %s%s\n", o.CostUSD, o.Profile, o.Model, o.Artifact, notesMark(o)+truncMark(o))
	}
}

func emit(cmd *cobra.Command, o aiOut) error {
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), o)
	}
	w := cmd.OutOrStdout()
	if o.Text != "" {
		fmt.Fprintln(w, o.Text)
		fmt.Fprintln(w)
	}
	printFooter(w, o)
	return nil
}

// --- summarize ---------------------------------------------------------------

func newAISummarizeCmd() *cobra.Command {
	var o aiOpts
	cmd := &cobra.Command{
		Use:   "summarize <mr-ref>",
		Short: "Summarise a merge request (cached by head SHA: an unchanged MR costs $0 the second time)",
		Args:  cobra.ExactArgs(1),
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
			mr, diff, issue, err := e.loadMR(cmd.Context(), ref)
			if err != nil {
				return err
			}
			sj, err := e.withNotes(ai.SummarizeJob(e.forgeName, mr, diff, issue), o, ai.NoteRefs(mr, issue)...)
			if err != nil {
				return err
			}
			_, out, err := e.runJob(cmd, sj, o)
			if err != nil {
				return err
			}
			return emit(cmd, out)
		},
	}
	o.bind(cmd)
	o.bindNotes(cmd)
	return cmd
}

// loadMR reads the MR, its latest diff, and the issue it closes (if any).
func (e *aiEnv) loadMR(ctx context.Context, ref forge.Ref) (forge.MR, forge.Diff, *forge.Issue, error) {
	return ai.LoadMR(ctx, e.be, ref)
}

// --- classify ----------------------------------------------------------------

func newAIClassifyCmd() *cobra.Command {
	var (
		o     aiOpts
		apply bool
		noAI  bool
	)
	cmd := &cobra.Command{
		Use:   "classify <issue-ref>",
		Short: "Suggest the missing type/app labels for an issue; --apply adds them after a confirmation",
		Long: "Suggests labels only for the type and app facets the issue lacks, from values the team\n" +
			"already uses. With AI on the model suggests; with ai.mode = \"off\", no profile, or --no-ai\n" +
			"the suggestion is deterministic (keywords in the title and description, no cost).\n" +
			"Nothing is applied without --apply, and --apply asks first (--yes or an interactive\n" +
			"yes); it only adds labels, never removes any.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := forge.ParseRef(args[0])
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
			issue, err := e.be.Issue(cmd.Context(), ref)
			if err != nil {
				return fmt.Errorf("read %s: %w", ref, err)
			}
			in, err := classifyInput(cmd.Context(), e.a, e.be, issue)
			if err != nil {
				return err
			}
			opts := ai.ClassifyOpts{Profile: o.profile, Refresh: o.refresh, Override: o.override, Assume: o.yes, Confirm: confirmPrompt(cmd), NoAI: noAI}
			w := cmd.OutOrStdout()
			if o.dryRun && e.svc.UsesAI(opts) {
				job, err := ai.ClassifyJob(e.forgeName, in)
				if errors.Is(err, ai.ErrNothingToClassify) {
					fmt.Fprintf(w, "%s: %s; nothing to suggest (no AI call made)\n", ref, err)
					return nil
				}
				if err != nil {
					return err
				}
				_, out, err := e.runJob(cmd, job, o)
				if err != nil {
					return err
				}
				return emit(cmd, out)
			}
			cr, err := e.svc.Classify(cmd.Context(), e.forgeName, in, opts)
			if errors.Is(err, ai.ErrNothingToClassify) {
				if jsonOut {
					return printJSON(w, classifyOut{Suggestion: ai.Suggestion{Labels: []string{}}})
				}
				fmt.Fprintf(w, "%s: %s; nothing to suggest (no AI call made)\n", ref, err)
				return nil
			}
			if err != nil {
				return aiError(err)
			}
			out := classifyOut{Ref: ref.String(), Suggestion: cr.Suggestion, DryRun: o.dryRun}
			if cr.Run != nil {
				out.aiOut = aiOut{
					Feature: "classify", Ref: ref.String(), CacheHit: cr.Run.CacheHit, CostUSD: cr.Run.CostUSD, SavedUSD: cr.Run.SavedUSD,
					Profile: cr.Run.Profile, Model: cr.Run.Model, Artifact: cr.Run.Artifact,
				}
			}
			if !jsonOut {
				printSuggestion(w, ref, cr.Suggestion)
				if cr.Run != nil {
					printFooter(w, out.aiOut)
				}
			}
			if apply && len(cr.Suggestion.Labels) > 0 && !o.dryRun {
				ok, err := confirmApply(cmd, ref, cr.Suggestion.Labels, o.yes)
				if err != nil {
					return err
				}
				if ok {
					iw, isWriter := e.be.(forge.IssueWriter)
					if !isWriter {
						return fmt.Errorf("forge %s cannot write issue labels", e.forgeName)
					}
					if err := ai.ApplyLabels(cmd.Context(), iw, e.a.Audit, ref, cr.Suggestion.Labels, cr.Suggestion.Source); err != nil {
						return err
					}
					out.Applied = true
					if !jsonOut {
						fmt.Fprintf(w, "%s: added %s\n", ref, strings.Join(cr.Suggestion.Labels, ", "))
					}
				} else if !jsonOut {
					fmt.Fprintln(w, "not applied")
				}
			} else if apply && !jsonOut && len(cr.Suggestion.Labels) == 0 {
				fmt.Fprintln(w, "nothing to apply")
			}
			if jsonOut {
				return printJSON(w, out)
			}
			return nil
		},
	}
	o.bind(cmd)
	cmd.Flags().BoolVar(&apply, "apply", false, "add the suggested labels to the issue (asks first; --yes skips the question)")
	cmd.Flags().BoolVar(&noAI, "no-ai", false, "suggest from keywords only: no model call, no cost")
	return cmd
}

// classifyOut is the JSON shape of `ade ai classify`.
type classifyOut struct {
	aiOut
	Suggestion ai.Suggestion `json:"suggestion"`
	Applied    bool          `json:"applied"`
}

// confirmApply asks before labels are written: --yes, or an interactive yes.
// A script that did not say --yes gets an error, never a silent write.
func confirmApply(cmd *cobra.Command, ref forge.Ref, labels []string, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	if f, ok := cmd.InOrStdin().(*os.File); !ok || !isTerminal(f) {
		return false, withCode(exitNeedsConfirm, fmt.Errorf("not applying %s to %s without a confirmation: pass --yes", strings.Join(labels, ", "), ref))
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Add %s to %s? [y/N] ", strings.Join(labels, ", "), ref)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}

func printSuggestion(w io.Writer, ref forge.Ref, s ai.Suggestion) {
	how := ""
	if s.Source != "" {
		how = " (" + s.Source + ")"
	}
	if len(s.Labels) == 0 {
		fmt.Fprintf(w, "%s: no label suggested%s\n", ref, how)
	} else {
		fmt.Fprintf(w, "%s: suggested labels%s: %s\n", ref, how, strings.Join(s.Labels, ", "))
	}
	if s.Reason != "" {
		fmt.Fprintf(w, "  why: %s\n", s.Reason)
	}
	for _, d := range s.Dropped {
		fmt.Fprintf(w, "  ignored %s\n", d)
	}
	switch {
	case s.Weight != nil:
		fmt.Fprintf(w, "  suggested weight: %d (%s)\n    set it with: ade issue weight %s %d\n", *s.Weight, s.WeightReason, ref, *s.Weight)
	case s.WeightReason != "":
		fmt.Fprintf(w, "  %s\n", s.WeightReason)
	}
}

// classifyInput finds the manifest that serves the issue's project and
// gathers the label patterns and the values the team already uses. An issue
// of a code repo inherits the facets of its tracker parent.
func classifyInput(ctx context.Context, a *app.App, f forge.Forge, is forge.Issue) (ai.ClassifyInput, error) {
	found, err := project.Serving(ctx, a, project.FetchManifest, is.Ref.Project)
	if err != nil {
		return ai.ClassifyInput{Issue: is}, fmt.Errorf("%w: its manifest holds the label patterns", err)
	}
	var parent *forge.Issue
	if f != nil && is.Ref.Project != found.Tracker.Project {
		links, _ := f.Links(ctx, is.Ref)
		parent = startwork.TrackerParent(ctx, f, found, is, links)
	}
	in := ai.NewClassifyInput(found, is, parent)
	if st, err := a.Cache(); err == nil {
		if results, err := forgesync.Load(st); err == nil {
			seen := map[string]bool{}
			var items []domain.WorkItem
			for _, r := range results {
				items = append(items, r.Items...)
				for _, it := range r.Items {
					if it.Type != "" && !seen[it.Type] {
						seen[it.Type] = true
						in.KnownTypes = append(in.KnownTypes, it.Type)
					}
				}
			}
			in.Samples = ai.WeightSamples(items)
		}
	}
	// The synced items cover the sprint window; the forge has the history.
	if f != nil && found.Tracker.Project != "" {
		if hist, err := ai.ClosedSamples(ctx, f, found.Tracker.Project, found.Labels, found.Tracker.Weight, time.Now()); err == nil {
			in.Samples = ai.MergeSamples(in.Samples, hist)
		}
	}
	return in, nil
}

// --- draft-reply -------------------------------------------------------------

func newAIReplyCmd() *cobra.Command {
	var o aiOpts
	cmd := &cobra.Command{
		Use:   "draft-reply <mr-ref> <discussion>",
		Short: "Draft a short reply to a review thread (printed; post it with `ade mr reply`)",
		Long: "<discussion> is a discussion id, or any unique prefix of one. Without a match the\n" +
			"threads of the MR are listed.",
		Args: cobra.ExactArgs(2),
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
			ds, err := e.be.Discussions(cmd.Context(), ref)
			if err != nil {
				return fmt.Errorf("read the threads of %s: %w", ref, err)
			}
			disc, err := pickDiscussion(ds, args[1])
			if err != nil {
				return err
			}
			mr, diff, issue, err := e.loadMR(cmd.Context(), ref)
			if err != nil {
				return err
			}
			rj, err := e.withNotes(ai.ReplyJob(e.forgeName, mr, disc, diff), o, ai.NoteRefs(mr, issue)...)
			if err != nil {
				return err
			}
			_, out, err := e.runJob(cmd, rj, o)
			if err != nil {
				return err
			}
			return emit(cmd, out)
		},
	}
	o.bind(cmd)
	o.bindNotes(cmd)
	return cmd
}

func pickDiscussion(ds []forge.Discussion, want string) (forge.Discussion, error) {
	var hits []forge.Discussion
	for _, d := range ds {
		if d.ID == want {
			return d, nil
		}
		if strings.HasPrefix(d.ID, want) {
			hits = append(hits, d)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	var b strings.Builder
	switch len(hits) {
	case 0:
		fmt.Fprintf(&b, "no discussion matches %q; threads:", want)
		hits = ds
	default:
		fmt.Fprintf(&b, "%q matches several discussions:", want)
	}
	for _, d := range hits {
		first := ""
		if len(d.Notes) > 0 {
			first = strings.Join(strings.Fields(d.Notes[0].Body), " ")
			if len(first) > 60 {
				first = first[:60] + "…"
			}
		}
		fmt.Fprintf(&b, "\n  %s  %s", d.ID, first)
	}
	return forge.Discussion{}, errors.New(b.String())
}

// --- review ------------------------------------------------------------------

func newAIReviewCmd() *cobra.Command {
	var (
		o      aiOpts
		guides []string
	)
	cmd := &cobra.Command{
		Use:   "review <mr-ref>",
		Short: "Run an agent review pass: structured comments become pending drafts (nothing is posted)",
		Long: "The review role reads the diff, the linked issue, existing threads and any --guide\n" +
			"files, and answers with comments (path, line, side, severity, body). Each is checked\n" +
			"against the MR's latest diff and saved as a pending agent draft; comments on lines the\n" +
			"diff does not show are rejected. Triage with `ade ai triage`; accepted drafts go out as\n" +
			"one batch with `ade review submit`. Denied and pending drafts are never posted.",
		Args: cobra.ExactArgs(1),
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
			mr, diff, issue, err := e.loadMR(cmd.Context(), ref)
			if err != nil {
				return err
			}
			threads, err := e.be.Discussions(cmd.Context(), ref)
			if err != nil {
				return fmt.Errorf("read the threads of %s: %w", ref, err)
			}
			docs, err := readGuides(guides)
			if err != nil {
				return err
			}
			rj, err := e.withNotes(ai.ReviewJob(e.forgeName, mr, diff, issue, threads, docs), o, ai.NoteRefs(mr, issue)...)
			if err != nil {
				return err
			}
			res, out, err := e.runJob(cmd, rj, o)
			if err != nil {
				return err
			}
			if o.dryRun {
				return emit(cmd, out)
			}
			comments, err := ai.ParseReview(res.Text)
			if err != nil {
				return err
			}
			st, err := reviewStore(cmd)
			if err != nil {
				return err
			}
			sess, err := review.Resume(st, ref, diff)
			if err != nil {
				return err
			}
			added, rejected, err := e.svc.AddReviewDrafts(sess, comments, res.Profile)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), struct {
					aiOut
					Drafts   []review.Draft `json:"drafts"`
					Rejected []ai.Rejected  `json:"rejected"`
				}{out, emptyIfNil(added), emptyIfNil(rejected)})
			}
			printReviewResult(cmd.OutOrStdout(), ref, added, rejected)
			printFooter(cmd.OutOrStdout(), out)
			return nil
		},
	}
	o.bind(cmd)
	o.bindNotes(cmd)
	cmd.Flags().StringArrayVar(&guides, "guide", nil, "extra context file (AGENTS.md, contributing notes); repeatable")
	return cmd
}

func emptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func readGuides(paths []string) (map[string]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("--guide: %w", err)
		}
		out[filepath.Base(p)] = string(data)
	}
	return out, nil
}

func printReviewResult(w io.Writer, ref forge.Ref, added []review.Draft, rejected []ai.Rejected) {
	fmt.Fprintf(w, "%s: %s saved as pending agent drafts", ref, plural(len(added), "comment"))
	if len(rejected) > 0 {
		fmt.Fprintf(w, ", %d rejected", len(rejected))
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, d := range added {
		fmt.Fprintf(tw, "  %s\t%s:%d\t%s\t%s\n", d.ID, d.Path, d.Line, nonEmpty(string(d.Severity), "comment"), firstLine(d.Body))
	}
	tw.Flush()
	for _, r := range rejected {
		fmt.Fprintf(w, "  rejected %s:%d: %s\n", r.Comment.Path, r.Comment.Line, r.Reason)
	}
	if len(added) > 0 {
		fmt.Fprintf(w, "triage: ade ai triage accept|deny|edit %s <id>… · send: ade review submit %s\n", ref, ref)
	}
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

// --- models ------------------------------------------------------------------

// roleUse is one role of a profile and the model that serves it.
type roleUse struct {
	Role  string `json:"role"`
	Model string `json:"model"`
	// From is the role whose entry supplied Model when Role has none of its own.
	From string `json:"from,omitempty"`
	// Offered is whether the driver lists Model; false means the gateway
	// would likely reject it. Unknown (nil) when the model list was empty.
	Offered *bool `json:"offered,omitempty"`
}

// roleUses resolves every role of p, fallbacks included, against the models
// the driver offers.
func roleUses(p config.AIProfile, offered []string) []roleUse {
	var out []roleUse
	for _, role := range config.AIRoles {
		m, from, err := p.ModelFrom(role)
		if err != nil {
			continue
		}
		u := roleUse{Role: role, Model: m}
		if from != role {
			u.From = from
		}
		if len(offered) > 0 {
			ok := slices.Contains(offered, m)
			u.Offered = &ok
		}
		out = append(out, u)
	}
	return out
}

func newAIModelsCmd() *cobra.Command {
	var profile string
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List the models the profile's driver or gateway offers and which role uses which",
		Long: `Lists the model ids the active profile's gateway (or provider) offers and marks
the roles that use each: explain, classify, draft, plan, implement, review.
A role without its own model shows the one it falls back to (classify uses
explain's). A configured model the gateway does not list is flagged.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			name, p, err := a.Config.Profile(profile)
			if err != nil {
				return err
			}
			svc, err := ai.NewService(a)
			if err != nil {
				return err
			}
			d, err := svc.Open(cmd.Context(), p)
			if err != nil {
				return err
			}
			ms, err := d.Models(cmd.Context())
			if err != nil {
				return err
			}
			slices.Sort(ms)
			uses := roleUses(p, ms)
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), map[string]any{"profile": name, "driver": p.Driver, "models": ms, "roles": uses})
			}
			w := cmd.OutOrStdout()
			rolesOf := func(model string) string {
				var rs []string
				for _, u := range uses {
					if u.Model != model {
						continue
					}
					r := u.Role
					if u.From != "" {
						r += " (via " + u.From + ")"
					}
					rs = append(rs, r)
				}
				return strings.Join(rs, ", ")
			}
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			for _, m := range ms {
				if rs := rolesOf(m); rs != "" {
					fmt.Fprintf(tw, "%s\t%s\n", m, rs)
				} else {
					fmt.Fprintln(tw, m)
				}
			}
			_ = tw.Flush()
			for _, u := range uses {
				if u.Offered != nil && !*u.Offered {
					fmt.Fprintf(w, "\nwarning: %s uses %q, which the gateway does not list\n", u.Role, u.Model)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "", "profile (default: ai.profile)")
	return cmd
}
