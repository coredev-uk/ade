package agile

import (
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

func TestLastDayIsTheLastDayYouPushedOrShippedNotTheCalendar(t *testing.T) {
	push := forge.Event{At: at("2026-11-04 17:00"), Action: "pushed to", Branch: "1-rate-limit", Author: me}
	in := StandupInput{
		Date: date("2026-11-10"), Cal: utcCal(), Me: []forge.User{me}, // a Tuesday; Monday had no commits or MRs
		Activity: []forge.Event{
			ev("2026-11-03 10:00", "opened", "MergeRequest", api1, me),
			push,
			ev("2026-11-04 11:00", "commented on", "Note", trk3, me),
			ev("2026-11-09 09:00", "commented on", "Note", trk1, me), // a comment alone does not make a working day
			ev("2026-11-09 10:00", "opened", "MergeRequest", api1, rae),
			ev("2026-11-10 08:00", "accepted", "MergeRequest", api1, me), // today: not the last day
		},
	}
	l := BuildLastDay(in)
	if l.Date != "2026-11-04" {
		t.Fatalf("Date = %q, want 2026-11-04 (the last push), not Monday", l.Date)
	}
	got := texts(l.Done)
	if len(got) != 2 || !strings.HasPrefix(got[0], "commented on tracker#3") || got[1] != "pushed 1-rate-limit" {
		t.Fatalf("Done = %q, want only that day's comment and push, in time order", got)
	}
	facts := LastDayFacts(l)
	if !strings.Contains(facts, "Wednesday 4 November 2026") || !strings.Contains(facts, "- 17:00 pushed 1-rate-limit") {
		t.Errorf("facts = %q, want the date and timed entries", facts)
	}
}

func TestLastDayCountsACachedMergeAndIsEmptyWithoutWork(t *testing.T) {
	merged := at("2026-11-06 15:00")
	in := StandupInput{
		Date: date("2026-11-09"), Cal: utcCal(), Me: []forge.User{me},
		Items: []domain.WorkItem{{MRs: []forge.MR{{Ref: api1, Title: "Rate limit", State: "merged", MergedAt: &merged, Author: me}}}},
	}
	if l := BuildLastDay(in); l.Date != "2026-11-06" || len(l.Done) != 1 {
		t.Fatalf("LastDay = %+v, want Friday from the cached merge", l)
	}
	in.Items = nil
	if l := BuildLastDay(in); l.Date != "" || l.Done == nil {
		t.Fatalf("LastDay = %+v, want no date and an empty (not nil) list", l)
	}
}

func TestReviewTimelineIsInDateOrder(t *testing.T) {
	d1, d2, d3 := at("2026-11-05 10:00"), at("2026-11-03 09:00"), at("2026-11-04 12:00")
	r := Review{Items: []ReviewItem{
		{Ref: trk3, Title: "Export", Done: true, DoneAt: &d1, MergedAt: &d3, MRs: []string{"api!1"}},
		{Ref: trk5, Title: "Login", Done: true, DoneAt: &d2},
		{Ref: trk10, Title: "Carried"},
	}}
	got := reviewTimeline(r)
	i2, i3, i1 := strings.Index(got, "Tue 3 Nov · tracker#5 done"), strings.Index(got, "Wed 4 Nov · tracker#3 merged (api!1)"), strings.Index(got, "Thu 5 Nov · tracker#3 done")
	if i2 < 0 || i3 < 0 || i1 < 0 || i2 > i3 || i3 > i1 || strings.Contains(got, "tracker#10") {
		t.Fatalf("timeline = %q, want dated entries earliest first and no unfinished item", got)
	}
	if reviewTimeline(Review{}) != "" {
		t.Error("an empty review adds no timeline")
	}
	_ = time.UTC
}
