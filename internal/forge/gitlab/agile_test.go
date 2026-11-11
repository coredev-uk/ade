package gitlab_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

func TestLabelEventsAreOrderedAndNamed(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ref := issueRef(tracker, 2)
		if err := srv.AddLabelEvent(ref, "workflow::in progress", "add", clock.Add(-72*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := srv.AddLabelEvent(ref, "workflow::done", "add", clock.Add(-96*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := srv.AddLabelEvent(ref, "workflow::in progress", "remove", clock.Add(-48*time.Hour)); err != nil {
			t.Fatal(err)
		}
		got, err := c.LabelEvents(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		want := []forge.LabelEvent{
			{Label: "workflow::done", Action: "add", At: clock.Add(-96 * time.Hour)},
			{Label: "workflow::in progress", Action: "add", At: clock.Add(-72 * time.Hour)},
			{Label: "workflow::in progress", Action: "remove", At: clock.Add(-48 * time.Hour)},
		}
		if len(got) != len(want) {
			t.Fatalf("got %+v", got)
		}
		for i := range want {
			if got[i].Label != want[i].Label || got[i].Action != want[i].Action || !got[i].At.Equal(want[i].At) {
				t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	})
}

func TestLabelEventsDefaultToCurrentLabels(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		got, err := c.LabelEvents(context.Background(), issueRef(tracker, 1))
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, e := range got {
			names[e.Label] = e.Action == "add"
		}
		for _, l := range []string{"type::story", "app::api", "workflow::in review"} {
			if !names[l] {
				t.Errorf("no add event for %s in %+v", l, got)
			}
		}
	})
}

func TestSprintEventsPerTier(t *testing.T) {
	forEachTier(t, func(t *testing.T, tier fake.Tier, c *gitlab.Client, srv *fake.Server) {
		got, err := c.SprintEvents(context.Background(), issueRef(tracker, 1))
		if err != nil {
			t.Fatal(err)
		}
		var milestones, iterations int
		for _, e := range got {
			switch e.Source {
			case forge.SprintByMilestone:
				milestones++
				if e.Title != "Sprint 42" || e.Action != "add" || e.ID == 0 {
					t.Errorf("milestone event %+v", e)
				}
			case forge.SprintByIteration:
				iterations++
			}
		}
		wantIterations := 0
		if tier == fake.Premium {
			wantIterations = 1 // Free answers 403 for iteration events: not an error, not an event
		}
		if milestones != 1 || iterations != wantIterations {
			t.Fatalf("%s: %d milestone and %d iteration events: %+v", tier, milestones, iterations, got)
		}
	})
}

func TestSprintEventsRecordedHistoryReplacesDefault(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithClock(clock))
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, fake.Token)
	ref := issueRef(tracker, 3)
	for i, e := range []struct{ title, action string }{{"Sprint 41", "add"}, {"Sprint 41", "remove"}, {"Sprint 42", "add"}} {
		if err := srv.AddSprintEvent(ref, e.title, e.action, clock.Add(-time.Duration(10-i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.SprintEvents(context.Background(), ref)
	if err != nil || len(got) != 3 || got[0].Title != "Sprint 41" || got[2].Title != "Sprint 42" || got[2].Action != "add" {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestEventsOfMissingIssueAreNotFound(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		if _, err := c.LabelEvents(context.Background(), issueRef(tracker, 999)); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("LabelEvents: %v", err)
		}
		if _, err := c.SprintEvents(context.Background(), issueRef(tracker, 999)); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("SprintEvents: %v", err)
		}
	})
}

func TestProjectEventsWindowAndShape(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		srv.AddEvent(forge.Event{
			Action: "pushed to", Branch: "1-rate-limit", At: clock.Add(-30 * time.Hour),
			Target: forge.Ref{Project: apiProj},
		})
		srv.AddEvent(forge.Event{
			Action: "commented on", TargetType: "Note", Target: mrRef(apiProj, 1), Title: "Rate-limit", At: clock.Add(-29 * time.Hour),
		})
		// 2026-11-10 only: after is exclusive of the 9th, before of the 11th.
		got, err := c.ProjectEvents(context.Background(), apiProj,
			time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		var push, comment *forge.Event
		for i := range got {
			switch got[i].Action {
			case "pushed to":
				push = &got[i]
			case "commented on":
				comment = &got[i]
			}
		}
		if push == nil || push.Branch != "1-rate-limit" || push.Author.Username != "sandbox-dev" {
			t.Fatalf("push = %+v", push)
		}
		if comment == nil || comment.Target != mrRef(apiProj, 1) {
			t.Fatalf("a note event must name the MR it is on: %+v", comment)
		}
		for _, e := range got {
			if e.At.Before(time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)) || !e.At.Before(time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)) {
				t.Errorf("event outside the day window: %+v", e)
			}
		}
		for i := 1; i < len(got); i++ {
			if got[i].At.After(got[i-1].At) {
				t.Fatal("events must be newest first, like GitLab")
			}
		}
	})
}
