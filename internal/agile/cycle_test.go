package agile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

func sys(body, when string) forge.Discussion {
	return forge.Discussion{Notes: []forge.Note{{System: true, Body: body, CreatedAt: at(when)}}}
}

func TestAssignedAtReadsTheSystemNote(t *testing.T) {
	other := forge.User{Username: "sam"}
	ds := []forge.Discussion{
		{Notes: []forge.Note{{System: false, Body: "assigned to @" + me.Username, CreatedAt: at("2026-11-01 08:00")}}}, // a human typing it is not an assignment
		sys("added ~type::story label", "2026-11-01 09:00"),
		sys("assigned to @sam", "2026-11-02 09:00"),
		sys("assigned to @sam and @"+me.Username, "2026-11-03 10:00"),
		sys("assigned to @"+me.Username, "2026-11-04 11:00"),
	}
	if got := assignedAt(ds, other, false); got == nil || !got.Equal(at("2026-11-02 09:00")) {
		t.Errorf("anyone: %v", got)
	}
	if got := assignedAt(ds, me, true); got == nil || !got.Equal(at("2026-11-03 10:00")) {
		t.Errorf("mine must skip assignments to others and find the first naming me: %v", got)
	}
	three := []forge.Discussion{sys("assigned to @a, @b and @"+me.Username, "2026-11-05 12:00")}
	if got := assignedAt(three, me, true); got == nil || !got.Equal(at("2026-11-05 12:00")) {
		t.Errorf("three assignees: %v", got)
	}
	if got := assignedAt(ds[:2], me, false); got != nil {
		t.Errorf("no assignment note: %v", got)
	}
	if got := assignedAt([]forge.Discussion{sys("unassigned @"+me.Username, "2026-11-05 12:00")}, me, false); got != nil {
		t.Errorf("an unassignment is not an assignment: %v", got)
	}
}

func cycleFixture() ReviewInput {
	in := reviewFixture()
	in.Merged = map[forge.Ref][]string{
		in.Issues[0].Ref: {"api!1"}, in.Issues[1].Ref: {"api!2"}, in.Issues[2].Ref: {"api!3"}, in.Issues[5].Ref: {"api!4"},
	}
	in.MergedAt = map[forge.Ref]time.Time{
		in.Issues[0].Ref: at("2026-11-05 10:00"), // assigned 11-03 09:00 by note: 49h
		in.Issues[1].Ref: at("2026-11-09 12:00"), // no note: created 10-20 09:00 → 20d 3h
		in.Issues[2].Ref: at("2026-11-02 08:00"), // merged before it was assigned: no cycle time
		in.Issues[5].Ref: at("2026-11-04 09:00"), // assigned 11-03 09:00: 24h
	}
	in.Assigned = map[forge.Ref]time.Time{
		in.Issues[0].Ref: at("2026-11-03 09:00"),
		in.Issues[2].Ref: at("2026-11-03 09:00"),
		in.Issues[5].Ref: at("2026-11-03 09:00"),
	}
	return in
}

func TestReviewCycleTimePerItemAndMedian(t *testing.T) {
	r := BuildReview(cycleFixture())
	byIID := map[int]ReviewItem{}
	for _, it := range r.Items {
		byIID[it.Ref.IID] = it
	}
	hours := func(iid int) float64 {
		t.Helper()
		if byIID[iid].CycleHours == nil {
			t.Fatalf("#%d has no cycle time: %+v", iid, byIID[iid])
		}
		return *byIID[iid].CycleHours
	}
	if h := hours(1); h != 49 {
		t.Errorf("#1 cycle = %v, want 49h", h)
	}
	if h := hours(6); h != 24 {
		t.Errorf("#6 cycle = %v, want 24h", h)
	}
	if byIID[1].AssignedFrom != AssignedFromNote {
		t.Errorf("#1 assigned from %q", byIID[1].AssignedFrom)
	}
	if byIID[2].AssignedFrom != AssignedFromCreated || hours(2) != 20*24+3 {
		t.Errorf("#2 without a note falls back to creation, said so: %+v", byIID[2])
	}
	if byIID[3].CycleHours != nil {
		t.Errorf("a merge before the assignment is no cycle time, not a negative one: %v", *byIID[3].CycleHours)
	}
	if byIID[4].CycleHours != nil || byIID[4].MergedAt != nil || byIID[5].CycleHours != nil {
		t.Errorf("items without a merged MR have no cycle time: %+v %+v", byIID[4], byIID[5])
	}
	// Median of 24h, 49h and 483h is 49h; three items, the unmeasured left out.
	if r.Cycle.Items != 3 || r.Cycle.MedianHours == nil || *r.Cycle.MedianHours != 49 {
		t.Errorf("summary %+v", r.Cycle)
	}
}

func TestMedianEvenCountAveragesTheMiddlePair(t *testing.T) {
	if m := median([]float64{10, 2, 4, 30}); m == nil || *m != 7 {
		t.Errorf("median = %v", m)
	}
	if median(nil) != nil {
		t.Error("no values: no median, not zero")
	}
}

func TestFormatCycle(t *testing.T) {
	for in, want := range map[float64]string{0.25: "15m", 5: "5h", 47.6: "48h", 49: "2d 1h", 72: "3d", 483: "20d 3h"} {
		if got := FormatCycle(in); got != want {
			t.Errorf("FormatCycle(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderShowsCycleTimePerItemAndMedian(t *testing.T) {
	r := BuildReview(cycleFixture())
	md := RenderReview(r, Markdown)
	for _, want := range []string{
		"Cycle time (assigned → merged): median 2d 1h over 3 items",
		"merged api!1 · cycle 2d 1h",
		"· cycle 20d 3h (from creation)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("review lacks %q:\n%s", want, md)
		}
	}
	empty := RenderReview(BuildReview(reviewFixture()), Text)
	if !strings.Contains(empty, "Cycle time (assigned → merged): no merged item to measure") {
		t.Errorf("no merged items:\n%s", empty)
	}
}

type fakeParents struct {
	calls  []string
	failOn string
}

func (f *fakeParents) NarrateParent(_ context.Context, r Review, it ReviewItem) (string, error) {
	f.calls = append(f.calls, it.Ref.String())
	if it.Ref.String() == f.failOn {
		return "", errors.New("budget stop")
	}
	if !strings.Contains(parentFacts(r, it), it.Title) {
		return "", errors.New("facts lack the item")
	}
	return "  Delivered " + it.Title + ".\n", nil
}

func TestParentNarrativesOnlyForDoneParentsWithMergedMRsAndOnlyInMarkdown(t *testing.T) {
	r := BuildReview(cycleFixture())
	n := &fakeParents{}
	got, count, err := WithParentNarratives(context.Background(), n, r)
	if err != nil {
		t.Fatal(err)
	}
	// #1 and #6 are closed with merged MRs; #2 is done by label with one; #3 has an MR but is not done.
	if count != 3 || len(n.calls) != 3 {
		t.Fatalf("narrated %d (%v)", count, n.calls)
	}
	for _, it := range got.Items {
		if has := it.Narrative != ""; has != ParentNarrated(it) {
			t.Errorf("%s narrative=%v narrated=%v", it.Ref, has, ParentNarrated(it))
		}
	}
	for _, it := range r.Items {
		if it.Narrative != "" {
			t.Fatal("the input review was mutated")
		}
	}
	md := RenderReview(got, Markdown)
	if !strings.Contains(md, "\n  Delivered issue A.\n") {
		t.Errorf("markdown lacks the paragraph under its item:\n%s", md)
	}
	if text := RenderReview(got, Text); strings.Contains(text, "Delivered issue A") {
		t.Errorf("the per-parent narrative is a Markdown export feature:\n%s", text)
	}
}

func TestParentNarrativesStopAtTheFirstFailure(t *testing.T) {
	r := BuildReview(cycleFixture())
	n := &fakeParents{failOn: "g/tracker#2"}
	got, count, err := WithParentNarratives(context.Background(), n, r)
	if err == nil || !strings.Contains(err.Error(), "tracker#2") || !strings.Contains(err.Error(), "budget stop") {
		t.Fatalf("err = %v", err)
	}
	if count != 1 || len(n.calls) != 2 {
		t.Fatalf("count %d calls %v: a refused call must end the run", count, n.calls)
	}
	if got.Items[0].Narrative == "" {
		t.Error("what was written before the failure is kept")
	}
	if _, c, err := WithParentNarratives(context.Background(), nil, r); c != 0 || err != nil {
		t.Errorf("no narrator: %d %v", c, err)
	}
}
