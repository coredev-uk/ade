package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/review"
)

// researchOut is the JSON shape of `ade ai research`.
type researchOut struct {
	aiOut
	// Job is the item's research job as stored in its record.
	Job *ai.ResearchView `json:"job,omitempty"`
	// Started is true when a detached worker was just started.
	Started bool `json:"started,omitempty"`
}

func newAIResearchCmd() *cobra.Command {
	var (
		o      aiOpts
		detach bool
		show   bool
		worker string
	)
	cmd := &cobra.Command{
		Use:   "research <issue-ref>",
		Short: "Research an issue: goal, open questions, approaches, risks; stored in its record (--detach runs it in the background)",
		Long: "Asks the model to research one issue from the issue, its parent, discussion, links, the\n" +
			"merge requests already open for it and your notes (it does not read code). The answer is\n" +
			"stored in the item's record and found again by its input hash, so asking again about an\n" +
			"unchanged issue is free (--refresh buys a new one). The pre-flight, caps, ledger and audit\n" +
			"apply as for every AI call.\n\n" +
			"--detach shows the pre-flight here, then runs the call in a worker that outlives this\n" +
			"command; the dashboard (R on the item) and `ade item` show its state, and a notification\n" +
			"arrives when it is done. --show prints what is stored, spending nothing. With\n" +
			"ai.mode = \"off\" the command refuses and nothing else changes.",
		Example: "  ade ai research acme/tracker#15 --detach\n  ade ai research acme/tracker#15 --show\n  ade ai research acme/tracker#15 --dry-run",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := itemRef(cmd, args[0])
			if err != nil {
				return err
			}
			if ref.Kind != forge.KindIssue {
				return fmt.Errorf("%s is a merge request: research takes an issue like group/project#3", args[0])
			}
			if show {
				return researchShow(cmd, ref, o.forge)
			}
			e, err := openAI(cmd, ref, o.forge)
			if err != nil {
				return err
			}
			defer e.stop()
			in, err := ai.GatherResearch(cmd.Context(), e.be, ref)
			if err != nil {
				return err
			}
			job, err := e.withNotes(ai.ResearchJob(e.forgeName, in), o, ai.ResearchNoteRefs(in.Issue)...)
			if err != nil {
				return err
			}
			ro := ai.ResearchOpts{Profile: o.profile, Refresh: o.refresh, Override: o.override, Assume: o.yes, Confirm: confirmPrompt(cmd)}
			switch {
			case worker != "":
				_, err := e.svc.RunResearch(cmd.Context(), e.forgeName, worker, job, ai.ResearchOpts{
					Profile: o.profile, Refresh: o.refresh, Override: o.override, Assume: true, Notify: true,
				})
				return err
			case o.dryRun:
				_, out, err := e.runJob(cmd, job, o)
				if err != nil {
					return err
				}
				return emit(cmd, out)
			case detach:
				return researchDetach(cmd, e, job, ro)
			}
			return researchForeground(cmd, e, job, ro)
		},
	}
	o.bind(cmd)
	o.bindNotes(cmd)
	cmd.Flags().BoolVar(&detach, "detach", false, "run the call in a background worker that outlives this command")
	cmd.Flags().BoolVar(&show, "show", false, "print the stored research (state and answer) and spend nothing")
	cmd.Flags().StringVar(&worker, "worker", "", "internal: run as the detached worker of this job id")
	_ = cmd.Flags().MarkHidden("worker")
	return cmd
}

// researchForeground runs the call here and files it like a detached job.
func researchForeground(cmd *cobra.Command, e *aiEnv, job ai.Job, ro ai.ResearchOpts) error {
	res, err := e.svc.RunResearch(cmd.Context(), e.forgeName, ulid.Make().String(), job, ro)
	if err != nil {
		return aiError(err)
	}
	out := researchOut{aiOut: aiOutOf(job, res)}
	return emitResearch(cmd, out)
}

// researchDetach shows the pre-flight and starts the worker.
func researchDetach(cmd *cobra.Command, e *aiEnv, job ai.Job, ro ai.ResearchOpts) error {
	st, err := e.svc.StartResearch(cmd.Context(), e.forgeName, job, ro)
	if errors.Is(err, ai.ErrResearchRunning) {
		return fmt.Errorf("%s: research is already running (started %s): `ade ai research %s --show`", job.Ref, st.Research.Started.Local().Format("15:04"), job.Ref)
	}
	if err != nil {
		return aiError(err)
	}
	out := researchOut{aiOut: aiOut{Feature: job.Feature, Ref: job.Ref.String(), IncludesNotes: job.Notes}}
	if st.Preflight.Model != "" {
		out.Preflight = &st.Preflight
	}
	if st.Result != nil {
		out.aiOut = aiOutOf(job, *st.Result)
	} else {
		out.Started = true
	}
	v := ai.ResearchView{Research: st.Research}
	out.Job = &v
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), out)
	}
	w := cmd.OutOrStdout()
	if st.Result != nil {
		return emitResearch(cmd, out)
	}
	fmt.Fprintf(w, "%s: research started in the background (job %s)\n", job.Ref, st.Research.ID)
	fmt.Fprintf(w, "  %s\n", st.Preflight.Describe())
	fmt.Fprintf(w, "  it is stored in the item's record: `ade ai research %s --show` (or R on the item in the dashboard); a notification arrives when it is done\n", job.Ref)
	return nil
}

func aiOutOf(job ai.Job, res ai.Result) aiOut {
	out := aiOut{
		Feature: job.Feature, Ref: job.Ref.String(), IncludesNotes: job.Notes,
		Text: res.Text, CacheHit: res.CacheHit, CostUSD: res.CostUSD, SavedUSD: res.SavedUSD,
		Profile: res.Profile, Model: res.Model, Artifact: res.Artifact, Truncated: res.Truncated,
	}
	if res.Preflight.Model != "" {
		out.Preflight = &res.Preflight
	}
	return out
}

func emitResearch(cmd *cobra.Command, o researchOut) error {
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), o)
	}
	return emit(cmd, o.aiOut)
}

// researchShow prints the stored research of an issue: no network, no spend.
func researchShow(cmd *cobra.Command, ref forge.Ref, forgeFlag string) error {
	a := appFrom(cmd)
	name, err := review.ForgeFor(cmd.Context(), a, ref, forgeFlag)
	if err != nil {
		return err
	}
	st := records.New(a.RecordsDir)
	svc := &ai.Service{Records: st, Now: time.Now}
	v, err := svc.ReadResearch(name, ref)
	if errors.Is(err, records.ErrNoResearch) {
		if jsonOut {
			return printJSON(cmd.OutOrStdout(), researchOut{aiOut: aiOut{Feature: "research", Ref: ref.String()}})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: no research yet: ade ai research %s --detach\n", ref, ref)
		return nil
	}
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), researchOut{aiOut: aiOut{Feature: "research", Ref: ref.String(), Text: v.Text, Artifact: v.Artifact}, Job: &v})
	}
	printResearch(cmd.OutOrStdout(), ref, v)
	return nil
}

// printResearch writes a stored research job for a terminal.
func printResearch(w io.Writer, ref forge.Ref, v ai.ResearchView) {
	fmt.Fprintf(w, "%s: %s\n", ref, researchLine(v.Research))
	if v.Error != "" {
		fmt.Fprintf(w, "  %s\n", v.Error)
	}
	if v.Text != "" {
		fmt.Fprintf(w, "\n%s\n", v.Text)
	}
}

// researchLine is one line on where a research job stands.
func researchLine(r records.Research) string {
	switch r.State {
	case records.ResearchRunning:
		return "research running since " + r.Started.Local().Format("2006-01-02 15:04")
	case records.ResearchFailed:
		return "research failed " + r.Finished.Local().Format("2006-01-02 15:04")
	}
	var b strings.Builder
	b.WriteString("research done " + r.Finished.Local().Format("2006-01-02 15:04"))
	switch {
	case r.CacheHit:
		b.WriteString(" · cached, $0")
	case r.CostUSD > 0:
		fmt.Fprintf(&b, " · $%.4f", r.CostUSD)
	}
	if r.Model != "" {
		b.WriteString(" · " + r.Model)
	}
	return b.String()
}
