package overview

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/store"
)

// cache is a temp cache.db with a project result written the way sync writes
// it; put replaces it.
type cache struct {
	t  *testing.T
	st *store.Store
}

func newCache(t *testing.T) *cache {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &cache{t: t, st: st}
}

func (c *cache) put(items ...domain.WorkItem) {
	c.t.Helper()
	res := forgesync.Result{Project: "demo", Forge: "gl", Tracker: "acme/tracker", SyncedAt: now.Add(-4 * time.Minute), Window: domain.Window{DaysLeft: 3}}
	state, _ := json.Marshal(res)
	tx, err := c.st.DB.Begin()
	if err != nil {
		c.t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(`DELETE FROM work_items WHERE project = 'demo'`); err != nil {
		c.t.Fatal(err)
	}
	for _, it := range items {
		b, _ := json.Marshal(it)
		if _, err := tx.Exec(`INSERT INTO work_items (project, ref, json) VALUES ('demo', ?, ?)`, it.Ref.String(), string(b)); err != nil {
			c.t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO sync_state (project, json) VALUES ('demo', ?) ON CONFLICT(project) DO UPDATE SET json = excluded.json`, string(state)); err != nil {
		c.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		c.t.Fatal(err)
	}
}

func TestFindAcceptsTrackerChildAndMRRefs(t *testing.T) {
	it := item()
	it.MRs = []forge.MR{{Ref: mrRef("acme/api", 5)}}
	other := item()
	other.Ref = ref("acme/tracker", 16)
	other.Children = nil
	results := []forgesync.Result{{Project: "demo", Items: []domain.WorkItem{other, it}}}
	for _, r := range []forge.Ref{ref("acme/tracker", 15), ref("acme/api", 3), mrRef("acme/api", 5)} {
		if _, got, ok := Find(results, r); !ok || got.Ref != it.Ref {
			t.Errorf("Find(%s) = %v, %v; want the item %s", r, got.Ref, ok, it.Ref)
		}
	}
	for _, r := range []forge.Ref{ref("acme/api", 5), mrRef("acme/api", 3), ref("acme/tracker", 99)} {
		if _, _, ok := Find(results, r); ok {
			t.Errorf("Find(%s) found an item", r)
		}
	}
}

func TestLoadReadsTheItemAndYourNotes(t *testing.T) {
	c := newCache(t)
	it := item()
	c.put(it)
	rs := records.New(t.TempDir())
	if _, _, err := rs.WriteSnapshot("gl", it, nil, 5); err != nil {
		t.Fatal(err)
	}
	if err := rs.WriteNotes("gl", it.Ref, "mine"); err != nil {
		t.Fatal(err)
	}
	l := &Loader{Cache: c.st, Records: rs, Ref: ref("acme/api", 3)} // a child ref finds the item
	snap, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Found || snap.Item.Ref != it.Ref || snap.Notes != "mine" || snap.Forge != "gl" || !snap.SyncedAt.Equal(now.Add(-4*time.Minute)) || snap.Window.DaysLeft != 3 {
		t.Errorf("snapshot = %+v", snap)
	}

	l.Ref = ref("acme/tracker", 99)
	if _, err := l.Load(); err == nil || err != ErrNotCached {
		t.Errorf("unknown ref: err = %v", err)
	}
}

func TestLoadWithoutARecordStillShowsTheItem(t *testing.T) {
	c := newCache(t)
	c.put(item())
	l := &Loader{Cache: c.st, Records: records.New(t.TempDir()), Ref: item().Ref}
	snap, err := l.Load()
	if err != nil || !snap.Found || snap.Notes != "" {
		t.Fatalf("snap = %+v, err %v", snap, err)
	}
}

func TestPollReloadsOnlyWhenTheCacheOrTheRecordChanged(t *testing.T) {
	c := newCache(t)
	it := item()
	c.put(it)
	rs := records.New(t.TempDir())
	if _, _, err := rs.WriteSnapshot("gl", it, nil, 5); err != nil {
		t.Fatal(err)
	}
	l := &Loader{Cache: c.st, Records: rs, Ref: it.Ref}

	poll := func() (Snapshot, bool) {
		t.Helper()
		s, reloaded, err := l.Poll()
		if err != nil {
			t.Fatal(err)
		}
		return s, reloaded
	}
	if s, reloaded := poll(); !reloaded || s.Item.Issue.Title != it.Issue.Title {
		t.Fatalf("first poll: reloaded %v, %q", reloaded, s.Item.Issue.Title)
	}
	for i := range 3 {
		if _, reloaded := poll(); reloaded {
			t.Fatalf("poll %d reloaded with nothing changed", i)
		}
	}

	// A sync rewrites the cache.
	it.Issue.Title = "Renamed by a sync"
	c.put(it)
	if s, reloaded := poll(); !reloaded || s.Item.Issue.Title != "Renamed by a sync" {
		t.Fatalf("after a sync: reloaded %v, %q", reloaded, s.Item.Issue.Title)
	}
	if _, reloaded := poll(); reloaded {
		t.Fatal("reloaded again with nothing changed")
	}

	// Notes are edited in the record file.
	if err := rs.WriteNotes("gl", it.Ref, "a new note"); err != nil {
		t.Fatal(err)
	}
	if s, reloaded := poll(); !reloaded || s.Notes != "a new note" {
		t.Fatalf("after a notes edit: reloaded %v, notes %q", reloaded, s.Notes)
	}
	if _, reloaded := poll(); reloaded {
		t.Fatal("reloaded again with nothing changed")
	}
	// An edit of the same size is seen by its modification time.
	if err := rs.WriteNotes("gl", it.Ref, "a NEW note"); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(rs.ItemPath("gl", it.Ref), later, later); err != nil {
		t.Fatal(err)
	}
	if s, reloaded := poll(); !reloaded || s.Notes != "a NEW note" {
		t.Fatalf("after a same-size edit: reloaded %v, notes %q", reloaded, s.Notes)
	}

	// The item leaves the cache: the poll says so (the view keeps its last picture).
	c.put()
	if _, reloaded, err := l.Poll(); !reloaded || !errors.Is(err, ErrNotCached) {
		t.Fatalf("item gone: reloaded %v, err = %v", reloaded, err)
	}
	if _, reloaded, _ := l.Poll(); reloaded {
		t.Fatal("reloaded while the cache stayed the same")
	}
}

// A sync writes while the pane reads: WAL readers are never blocked or failed
// by the writer (the writer here is a second connection, as `ade sync` is).
func TestLoadWhileASyncWrites(t *testing.T) {
	c := newCache(t)
	c.put(item())
	writer, err := store.Open(c.st.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	w := &cache{t: t, st: writer}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 60 {
			it := item()
			it.Issue.Title = fmt.Sprintf("title %d", i)
			w.put(it)
		}
	}()
	l := &Loader{Cache: c.st, Ref: item().Ref}
	reloads := 0
	for {
		snap, reloaded, err := l.Poll()
		if err != nil {
			t.Fatalf("poll during a sync: %v", err)
		}
		if reloaded {
			reloads++
			if !snap.Found {
				t.Fatal("the item vanished mid-sync")
			}
		}
		select {
		case <-done:
			if _, reloaded, err := l.Poll(); err != nil || (!reloaded && reloads == 0) {
				t.Fatalf("final poll: reloaded %v, err %v, reloads %d", reloaded, err, reloads)
			}
			s, err := l.Load()
			if err != nil || s.Item.Issue.Title != "title 59" {
				t.Fatalf("final title %q, err %v", s.Item.Issue.Title, err)
			}
			return
		default:
		}
	}
}
