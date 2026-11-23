package startwork_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/startwork"
)

func TestStartStopsAtAClosedMR(t *testing.T) {
	for _, tier := range tiers {
		t.Run(tier.String(), func(t *testing.T) {
			h := newHarness(t, tier)
			ctx := context.Background()
			_, first, err := h.start(3, draftApi())
			if err != nil {
				t.Fatal(err)
			}
			closed := *target(t, first, "api").MR
			if err := gitlab.New(h.srv.URL, fake.Token).CloseMR(ctx, closed); err != nil {
				t.Fatal(err)
			}
			h.drain()
			before := h.writes()

			// Dry run says so too, and shows no MR body to be sent.
			_, dry, err := h.env.Start(ctx, h.resolve(3), draftApi(), true)
			if err != nil {
				t.Fatal(err)
			}
			if len(dry.Previews) != 0 {
				t.Errorf("dry run previews an MR body: %+v", dry.Previews)
			}
			var step startwork.StepResult
			for _, s := range dry.Steps {
				if s.Kind == startwork.StepMR {
					step = s
				}
			}
			if step.Outcome != startwork.OutcomeClosedMR || !strings.Contains(step.Text, "ade mr reopen "+closed.String()) || !strings.Contains(step.Text, "--new-mr") {
				t.Errorf("dry-run MR step = %+v", step)
			}

			plan, second, err := h.start(3, draftApi())
			if err != nil {
				t.Fatalf("a closed MR is not an error: %v", err)
			}
			if got := h.writes() - before; got != 0 {
				t.Errorf("rerun made %d forge writes, want 0 (no new MR)", got)
			}
			if plan.Writes() != 0 {
				t.Errorf("plan still writes: %+v", plan.Steps)
			}
			for _, s := range second.Steps {
				if s.Kind == startwork.StepMR && (s.Outcome != startwork.OutcomeClosedMR || s.Error != "") {
					t.Errorf("MR step = %+v", s)
				}
			}
			if mrs, _ := h.forge.ListMRs(ctx, forge.MRQuery{Project: apiProj, State: "all", SourceBranch: target(t, second, "api").Branch}); len(mrs) != 1 {
				t.Errorf("%d MRs, want only the closed one", len(mrs))
			}

			// --new-mr opens a fresh MR beside the closed one.
			ch := draftApi()
			ch.NewMR = true
			_, third, err := h.start(3, ch)
			if err != nil {
				t.Fatal(err)
			}
			if mr := target(t, third, "api").MR; mr == nil || *mr == closed {
				t.Errorf("--new-mr result MR = %v, want a new one", mr)
			}
		})
	}
}

// A closed MR of another branch of the issue does not stop the MR step of the
// branch being worked on.
func TestStartIgnoresAClosedMROfAnotherBranch(t *testing.T) {
	h := newHarness(t, fake.Free)
	ctx := context.Background()
	branchOnly := draftApi()
	branchOnly.Mode = startwork.ModeBranch
	_, first, err := h.start(3, branchOnly)
	if err != nil {
		t.Fatal(err)
	}
	child := target(t, first, "api").Child
	c := gitlab.New(h.srv.URL, fake.Token)
	if _, err := c.CreateBranch(ctx, apiProj, "other-attempt", "main"); err != nil {
		t.Fatal(err)
	}
	other, err := c.CreateMR(ctx, forge.MRDraft{Project: apiProj, Source: "other-attempt", Target: "main", Title: "other", Description: "Closes #" + strconv.Itoa(child.IID)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CloseMR(ctx, other.Ref); err != nil {
		t.Fatal(err)
	}
	plan, _, err := h.start(3, draftApi())
	if err != nil {
		t.Fatal(err)
	}
	f := plan.Targets[0].Found
	if f.ClosedMR != nil {
		t.Fatalf("closed MR of branch %q stopped branch %q", f.ClosedMR.SourceBranch, f.Branch)
	}
	if plan.Writes() == 0 {
		t.Errorf("the MR step should still be pending: %+v", plan.Steps)
	}
}
