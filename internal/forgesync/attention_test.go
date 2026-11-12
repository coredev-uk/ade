package forgesync

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/store"
)

// CountAttention must agree with what Home counts from the decoded items.
func TestCountAttentionMatchesTheCachedItems(t *testing.T) {
	h := newHarness(t, manifest("a", "alpha", "tracker", "milestones"), manifest("b", "beta", "tracker", "milestones"))
	seed(h.f)
	h.sync()
	st, err := h.app.Cache()
	if err != nil {
		t.Fatal(err)
	}
	cached, err := Load(st)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{}
	reviews := map[string]int{}
	for _, r := range cached {
		reviews[r.Project] = len(r.Reviews)
		for _, wi := range r.Items {
			if wi.Assigned && wi.Court == domain.CourtYou && wi.Stage != domain.StageDone {
				want[r.Project]++
			}
		}
	}
	if want["alpha"]+want["beta"] == 0 {
		t.Fatal("the seed has no item waiting on you: the test would prove nothing")
	}
	got, err := CountAttention(st.DB, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Items != want["alpha"]+want["beta"] || got.Reviews != reviews["alpha"]+reviews["beta"] || got.Total != got.Items+got.Reviews {
		t.Errorf("CountAttention = %+v, want items %d reviews %d", got, want["alpha"]+want["beta"], reviews["alpha"]+reviews["beta"])
	}
	if got.SyncedAt.IsZero() {
		t.Error("SyncedAt is empty after a sync")
	}
	one, err := CountAttention(st.DB, "alpha")
	if err != nil || one.Items != want["alpha"] {
		t.Errorf("alpha = %+v (%v), want %d items", one, err, want["alpha"])
	}

	// A one-item refresh replaces the row; the count follows without a rebuild.
	for _, r := range cached {
		if r.Project != "alpha" || len(r.Items) == 0 {
			continue
		}
		wi := r.Items[0]
		wi.Court, wi.Assigned = domain.CourtYou, true
		wi.Stage = domain.StageStarted
		wasCounted := r.Items[0].Assigned && r.Items[0].Court == domain.CourtYou && r.Items[0].Stage != domain.StageDone
		if err := saveItem(st, wi); err != nil {
			t.Fatal(err)
		}
		after, _ := CountAttention(st.DB, "alpha")
		wantAfter := want["alpha"]
		if !wasCounted {
			wantAfter++
		}
		if after.Items != wantAfter {
			t.Errorf("after replacing a row: %d items, want %d", after.Items, wantAfter)
		}
		break
	}
}

// A cache written before the attention column existed gets it, filled, when
// a newer ade opens it.
func TestAttentionMigratesAnExistingCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP INDEX work_items_attention`,
		`ALTER TABLE work_items DROP COLUMN attention`,
		`DELETE FROM migrations WHERE name LIKE 'forgesync.work_items_attention%'`,
	} {
		if _, err := st.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	r := forge.Ref{Project: "g/tracker", IID: 1, Kind: forge.KindIssue}
	b, _ := json.Marshal(domain.WorkItem{Project: "alpha", Ref: r, Court: domain.CourtYou, Stage: domain.StageReview, Assigned: true})
	if _, err := st.DB.Exec(`INSERT INTO work_items (project, ref, json) VALUES ('alpha', ?, ?)`, r.String(), string(b)); err != nil {
		t.Fatal(err)
	}
	if _, err := CountAttention(st.DB, ""); err == nil {
		t.Fatal("an unmigrated cache answered")
	}
	st.Close()

	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if a, err := CountAttention(st.DB, ""); err != nil || a.Items != 1 {
		t.Errorf("after migrating: %+v, %v", a, err)
	}
}
