package agile

import (
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

var sprint42 = domain.Sprint{
	Key: "42", Title: "Sprint 42", Source: "milestones",
	Start: forge.NewDate(2026, 11, 2), Due: forge.NewDate(2026, 11, 13),
}

func manifest() *config.Manifest {
	m := &config.Manifest{}
	m.Tracker.Weight = config.WeightSource{Source: "gitlab"}
	m.Labels.Workflow = config.WorkflowFacet{Pattern: "workflow::{value}", Done: "done"}
	return m
}

func iss(iid int, w *int, state string, labels ...string) forge.Issue {
	return forge.Issue{
		Ref: forge.Ref{Project: "g/tracker", IID: iid}, Title: "issue " + string(rune('A'+iid-1)), State: state,
		Labels: labels, Weight: w, CreatedAt: at("2026-10-20 09:00"), UpdatedAt: at("2026-11-10 09:00"),
		Assignees: []forge.User{me},
	}
}

func closedAt(is forge.Issue, when string) forge.Issue {
	t := at(when)
	is.ClosedAt = &t
	return is
}

func sprintAdd(action, when string) forge.SprintEvent {
	return forge.SprintEvent{Source: "milestones", ID: 42, Title: "Sprint 42", Action: action, At: at(when)}
}

func reviewFixture() ReviewInput {
	a := closedAt(iss(1, weight(3), "closed"), "2026-11-04 10:00")
	b := iss(2, weight(5), "opened", "workflow::done")
	c := iss(3, weight(2), "opened", "workflow::in progress")
	d := iss(4, nil, "opened")
	e := iss(5, weight(8), "opened")
	f := closedAt(iss(6, nil, "closed"), "2026-11-10 10:00")
	return ReviewInput{
		Project: "acme", Manifest: manifest(), Sprint: sprint42,
		Issues: []forge.Issue{a, b, c, d, e, f},
		History: map[forge.Ref]issueHistory{
			b.Ref: {Labels: []forge.LabelEvent{{Label: "workflow::done", Action: "add", At: at("2026-11-09 15:00")}}},
			e.Ref: {Sprint: []forge.SprintEvent{sprintAdd("add", "2026-11-05 10:00")}},
		},
		Me: me, Cal: utcCal(), Now: at("2026-11-11 12:00"),
	}
}

func TestReviewTotalsByCountAndWeight(t *testing.T) {
	r := BuildReview(reviewFixture())
	tests := []struct {
		name string
		got  Totals
		want Totals
	}{
		{"committed", r.Committed, Totals{Count: 5, Weight: 10, Weighted: 3, Unweighted: 2}},
		{"added", r.Added, Totals{Count: 1, Weight: 8, Weighted: 1}},
		{"done", r.Done, Totals{Count: 3, Weight: 8, Weighted: 2, Unweighted: 1}},
		{"carried", r.Carried, Totals{Count: 3, Weight: 10, Weighted: 2, Unweighted: 1}},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %+v, want %+v", tc.name, tc.got, tc.want)
		}
	}
	if r.Basis != BasisWeight {
		t.Errorf("basis = %s", r.Basis)
	}
	// Done + Carried is the whole scope: Committed + Added.
	if r.Done.Weight+r.Carried.Weight != r.Committed.Weight+r.Added.Weight {
		t.Error("done + carried weight differs from committed + added")
	}
}

func TestReviewKeptOpenCountsAsDone(t *testing.T) {
	r := BuildReview(reviewFixture())
	var b ReviewItem
	for _, it := range r.Items {
		if it.Ref.IID == 2 {
			b = it
		}
	}
	if !b.Done || !b.KeptOpen {
		t.Fatalf("workflow::done on an open issue: done=%v kept_open=%v", b.Done, b.KeptOpen)
	}
	if b.DoneAt == nil || !b.DoneAt.Equal(at("2026-11-09 15:00")) {
		t.Fatalf("done_at = %v, want the label's add time", b.DoneAt)
	}
	// Without a done label configured the same issue is still open work.
	in := reviewFixture()
	in.Manifest.Labels.Workflow.Done = ""
	if got := BuildReview(in).Done.Count; got != 2 {
		t.Fatalf("no done label configured: done = %d, want only the 2 closed", got)
	}
}

func TestReviewBurndown(t *testing.T) {
	r := BuildReview(reviewFixture())
	want := []struct {
		date string
		rem  int
	}{
		{"2026-11-02", 10},
		{"2026-11-03", 10},
		{"2026-11-04", 7},
		{"2026-11-05", 15},
		{"2026-11-06", 15},
		{"2026-11-09", 10},
		{"2026-11-10", 10},
		{"2026-11-11", 10},
	}
	if len(r.Burndown) != len(want) {
		t.Fatalf("burndown has %d points, want %d: %+v", len(r.Burndown), len(want), r.Burndown)
	}
	for i, w := range want {
		if r.Burndown[i].Date != w.date || r.Burndown[i].Remaining != w.rem {
			t.Errorf("day %d = %+v, want %s remaining %d", i, r.Burndown[i], w.date, w.rem)
		}
	}
	if got := r.Burndown[0].Ideal; got != 9 {
		t.Errorf("ideal after day 1 = %v, want 9 (10w over 10 working days)", got)
	}
}

func TestReviewAddedAtFollowsRemovalsAndReadds(t *testing.T) {
	in := reviewFixture()
	e := in.Issues[4]
	in.History[e.Ref] = issueHistory{Sprint: []forge.SprintEvent{
		sprintAdd("add", "2026-11-03 10:00"), sprintAdd("remove", "2026-11-04 10:00"), sprintAdd("add", "2026-11-06 10:00"),
		{Source: "milestones", ID: 99, Action: "add", At: at("2026-11-02 08:00")}, // another sprint
	}}
	for _, it := range BuildReview(in).Items {
		if it.Ref.IID == 5 && !it.AddedAt.Equal(at("2026-11-06 10:00")) {
			t.Fatalf("added_at = %v, want the last add after the removal", it.AddedAt)
		}
	}
}

func TestReviewFallsBackToCreationWithoutHistory(t *testing.T) {
	in := reviewFixture()
	in.History = nil
	r := BuildReview(in)
	if r.Added.Count != 0 || r.Committed.Count != 6 {
		t.Fatalf("no history: committed %d, added %d (issues predate the sprint)", r.Committed.Count, r.Added.Count)
	}
}

func TestReviewCountBasisWhenNothingIsWeighted(t *testing.T) {
	in := reviewFixture()
	for i := range in.Issues {
		in.Issues[i].Weight = nil
	}
	r := BuildReview(in)
	if r.Basis != BasisCount {
		t.Fatalf("basis = %s", r.Basis)
	}
	if r.Done.Weight != 0 || r.Done.Count != 3 || r.Done.Unweighted != 3 {
		t.Fatalf("done = %+v", r.Done)
	}
	if got := r.Burndown[0].Remaining; got != 5 { // A, B, C, D and F were all committed; none done yet
		t.Fatalf("count burndown day 1 = %d, want 5", got)
	}
	out := RenderReview(r, Text)
	if !strings.Contains(out, "by item count (nothing is weighted)") {
		t.Errorf("render must say it counts items:\n%s", out)
	}
}

func TestReviewMineAndEpics(t *testing.T) {
	in := reviewFixture()
	in.Issues[0].Epic = "Self-serve exports"
	in.Issues[1].Epic = "Self-serve exports"
	in.Issues[2].Assignees = []forge.User{rae}
	r := BuildReview(in)
	if len(r.Epics) != 2 || r.Epics[0].Epic != "Self-serve exports" || r.Epics[1].Epic != "" {
		t.Fatalf("epics = %+v, want the named epic then the epic-less rest", r.Epics)
	}
	g := r.Epics[0]
	if g.Total.Count != 2 || g.Total.Weight != 8 || g.Done.Count != 2 || g.Carried.Count != 0 {
		t.Fatalf("group = %+v", g)
	}
	in.Mine = true
	if got := BuildReview(in).Committed.Count + BuildReview(in).Added.Count; got != 5 {
		t.Fatalf("--mine kept %d items, want the 5 assigned to me", got)
	}
}

func pastSprint(title, start, due string, issues ...forge.Issue) PastSprint {
	s, _ := forge.ParseDate(start)
	d, _ := forge.ParseDate(due)
	return PastSprint{Sprint: domain.Sprint{Title: title, Start: s, Due: d}, Issues: issues}
}

func TestVelocityLeavesUnweightedSprintsOutOfTheAverage(t *testing.T) {
	m := manifest()
	v := velocity([]PastSprint{
		pastSprint("Sprint 41", "2026-10-19", "2026-10-30", iss(1, weight(4), "closed"), iss(2, weight(2), "opened"), iss(3, nil, "closed")),
		pastSprint("Sprint 40", "2026-10-05", "2026-10-16", iss(4, nil, "closed"), iss(5, nil, "closed")),
		pastSprint("Sprint 39", "2026-09-21", "2026-10-02", iss(6, weight(6), "closed")),
	}, m)
	if v.Basis != BasisWeight || len(v.Sprints) != 3 {
		t.Fatalf("basis %s over %d sprints", v.Basis, len(v.Sprints))
	}
	if v.Sprints[0].Title != "Sprint 39" || v.Sprints[2].Title != "Sprint 41" {
		t.Fatalf("sprints must be oldest first: %+v", v.Sprints)
	}
	if v.Sprints[1].Velocity != nil {
		t.Fatalf("a sprint with no weighted work must have no velocity, got %d", *v.Sprints[1].Velocity)
	}
	if v.Sprints[2].Velocity == nil || *v.Sprints[2].Velocity != 4 || v.Sprints[2].Done.Unweighted != 1 {
		t.Fatalf("sprint 41 = %+v", v.Sprints[2])
	}
	if v.Average == nil || *v.Average != 5 {
		t.Fatalf("average = %v, want (6+4)/2 = 5 — the unweighted sprint is not a zero", v.Average)
	}
	out := RenderVelocity(v, Text)
	if !strings.Contains(out, "Sprint 40: no weighted work") || !strings.Contains(out, "average: 5.0w") {
		t.Errorf("render:\n%s", out)
	}
}

func TestVelocityFallsBackToCountWhenNothingIsWeighted(t *testing.T) {
	v := velocity([]PastSprint{
		pastSprint("Sprint 41", "2026-10-19", "2026-10-30", iss(1, nil, "closed"), iss(2, nil, "opened")),
		pastSprint("Sprint 40", "2026-10-05", "2026-10-16", iss(3, nil, "closed"), iss(4, nil, "closed"), iss(5, nil, "closed")),
	}, manifest())
	if v.Basis != BasisCount || v.Average == nil || *v.Average != 2 {
		t.Fatalf("basis %s average %v, want count 2", v.Basis, v.Average)
	}
	for _, s := range v.Sprints {
		if s.Velocity != nil {
			t.Errorf("%s has a weight velocity with no weights", s.Title)
		}
	}
}

func TestVelocityWithNoClosedSprints(t *testing.T) {
	v := velocity(nil, manifest())
	if v.Average != nil || len(v.Sprints) != 0 {
		t.Fatalf("%+v", v)
	}
	if !strings.Contains(RenderVelocity(v, Markdown), "no closed sprints") {
		t.Error("render should say there is nothing to compare")
	}
}

func TestZeroWeightIsUnweighted(t *testing.T) {
	zero := 0
	in := reviewFixture()
	in.Issues = []forge.Issue{iss(1, &zero, "closed")}
	r := BuildReview(in)
	if r.Done.Weighted != 0 || r.Done.Unweighted != 1 || r.Basis != BasisCount {
		t.Fatalf("weight 0 is GitLab's 'unset': done=%+v basis=%s", r.Done, r.Basis)
	}
}

func TestRenderReviewMarkdownExport(t *testing.T) {
	out := RenderReview(BuildReview(reviewFixture()), Markdown)
	for _, want := range []string{
		"# Sprint review · Sprint 42 (2026-11-02 – 2026-11-13)",
		"- Committed: 10w · 5 items (2 unweighted)",
		"- Added mid-sprint: 8w · 1 item",
		"- Done: 8w · 3 items (1 unweighted)",
		"- Carried over: 10w · 3 items (1 unweighted)",
		"tracker#2 · done (kept open) · 5w",
		"tracker#5 · carried · 8w · issue E · added mid-sprint",
		"tracker#4 · carried · – · issue D",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown lacks %q:\n%s", want, out)
		}
	}
}

func TestPastSprintsPicksLastNEndedBefore(t *testing.T) {
	mk := func(title, s, d string) domain.Sprint { return pastSprint(title, s, d).Sprint }
	all := []domain.Sprint{
		mk("S38", "2026-09-07", "2026-09-18"), mk("S39", "2026-09-21", "2026-10-02"), mk("S40", "2026-10-05", "2026-10-16"),
		mk("S41", "2026-10-19", "2026-10-30"), sprint42, mk("S43", "2026-11-16", "2026-11-27"),
	}
	got := pastSprints(all, sprint42, 2)
	if len(got) != 2 || got[0].Title != "S40" || got[1].Title != "S41" {
		t.Fatalf("%+v", got)
	}
}
