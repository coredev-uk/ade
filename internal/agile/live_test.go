package agile

import (
	"context"
	"errors"
	"strings"
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

// The live path reads each merged item's assignment from the forge's system
// notes (the real GitLab client against the fake) and takes the merge time from
// the cached MRs.
func TestReviewCycleTimeAgainstFake(t *testing.T) {
	day := func(d, hm string) time.Time {
		v, _ := time.Parse("2006-01-02 15:04", d+" "+hm)
		return v
	}
	var closed, noNote forge.Ref
	srv := fake.New(fake.Premium, fake.WithClock(fakeNow))
	defer srv.Close()
	closed = forge.Ref{Project: fakeTracker, IID: 10, Kind: forge.KindIssue} // closed in the seed, in Sprint 42
	noNote = forge.Ref{Project: fakeTracker, IID: 4, Kind: forge.KindIssue}
	if err := srv.AddSystemNote(closed, "added ~type::task label", day("2026-11-02", "08:00")); err != nil {
		t.Fatal(err)
	}
	if err := srv.AddSystemNote(closed, "assigned to @sandbox-dev", day("2026-11-03", "09:00")); err != nil {
		t.Fatal(err)
	}
	mrRef, err := srv.AddMergedMR(fake.GroupPath+"/api", "Upgrade", closed, day("2026-11-03", "10:00"), day("2026-11-05", "09:00"))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.SetClosedAt(noNote, day("2026-11-06", "12:00")); err != nil {
		t.Fatal(err)
	}
	noNoteMR, err := srv.AddMergedMR(fake.GroupPath+"/api", "Remove", noNote, day("2026-11-04", "10:00"), day("2026-11-06", "11:00"))
	if err != nil {
		t.Fatal(err)
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
	merged := func(ref forge.Ref, at time.Time) []forge.MR {
		return []forge.MR{{Ref: ref, State: "merged", MergedAt: &at}}
	}
	items := []domain.WorkItem{
		{Ref: closed, MRs: merged(mrRef, day("2026-11-05", "09:00"))},
		{Ref: noNote, MRs: merged(noNoteMR, day("2026-11-06", "11:00"))},
	}
	r, err := BuildReviewLive(ctx, c, ReviewLiveInput{
		Project: "sandbox", Manifest: m, Sprint: *cur, Sprints: sprints, Me: forge.User{Username: "sandbox-dev"},
		Cal: utcCal(), Now: fakeNow, VelocityN: 3, Items: items,
	})
	if err != nil {
		t.Fatal(err)
	}
	byIID := map[int]ReviewItem{}
	for _, it := range r.Items {
		byIID[it.Ref.IID] = it
	}
	if it := byIID[10]; it.AssignedFrom != AssignedFromNote || it.CycleHours == nil || *it.CycleHours != 48 {
		t.Errorf("#10 (assigned by note 11-03 09:00, merged 11-05 09:00): %+v", it)
	}
	if it := byIID[4]; it.AssignedFrom != AssignedFromCreated || it.CycleHours == nil {
		t.Errorf("#4 has no assignment note in the fake: it falls back to its creation time: %+v", it)
	}
	if r.Cycle.Items != 2 || r.Cycle.MedianHours == nil {
		t.Errorf("summary %+v", r.Cycle)
	}
}

func TestPickProjectWithSeveralCachedProjects(t *testing.T) {
	sp := []domain.Sprint{{Title: "Sprint 1"}}
	results := []forgesync.Result{
		{Project: "a", Items: []domain.WorkItem{{}}},
		{Project: "b", Sprints: sp},
		{Project: "c", Sprints: sp},
	}
	_, err := pickProject(results, "")
	var several *severalProjectsError
	if !errors.As(err, &several) || !strings.Contains(err.Error(), "choose one with --project") {
		t.Fatalf("pickProject without a name = %v, want the several-projects error", err)
	}
	if res, err := several.first(); err != nil || res.Project != "b" {
		t.Errorf("first = %q, %v: want b, the first with sprints", res.Project, err)
	}
	if note := several.chose("b"); !strings.Contains(note, "reviewing b") || !strings.Contains(note, "a, b, c") {
		t.Errorf("note = %q", note)
	}
	if res, err := pickProject(results, "c"); err != nil || res.Project != "c" {
		t.Errorf("pickProject c = %q, %v", res.Project, err)
	}
	if _, err := (&severalProjectsError{results: results[:1]}).first(); err == nil {
		t.Error("no project with sprints should stay an error")
	}
}

func TestNoProjectWithSprintsIsNotTheSeveralProjectsError(t *testing.T) {
	results := []forgesync.Result{
		{Project: "a", Items: []domain.WorkItem{{}}},
		{Project: "b", Items: []domain.WorkItem{{}}},
	}
	_, err := pickProject(results, "")
	var several *severalProjectsError
	if !errors.As(err, &several) {
		t.Fatalf("pickProject = %v, want the several-projects error", err)
	}
	if _, err := several.first(); !errors.Is(err, ErrNoSprints) || strings.Contains(err.Error(), "--project") {
		t.Errorf("first = %v, want ErrNoSprints without the advice to pick a project", err)
	}
}
