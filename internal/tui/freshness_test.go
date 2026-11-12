package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forgesync"
)

// A dialog never waits for a sync: while one runs against a slow forge, the
// classify and weight dialogs still open in well under a second, because they
// read their one issue (with identity and capabilities from the cache) on
// their own client and the history from the cache.
func TestDialogsOpenWhileASlowSyncRuns(t *testing.T) {
	e := newClassifyEnv(t)
	// An earlier sync of the session has cached identity and capabilities.
	if _, err := e.m.sync.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.srv.SetLatency(200 * time.Millisecond)

	syncDone := make(chan struct{})
	go func() {
		defer close(syncDone)
		_, _ = e.m.sync.Sync(context.Background())
	}()
	time.Sleep(100 * time.Millisecond) // the sync is under way
	select {
	case <-syncDone:
		t.Fatal("the sync finished at once: not slow enough to prove anything")
	default:
	}

	start := time.Now()
	e.press("c")
	took := time.Since(start)
	if e.m.cl == nil || e.m.cl.stage != clSuggest {
		t.Fatalf("classify did not open: %+v", e.m.cl)
	}
	if took > time.Second {
		t.Errorf("classify took %v to open beside a running sync, want < 1s", took)
	}
	e.press("esc")

	start = time.Now()
	e.press("=")
	took = time.Since(start)
	if e.m.wt == nil || e.m.wt.stage != wtInput {
		t.Fatalf("weight did not open: %+v", e.m.wt)
	}
	if took > time.Second {
		t.Errorf("weight took %v to open beside a running sync, want < 1s", took)
	}

	select {
	case <-syncDone:
		t.Error("the sync was done before the dialogs: they did not overlap it")
	default:
	}
	<-syncDone
}

// After a dialog writes to an issue, Home reads that one issue again: no full
// sync is started, and the cached item carries what was written.
func TestClassifyApplyRefreshesOnlyItsItem(t *testing.T) {
	e := newClassifyEnv(t)
	if _, err := e.m.sync.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.press("c")
	if e.m.cl == nil || e.m.cl.stage != clSuggest {
		t.Fatalf("classify did not open: %+v", e.m.cl)
	}
	before := len(e.srv.Requests())
	next, cmd := e.m.Update(keyMsg("enter")) // applies the ticked label
	e.m = next.(model)
	next, cmd = e.m.Update(cmd()) // clAppliedMsg: the refresh starts
	e.m = next.(model)
	if e.m.syncing {
		t.Error("applying labels started a full sync")
	}
	msg, ok := cmd().(resultsMsg)
	if !ok {
		t.Fatalf("the refresh answered %T, want the cache's results", msg)
	}
	if n := len(e.srv.Requests()) - before; n > 10 {
		t.Errorf("applying labels cost %d requests: that is a sync, not one item", n)
	}
	var found bool
	for _, r := range msg.results {
		for _, it := range r.Items {
			if it.Ref == trk(11) {
				found = true
				if !strings.Contains(strings.Join(it.Issue.Labels, ","), "type::bug") {
					t.Errorf("the refreshed item lacks the label that was just added: %v", it.Issue.Labels)
				}
			}
		}
	}
	if !found {
		t.Error("tracker#11 is not in the refreshed results")
	}
}

// Start work leaves the new child and MR in the cache without a sync: a
// dashboard (or `ade item --watch`) reading the cache shows them at once.
func TestStartWorkWritesTheItemToTheCache(t *testing.T) {
	e := newSWEnv(t)
	if _, err := e.m.sync.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	cachedItem := func() (children, mrs int) {
		rs, err := forgesync.Load(e.m.cache)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rs {
			for _, it := range r.Items {
				if it.Ref == trk(3) {
					return len(it.Children), len(it.MRs)
				}
			}
		}
		t.Fatalf("%s is not cached", trk(3))
		return 0, 0
	}
	c0, m0 := cachedItem()

	e.selectRow(trk(3).String())
	e.press("s", "enter", "enter")
	if e.m.sw == nil || e.m.sw.stage != swDone || e.m.sw.err != nil {
		t.Fatalf("start work did not finish: %+v", e.m.sw)
	}
	if e.m.syncing {
		t.Error("start work started a full sync")
	}
	c1, m1 := cachedItem()
	if c1 != c0+1 || m1 != m0+1 {
		t.Errorf("cached children/MRs %d/%d → %d/%d, want one more of each", c0, m0, c1, m1)
	}
}
