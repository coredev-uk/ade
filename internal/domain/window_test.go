package domain

import (
	"reflect"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
)

func configWeight(src string) config.WeightSource { return config.WeightSource{Source: src} }

var monFri = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func sp(key, start, due string) Sprint {
	return Sprint{Key: key, Title: "Sprint " + key, Start: d(start), Due: d(due)}
}

func key(s *Sprint) string {
	if s == nil {
		return ""
	}
	return s.Key
}

var sandboxSprints = []Sprint{sp("41", "2026-10-19", "2026-10-30"), sp("42", "2026-11-02", "2026-11-13"), sp("43", "2026-11-16", "2026-11-27")}

func TestWindowAt(t *testing.T) {
	tests := []struct {
		name     string
		sprints  []Sprint
		now      string
		workdays []time.Weekday
		overlap  int
		cur      string
		prev     string
		next     string
		daysLeft int
	}{
		{"Wed 11 Nov", sandboxSprints, "2026-11-11 09:00", monFri, 1, "42", "", "", 3},
		{"Thu 12 Nov overlap 1", sandboxSprints, "2026-11-12 23:59", monFri, 1, "42", "", "", 2},
		{"Thu 12 Nov overlap 2", sandboxSprints, "2026-11-12 08:00", monFri, 2, "42", "", "43", 2},
		{"Fri 13 Nov last day", sandboxSprints, "2026-11-13 12:00", monFri, 1, "42", "", "43", 1},
		{"Mon 16 Nov first day of next", sandboxSprints, "2026-11-16 08:00", monFri, 1, "43", "42", "", 10},
		{"Tue 17 Nov past overlap", sandboxSprints, "2026-11-17 08:00", monFri, 1, "43", "", "", 9},
		{"Mon 2 Nov first day of 42", sandboxSprints, "2026-11-02 08:00", monFri, 1, "42", "41", "", 10},
		{"overlap 0 never shows neighbours", sandboxSprints, "2026-11-13 08:00", monFri, 0, "42", "", "", 1},
		{"Sat inside sprint", []Sprint{sp("42", "2026-11-02", "2026-11-13")}, "2026-11-07 10:00", monFri, 1, "42", "", "", 5},
		{"Sat inside sprint, early overlap", sandboxSprints, "2026-11-07 10:00", monFri, 5, "42", "41", "43", 5},
		{"weekend gap before next sprint", sandboxSprints, "2026-11-14 10:00", monFri, 1, "", "", "43", 0},
		{"no current: before all", sandboxSprints, "2026-09-01 10:00", monFri, 1, "", "", "41", 0},
		{"no current: after all", sandboxSprints, "2026-12-30 10:00", monFri, 1, "", "", "", 0},
		{"no sprints", nil, "2026-11-11 10:00", monFri, 1, "", "", "", 0},
		{"Tue–Mon cadence, due Monday", []Sprint{sp("a", "2026-11-03", "2026-11-16"), sp("b", "2026-11-17", "2026-11-30")}, "2026-11-16 10:00", monFri, 1, "a", "", "b", 1},
		{"Tue–Mon cadence, first day Tuesday", []Sprint{sp("a", "2026-11-03", "2026-11-16"), sp("b", "2026-11-17", "2026-11-30")}, "2026-11-17 10:00", monFri, 1, "b", "a", "", 10},
		{"Tue–Mon cadence, Fri before due", []Sprint{sp("a", "2026-11-03", "2026-11-16"), sp("b", "2026-11-17", "2026-11-30")}, "2026-11-13 10:00", monFri, 1, "a", "", "", 2},
		{"Tue–Mon cadence, Fri overlap 2", []Sprint{sp("a", "2026-11-03", "2026-11-16"), sp("b", "2026-11-17", "2026-11-30")}, "2026-11-13 10:00", monFri, 2, "a", "", "b", 2},
		{"1-week sprints", []Sprint{sp("a", "2026-11-09", "2026-11-13"), sp("b", "2026-11-16", "2026-11-20")}, "2026-11-12 10:00", monFri, 1, "a", "", "", 2},
		{"1-week, short overlap both ways", []Sprint{sp("a", "2026-11-09", "2026-11-13"), sp("b", "2026-11-16", "2026-11-20")}, "2026-11-16 10:00", monFri, 1, "b", "a", "", 5},
		{"3-week sprint", []Sprint{sp("a", "2026-11-02", "2026-11-20")}, "2026-11-11 10:00", monFri, 1, "a", "", "", 8},
		{"4-week sprint", []Sprint{sp("a", "2026-11-02", "2026-11-27")}, "2026-11-11 10:00", monFri, 1, "a", "", "", 13},
		{"single-day sprint", []Sprint{sp("a", "2026-11-11", "2026-11-11")}, "2026-11-11 10:00", monFri, 1, "a", "", "", 1},
		{"custom workdays Sun–Thu", []Sprint{sp("a", "2026-11-08", "2026-11-19")}, "2026-11-12 10:00", []time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday}, 1, "a", "", "", 6},
		{"default workdays when empty", sandboxSprints, "2026-11-11 10:00", nil, 1, "42", "", "", 3},
		{"unsorted input", []Sprint{sandboxSprints[2], sandboxSprints[0], sandboxSprints[1]}, "2026-11-13 10:00", monFri, 1, "42", "", "43", 1},
		{"inverted sprint ignored", []Sprint{sp("x", "2026-11-13", "2026-11-02")}, "2026-11-11 10:00", monFri, 1, "", "", "", 0},
		{"previous skips gaps", []Sprint{sp("a", "2026-10-05", "2026-10-16"), sp("b", "2026-11-02", "2026-11-13")}, "2026-11-02 10:00", monFri, 1, "b", "a", "", 10},
		{"time zone of now is by wall date", sandboxSprints, "2026-11-13 23:30", monFri, 1, "42", "", "43", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := WindowAt(tt.sprints, at(tt.now), tt.workdays, tt.overlap)
			if key(w.Current) != tt.cur || key(w.Previous) != tt.prev || key(w.Next) != tt.next || w.DaysLeft != tt.daysLeft {
				t.Errorf("cur/prev/next/left = %q/%q/%q/%d, want %q/%q/%q/%d",
					key(w.Current), key(w.Previous), key(w.Next), w.DaysLeft, tt.cur, tt.prev, tt.next, tt.daysLeft)
			}
		})
	}
}

func TestWindowAtDoesNotMutateInput(t *testing.T) {
	in := []Sprint{sandboxSprints[2], sandboxSprints[0], sandboxSprints[1]}
	WindowAt(in, at("2026-11-11 10:00"), monFri, 1)
	if in[0].Key != "43" || in[1].Key != "41" {
		t.Error("input order changed")
	}
}

func TestWindowAtLocalTime(t *testing.T) {
	loc := time.FixedZone("far", 14*3600)
	now := time.Date(2026, 11, 13, 1, 0, 0, 0, loc) // 12 Nov 11:00 UTC but 13 Nov locally
	w := WindowAt(sandboxSprints, now, monFri, 1)
	if w.DaysLeft != 1 || key(w.Next) != "43" {
		t.Errorf("DaysLeft=%d next=%q: want the caller's calendar date", w.DaysLeft, key(w.Next))
	}
}

func TestParseWorkdays(t *testing.T) {
	got, err := ParseWorkdays([]string{"mon", "Tue", "WEDNESDAY", " thu ", "fri", "sat", "sun"})
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	for _, bad := range []string{"", "funday", "mond", "mo"} {
		if _, err := ParseWorkdays([]string{"mon", bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got, err := ParseWorkdays(nil); err != nil || len(got) != 0 {
		t.Errorf("nil: %v %v", got, err)
	}
}

func TestSprintsFromMilestones(t *testing.T) {
	got := SprintsFromMilestones([]forge.Milestone{
		{ID: 3, Title: "Sprint 43", Start: d("2026-11-16"), Due: d("2026-11-27")},
		{ID: 9, Title: "Backlog"},
		{ID: 1, Title: "Sprint 41", Start: d("2026-10-19"), Due: d("2026-10-30"), State: "closed"},
		{ID: 4, Title: "Undated start", Due: d("2026-12-01")},
	})
	if len(got) != 2 || got[0].Title != "Sprint 41" || got[1].Key != "3" || got[0].Source != "milestones" {
		t.Errorf("got %+v", got)
	}
}

func TestSprintsFromIterations(t *testing.T) {
	its := []forge.Iteration{
		{ID: 2, Start: d("2026-11-17"), Due: d("2026-11-30")},
		{ID: 1, Title: "Named", Start: d("2026-11-03"), Due: d("2026-11-16")},
		{ID: 3, Start: d("2026-11-03"), Due: d("2026-11-14")},
		{ID: 4, Start: d("2026-12-01")},
	}
	got := SprintsFromIterations(its, "")
	var titles []string
	for _, s := range got {
		titles = append(titles, s.Title)
	}
	want := []string{"Named", "Iteration · 3–14 Nov", "Iteration · 17–30 Nov"}
	if len(got) != 3 || got[0].Title != want[0] || got[1].Title != want[1] || got[2].Title != want[2] {
		t.Errorf("titles = %v, want %v", titles, want)
	}

	got = SprintsFromIterations([]forge.Iteration{{ID: 5, Start: d("2026-11-28"), Due: d("2026-12-09")}}, "Platform")
	if got[0].Title != "Platform · 28 Nov – 9 Dec" {
		t.Errorf("cross-month title = %q", got[0].Title)
	}
	got = SprintsFromIterations([]forge.Iteration{{ID: 6, Cadence: "Own", Start: d("2026-11-03"), Due: d("2026-11-03")}}, "")
	if got[0].Title != "Own · 3 Nov" {
		t.Errorf("single day / own cadence title = %q", got[0].Title)
	}
}

func TestSprintsFromLabels(t *testing.T) {
	start := d("2026-11-02")
	got := SprintsFromLabels("Sprint::{n}", start, 14*24*time.Hour, []string{"Sprint::2", "bug", "Sprint::1", "sprint::2", "Sprint::x", "Sprint::0"})
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Key != "Sprint::1" || got[0].Start.String() != "2026-11-02" || got[0].Due.String() != "2026-11-15" {
		t.Errorf("sprint 1 = %+v", got[0])
	}
	if got[1].Start.String() != "2026-11-16" || got[1].Due.String() != "2026-11-29" {
		t.Errorf("sprint 2 = %+v", got[1])
	}
	if SprintsFromLabels("Sprint::{n}", start, time.Hour, []string{"Sprint::1"}) != nil {
		t.Error("sub-day length yields sprints")
	}
}

func TestSprintOf(t *testing.T) {
	ms := SprintsFromMilestones([]forge.Milestone{
		{ID: 2, Title: "Sprint 42", Start: d("2026-11-02"), Due: d("2026-11-13")},
	})
	its := SprintsFromIterations([]forge.Iteration{{ID: 7, Title: "It 7", Start: d("2026-11-03"), Due: d("2026-11-16")}}, "")
	lbl := SprintsFromLabels("Sprint::{n}", d("2026-11-02"), 14*24*time.Hour, []string{"Sprint::1", "Sprint::2"})
	all := append(append(append([]Sprint{}, ms...), its...), lbl...)

	tests := []struct {
		name  string
		issue forge.Issue
		src   config.IterationsSource
		want  string // Title
	}{
		{"iteration", forge.Issue{Iteration: &forge.Iteration{ID: 7}}, config.IterationsSource{Source: "gitlab-iterations"}, "It 7"},
		{"iteration not listed: built from issue", forge.Issue{Iteration: &forge.Iteration{ID: 8, Title: "Other", Start: d("2026-12-01"), Due: d("2026-12-14")}}, config.IterationsSource{Source: "gitlab-iterations"}, "Other"},
		{"iteration undated and unknown", forge.Issue{Iteration: &forge.Iteration{ID: 8}}, config.IterationsSource{Source: "gitlab-iterations"}, ""},
		{"milestone by title", forge.Issue{Milestone: &forge.Milestone{Title: "Sprint 42"}}, config.IterationsSource{Source: "milestones"}, "Sprint 42"},
		{"milestone built from issue", forge.Issue{Milestone: &forge.Milestone{ID: 5, Title: "Sprint 44", Start: d("2026-11-30"), Due: d("2026-12-11")}}, config.IterationsSource{Source: "milestones"}, "Sprint 44"},
		{"milestone undated unknown", forge.Issue{Milestone: &forge.Milestone{Title: "Backlog"}}, config.IterationsSource{Source: "milestones"}, ""},
		{"newest sprint label", forge.Issue{Labels: []string{"Sprint::1", "Sprint::2", "bug"}}, config.IterationsSource{Source: "labels", Pattern: "Sprint::{n}"}, "Sprint::2"},
		{"label source ignores milestone", forge.Issue{Milestone: &forge.Milestone{Title: "Sprint 42"}}, config.IterationsSource{Source: "labels", Pattern: "Sprint::{n}"}, ""},
		{"label not a known sprint", forge.Issue{Labels: []string{"Sprint::9"}}, config.IterationsSource{Source: "labels", Pattern: "Sprint::{n}"}, ""},
		{"auto prefers iteration", forge.Issue{Iteration: &forge.Iteration{ID: 7}, Milestone: &forge.Milestone{Title: "Sprint 42"}}, config.IterationsSource{Source: "auto"}, "It 7"},
		{"auto falls to milestone", forge.Issue{Milestone: &forge.Milestone{Title: "Sprint 42"}, Labels: []string{"Sprint::1"}}, config.IterationsSource{}, "Sprint 42"},
		{"auto falls to label", forge.Issue{Labels: []string{"Sprint::1"}}, config.IterationsSource{Pattern: "Sprint::{n}"}, "Sprint::1"},
		{"nothing", forge.Issue{}, config.IterationsSource{Source: "auto"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SprintOf(tt.issue, tt.src, all)
			title := ""
			if got != nil {
				title = got.Title
			}
			if title != tt.want {
				t.Errorf("got %q, want %q", title, tt.want)
			}
		})
	}
}
