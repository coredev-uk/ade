package agile

import (
	"context"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/forgesync"
)

var fakeNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

const fakeTracker = fake.GroupPath + "/tracker"

func fakeReview(t *testing.T, tier fake.Tier, setup func(srv *fake.Server)) Review {
	t.Helper()
	srv := fake.New(tier, fake.WithClock(fakeNow))
	t.Cleanup(srv.Close)
	if setup != nil {
		setup(srv)
	}
	c := gitlab.New(srv.URL, fake.Token, gitlab.WithClock(func() time.Time { return fakeNow }))
	ctx := context.Background()
	ms, err := c.Milestones(ctx, fake.GroupPath)
	if err != nil {
		t.Fatal(err)
	}
	sprints := domain.SprintsFromMilestones(ms)
	m := manifest()
	m.Tracker.Project = fakeTracker
	cur := domain.WindowAt(sprints, fakeNow, nil, 1).Current
	if cur == nil || cur.Title != "Sprint 42" {
		t.Fatalf("current sprint = %+v", cur)
	}
	r, err := BuildReviewLive(ctx, c, ReviewLiveInput{
		Project: "sandbox", Manifest: m, Sprint: *cur, Sprints: sprints, Me: forge.User{Username: "sandbox-dev"},
		Cal: utcCal(), Now: fakeNow, VelocityN: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReviewAgainstPremiumFake(t *testing.T) {
	r := fakeReview(t, fake.Premium, nil)
	// Sprint 42 holds #1-#6, #10 and #11: weights 3 5 2 1 3 – 2 3, and only
	// #10 (closed) is finished.
	if want := (Totals{Count: 8, Weight: 19, Weighted: 7, Unweighted: 1}); r.Committed != want || r.Added.Count != 0 {
		t.Fatalf("committed = %+v added = %+v, want %+v", r.Committed, r.Added, want)
	}
	if want := (Totals{Count: 1, Weight: 2, Weighted: 1}); r.Done != want {
		t.Fatalf("done = %+v, want %+v", r.Done, want)
	}
	if want := (Totals{Count: 7, Weight: 17, Weighted: 6, Unweighted: 1}); r.Carried != want {
		t.Fatalf("carried = %+v, want %+v", r.Carried, want)
	}
	if r.Basis != BasisWeight || len(r.Warnings) != 0 {
		t.Fatalf("basis %s warnings %v", r.Basis, r.Warnings)
	}
	// Sprint 41 holds #9 (weight 1, closed): the only past sprint.
	if len(r.Velocity.Sprints) != 1 || r.Velocity.Sprints[0].Velocity == nil || *r.Velocity.Sprints[0].Velocity != 1 {
		t.Fatalf("velocity = %+v", r.Velocity)
	}
	if r.Epics != nil { // the only epic'd issue (#8) is in Sprint 43
		t.Fatalf("epics = %+v", r.Epics)
	}
}

func TestReviewAgainstFreeFakeCountsItems(t *testing.T) {
	r := fakeReview(t, fake.Free, nil)
	if r.Basis != BasisCount || r.Done.Count != 1 || r.Committed.Count != 8 || r.Done.Weight != 0 {
		t.Fatalf("free tier has no weights: basis %s, done %+v, committed %+v", r.Basis, r.Done, r.Committed)
	}
	if r.Epics != nil {
		t.Fatalf("epics need Premium: %+v", r.Epics)
	}
	if r.Velocity.Basis != BasisCount || r.Velocity.Average == nil || *r.Velocity.Average != 1 {
		t.Fatalf("velocity = %+v", r.Velocity)
	}
}

func TestReviewScopeAddedMidSprintAndUnweightedVelocity(t *testing.T) {
	r := fakeReview(t, fake.Premium, func(srv *fake.Server) {
		closed := at("2026-11-09 16:00")
		ref, err := srv.AddIssue(fake.IssueSpec{
			Title: "Hotfix export crash", Milestone: "Sprint 42", Weight: weight(5), Assigned: true,
			Created: at("2026-11-05 09:00"), Closed: &closed, Epic: "Self-serve exports",
		})
		if err != nil {
			t.Fatal(err)
		}
		// The milestone was set after creation: a mid-sprint addition.
		if err := srv.AddSprintEvent(ref, "Sprint 42", "add", at("2026-11-05 09:30")); err != nil {
			t.Fatal(err)
		}
		// An older sprint whose work was never weighted: no velocity at all.
		srv.AddMilestone("Sprint 40", "closed", "2026-10-05", "2026-10-16")
		for _, title := range []string{"old a", "old b"} {
			c := at("2026-10-14 10:00")
			if _, err := srv.AddIssue(fake.IssueSpec{Title: title, Milestone: "Sprint 40", Created: at("2026-10-01 09:00"), Closed: &c}); err != nil {
				t.Fatal(err)
			}
		}
	})
	if want := (Totals{Count: 1, Weight: 5, Weighted: 1}); r.Added != want {
		t.Fatalf("added = %+v, want %+v", r.Added, want)
	}
	if r.Done.Count != 2 || r.Done.Weight != 7 {
		t.Fatalf("done = %+v, want #10 and the hotfix: 7w", r.Done)
	}
	var added ReviewItem
	for _, it := range r.Items {
		if it.Added {
			added = it
		}
	}
	if added.Title != "Hotfix export crash" || added.DoneAt == nil || !added.DoneAt.Equal(at("2026-11-09 16:00")) || added.Epic != "Self-serve exports" {
		t.Fatalf("added item = %+v", added)
	}
	if len(r.Epics) != 2 || r.Epics[0].Epic != "Self-serve exports" {
		t.Fatalf("epics = %+v", r.Epics)
	}

	var s40, s41 *SprintResult
	for i := range r.Velocity.Sprints {
		switch r.Velocity.Sprints[i].Title {
		case "Sprint 40":
			s40 = &r.Velocity.Sprints[i]
		case "Sprint 41":
			s41 = &r.Velocity.Sprints[i]
		}
	}
	if s40 == nil || s41 == nil || s40.Velocity != nil || s40.Done.Count != 2 {
		t.Fatalf("sprint 40 (all unweighted) must have no weight velocity: %+v", s40)
	}
	if r.Velocity.Average == nil || *r.Velocity.Average != 1 {
		t.Fatalf("average = %v, want 1: the unweighted sprint is not a zero", r.Velocity.Average)
	}
}

func TestPickSprint(t *testing.T) {
	s41 := domain.Sprint{Key: "41", Title: "Sprint 41", Start: forge.NewDate(2026, 10, 19), Due: forge.NewDate(2026, 10, 30)}
	s43 := domain.Sprint{Key: "43", Title: "Sprint 43", Start: forge.NewDate(2026, 11, 16), Due: forge.NewDate(2026, 11, 27)}
	res := forgesync.Result{Project: "p", Sprints: []domain.Sprint{s41, sprint42, s43}, SyncedAt: fakeNow, Window: domain.Window{Current: &sprint42}}
	if got, err := PickSprint(res, ""); err != nil || got.Title != "Sprint 42" {
		t.Fatalf("default: %+v, %v", got, err)
	}
	if got, err := PickSprint(res, "sprint 41"); err != nil || got.Key != "41" {
		t.Fatalf("by title (case-insensitive): %+v, %v", got, err)
	}
	if got, err := PickSprint(res, "43"); err != nil || got.Title != "Sprint 43" {
		t.Fatalf("by key: %+v, %v", got, err)
	}
	if _, err := PickSprint(res, "Sprint 99"); err == nil {
		t.Fatal("unknown sprint must be an error")
	}
	res.Window = domain.Window{}
	if got, err := PickSprint(res, ""); err != nil || got.Title != "Sprint 42" {
		t.Fatalf("between sprints: the newest started one, got %+v, %v", got, err)
	}
}
