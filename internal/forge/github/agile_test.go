package github_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

func TestLabelAndSprintEvents(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	ref := issueRef(trackerProj, 1)

	les, err := c.LabelEvents(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range les {
		got = append(got, e.Action+" "+e.Label)
		if e.At.IsZero() {
			t.Errorf("event without a time: %+v", e)
		}
	}
	want := []string{"add workflow:in progress", "remove workflow:in progress", "add workflow:in review"}
	if len(got) != len(want) {
		t.Fatalf("label events = %v, want %v (milestone and close events are not label events)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("label event %d = %q, want %q", i, got[i], want[i])
		}
	}
	if les[0].At.After(les[1].At) {
		t.Error("label events are not oldest first")
	}

	ses, err := c.SprintEvents(ctx, ref)
	if err != nil || len(ses) != 1 {
		t.Fatalf("sprint events = %+v, %v", ses, err)
	}
	s := ses[0]
	if s.Source != forge.SprintByMilestone || s.Title != "Sprint 42" || s.Action != "add" || s.ID == 0 || s.At.IsZero() {
		t.Errorf("sprint event = %+v", s)
	}
	cur, _ := c.Issue(ctx, ref)
	if s.ID != cur.Milestone.ID {
		t.Errorf("sprint event id %d, the issue's milestone id is %d: they must agree so the window matches", s.ID, cur.Milestone.ID)
	}
	if none, err := c.SprintEvents(ctx, issueRef(trackerProj, 3)); err != nil || len(none) != 0 {
		t.Errorf("issue without sprint history: %+v, %v", none, err)
	}
	if _, err := c.LabelEvents(ctx, issueRef(trackerProj, 99)); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown issue: err = %v", err)
	}
	if _, err := c.SprintEvents(ctx, mrRef(apiProj, 2)); err == nil {
		t.Error("an MR ref was accepted")
	}
}

func TestProjectEvents(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	all, err := c.ProjectEvents(ctx, apiProj, time.Time{}, time.Time{})
	if err != nil || len(all) == 0 {
		t.Fatalf("events = %d, %v", len(all), err)
	}
	have := map[string]bool{}
	for _, e := range all {
		have[e.Action+"/"+e.TargetType] = true
		if e.Author.Username == "" || e.At.IsZero() {
			t.Errorf("event = %+v", e)
		}
		if e.Target.Project != "" && e.Target.Project != apiProj {
			t.Errorf("target of another project: %+v", e)
		}
	}
	for _, want := range []string{"opened/Issue", "closed/Issue", "opened/MergeRequest", "accepted/MergeRequest", "commented on/Note"} {
		if !have[want] {
			t.Errorf("no %q event among %v", want, have)
		}
	}
	pushes := 0
	for _, e := range all {
		if e.Action == "pushed to" {
			pushes++
			if e.Branch == "" || e.Branch[:5] == "refs/" {
				t.Errorf("push branch = %q: want the bare branch name", e.Branch)
			}
		}
		if e.Action == "accepted" && e.Target.Kind != forge.KindMR {
			t.Errorf("a merge must target an MR: %+v", e)
		}
	}
	if pushes == 0 {
		t.Error("no push events")
	}

	// after and before are exclusive calendar days.
	day := func(d int) time.Time { return time.Date(2026, 11, d, 18, 30, 0, 0, time.UTC) }
	win, err := c.ProjectEvents(ctx, apiProj, day(4), day(8))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range win {
		if d := e.At.UTC().Day(); e.At.Month() != time.November || d <= 4 || d >= 8 {
			t.Errorf("event on %s outside the exclusive window 4..8 Nov: %+v", e.At, e)
		}
	}
	if len(win) == 0 || len(win) >= len(all) {
		t.Errorf("window holds %d of %d events", len(win), len(all))
	}
	if _, err := c.ProjectEvents(ctx, "ade-demo/nope", time.Time{}, time.Time{}); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown project: err = %v", err)
	}
}
