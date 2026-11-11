package agile

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/ai"
)

type fakeRunner struct {
	jobs []ai.Job
	res  ai.Result
	err  error
}

func (f *fakeRunner) Run(_ context.Context, j ai.Job) (ai.Result, error) {
	f.jobs = append(f.jobs, j)
	return f.res, f.err
}

func TestAINarratorStandupJob(t *testing.T) {
	s := BuildStandup(StandupInput{Date: date("2026-11-09"), Cal: utcCal()})
	s.Narrative, s.Warnings = "stale", []string{"events: boom"}
	r := &fakeRunner{res: ai.Result{Text: "Friday was quiet."}}
	n := &AINarrator{Svc: r, Tune: func(j *ai.Job) { j.Assume = true }}
	got, err := WithNarrative(context.Background(), n, s)
	if err != nil || got.Narrative != "Friday was quiet." {
		t.Fatalf("%q, %v", got.Narrative, err)
	}
	j := r.jobs[0]
	if j.Feature != "standup" || j.Role != ai.RoleExplain || !j.Assume {
		t.Fatalf("job = %+v", j)
	}
	if j.Forge != ai.StandupForge || j.Ref.IID != 20261109 {
		t.Fatalf("artifact lands under %s/%v, want items/_standup/…/20261109", j.Forge, j.Ref)
	}
	if strings.Contains(j.Prompt, "stale") || strings.Contains(j.Prompt, "boom") {
		t.Errorf("the facts must not contain the old narrative or warnings:\n%s", j.Prompt)
	}
	if !strings.Contains(j.Prompt, "Yesterday · Fri 6 Nov") {
		t.Errorf("facts are the Markdown standup:\n%s", j.Prompt)
	}
	// Same facts, same job input: that is what makes the second call a cache hit.
	again := ai.StandupJob("all", date("2026-11-09"), RenderStandup(Standup{Date: s.Date, Yesterday: s.Yesterday, Done: s.Done, Today: s.Today, Blockers: s.Blockers}, Markdown))
	if again.Prompt != j.Prompt {
		t.Error("prompt is not deterministic")
	}
}

func TestAINarratorReviewAndFailures(t *testing.T) {
	rv := BuildReview(reviewFixture())
	r := &fakeRunner{res: ai.Result{Text: " Done 8w of 18w. "}}
	n := &AINarrator{Svc: r}
	got, err := WithReviewNarrative(context.Background(), n, rv)
	if err != nil || got.Narrative != "Done 8w of 18w." {
		t.Fatalf("%q, %v", got.Narrative, err)
	}
	if j := r.jobs[0]; j.Feature != "sprint-review" || j.Forge != ai.SprintForge || j.Ref.IID != 20261102 || !strings.Contains(j.Prompt, "# Sprint review · Sprint 42") {
		t.Fatalf("job = %+v", j)
	}

	off := &AINarrator{Svc: &fakeRunner{err: ai.ErrDisabled}}
	got, err = WithReviewNarrative(context.Background(), off, rv)
	if !errors.Is(err, ai.ErrDisabled) || got.Narrative != "" {
		t.Fatalf("ai.mode=off must leave the review deterministic: %q, %v", got.Narrative, err)
	}
	if got, err := WithReviewNarrative(context.Background(), nil, rv); err != nil || got.Narrative != "" {
		t.Fatalf("nil narrator: %q %v", got.Narrative, err)
	}
}

func TestParentNarrativeJobIsKeyedByTheFactsAndFiledUnderTheParent(t *testing.T) {
	r := BuildReview(cycleFixture())
	it := r.Items[0]
	a := ai.SprintParentJob(r.Project, r.Sprint.Title, it.Ref, parentFacts(r, it))
	if a.Feature != "sprint-parent" || a.Forge != ai.SprintForge || a.Ref.IID != it.Ref.IID || a.Ref.Project != r.Project || a.System == "" {
		t.Fatalf("job = %+v", a)
	}
	// An unchanged parent is the same call (a cache hit); a changed cycle time is a new one.
	if b := ai.SprintParentJob(r.Project, r.Sprint.Title, it.Ref, parentFacts(r, it)); b.Prompt != a.Prompt || b.Name != a.Name {
		t.Error("the same facts must give the same job")
	}
	h := 1.0
	it.CycleHours = &h
	if c := ai.SprintParentJob(r.Project, r.Sprint.Title, it.Ref, parentFacts(r, it)); c.Prompt == a.Prompt {
		t.Error("a changed cycle time must change the prompt")
	}
}
