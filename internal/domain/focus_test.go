package domain

import (
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

func attemptMR(iid int, state string) forge.MR {
	return forge.MR{Ref: forge.Ref{Project: "g/api", IID: iid, Kind: forge.KindMR}, State: state, SourceBranch: "7-attempt-" + string(rune('a'+iid))}
}

func TestFocusIsTheOnlyOpenMRAmongEarlierOnes(t *testing.T) {
	wi := WorkItem{MRs: []forge.MR{attemptMR(1, "merged"), attemptMR(2, "closed"), attemptMR(3, "opened")}}
	if f := wi.FocusMR(); f == nil || f.Ref.IID != 3 {
		t.Fatalf("focus = %+v, want !3", f)
	}
	if f := (&WorkItem{MRs: []forge.MR{attemptMR(1, "merged"), attemptMR(2, "closed")}}).FocusMR(); f != nil {
		t.Errorf("no open MR: focus = %+v, want nil", f)
	}
}

func TestFocusWithSeveralOpenIsTheLeastAdvanced(t *testing.T) {
	ready, draft := attemptMR(1, "opened"), attemptMR(2, "opened")
	draft.Draft = true
	wi := WorkItem{MRs: []forge.MR{ready, draft}}
	if f := wi.FocusMR(); f == nil || f.Ref.IID != 2 {
		t.Fatalf("focus = %+v, want the draft !2", f)
	}
}

func TestAssembleListsTheFocusMRFirst(t *testing.T) {
	old, older := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	m1, m2 := attemptMR(1, "merged"), attemptMR(2, "merged")
	m1.MergedAt, m2.MergedAt = &older, &old
	in := AssembleInput{
		Tracker: forge.Issue{Ref: forge.Ref{Project: "g/tracker", IID: 7}, State: "opened"},
		MRs:     []forge.MR{m1, attemptMR(3, "closed"), m2, attemptMR(4, "opened")},
	}
	got := Assemble(in).MRs
	var order []int
	for _, mr := range got {
		order = append(order, mr.Ref.IID)
	}
	want := []int{4, 2, 1, 3} // open focus, merged newest first, closed
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("MR order = %v, want %v", order, want)
		}
	}
	if s := Assemble(in).Stage; s != StageReview {
		t.Errorf("stage = %v, want review (the open MR decides)", s)
	}
}
