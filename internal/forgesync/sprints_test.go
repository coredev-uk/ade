package forgesync

import (
	"path/filepath"
	"testing"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/store"
)

// A recorded sprint move is what the next sync diffs against: it must not
// raise issue.iteration_changed for a move ADE already announced.
func TestRecordSprintMoveSilencesTheNextSyncsEvent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ref := forge.Ref{Project: "g/tracker", IID: 1}
	s42 := domain.Sprint{Key: "42", Title: "Sprint 42", Source: "milestones"}
	s43 := domain.Sprint{Key: "43", Title: "Sprint 43", Source: "milestones"}
	other := forge.Ref{Project: "g/tracker", IID: 2}
	res := Result{Project: "demo", Items: []domain.WorkItem{
		{Project: "demo", Ref: ref, Issue: forge.Issue{Ref: ref, Title: "Rate-limit"}, Sprint: &s42},
		{Project: "demo", Ref: other, Issue: forge.Issue{Ref: other}, Sprint: &s42},
	}}
	if err := saveResult(st, res); err != nil {
		t.Fatal(err)
	}
	if err := RecordSprintMove(st, "demo", ref, s43); err != nil {
		t.Fatal(err)
	}
	cached, err := Load(st)
	if err != nil || len(cached) != 1 {
		t.Fatalf("Load = %v, %v", cached, err)
	}
	moved, kept := cached[0].Items[0], cached[0].Items[1]
	if moved.Sprint == nil || moved.Sprint.Title != "Sprint 43" || kept.Sprint.Title != "Sprint 42" {
		t.Fatalf("sprints after the move: %v and %v", moved.Sprint, kept.Sprint)
	}

	// The forge now says Sprint 43: nothing changed since the cached state.
	fresh := Result{Project: "demo", Items: []domain.WorkItem{
		{Project: "demo", Ref: ref, Issue: forge.Issue{Ref: ref, Title: "Rate-limit"}, Sprint: &s43},
	}}
	for _, e := range diff(cached[0], fresh) {
		if e.Kind == EvIterationMoved {
			t.Errorf("sync raised %q again: %s", e.Kind, e.Title)
		}
	}
	// An item that is not cached is not an error.
	if err := RecordSprintMove(st, "demo", forge.Ref{Project: "g/tracker", IID: 99}, s43); err != nil {
		t.Errorf("uncached item: %v", err)
	}
}
