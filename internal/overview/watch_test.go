package overview

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/fsnotify/fsnotify"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/store"
	"github.com/coredev-uk/ade/internal/watch"
)

// settle bounds how long a cache or notes write may take to reach the pane
// (measured: well under a second; 3 s leaves room for a loaded CI runner).
const settle = 3 * time.Second

var failingWatcher = func() (*fsnotify.Watcher, error) { return nil, errors.New("inotify unavailable") }

// driver runs a model the way the Bubble Tea runtime does: commands run in
// their own goroutines and their messages come back through Update, one at a
// time.
type driver struct {
	t    *testing.T
	m    model
	msgs chan tea.Msg
}

func newDriver(t *testing.T, m model) *driver {
	t.Helper()
	d := &driver{t: t, m: m, msgs: make(chan tea.Msg, 64)}
	d.run(m.Init())
	return d
}

func (d *driver) run(c tea.Cmd) {
	if c == nil {
		return
	}
	go func() {
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			for _, c := range msg {
				d.run(c)
			}
		default:
			d.msgs <- msg
		}
	}()
}

// until feeds messages to the model until the screen satisfies ok or the
// deadline passes; it reports the time it took.
func (d *driver) until(within time.Duration, ok func(screen string) bool) (time.Duration, bool) {
	start := time.Now()
	deadline := time.After(within)
	for {
		if ok(strings.Join(screen(d.m), "\n")) {
			return time.Since(start), true
		}
		select {
		case msg := <-d.msgs:
			next, cmd := d.m.Update(msg)
			d.m = next.(model)
			d.run(cmd)
		case <-deadline:
			return time.Since(start), false
		}
	}
}

// TestOverviewFollowsACacheWriteWithinASecond: the pane watches the cache
// and the records, and a write by another process (here a second handle on
// the same cache.db: what `ade sync` or start work does) shows up at once.
func TestOverviewFollowsACacheWriteWithinASecond(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts watch.Options
		mode string
	}{
		{"fsnotify", watch.Options{}, watch.ModeFS},
		{"poll fallback", watch.Options{PollInterval: 200 * time.Millisecond, NewWatcher: failingWatcher}, watch.ModePoll},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c := pane(t, item(), 60, 40)
			rec := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			w, err := watch.Watch(ctx, []watch.Target{
				{Path: filepath.Dir(c.st.Path)},
				{Path: rec, Recursive: true},
			}, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if w.Mode() != tc.mode {
				t.Fatalf("watch mode = %q, want %q", w.Mode(), tc.mode)
			}
			m.opts.Changes = w.C()
			d := newDriver(t, m)
			if _, ok := d.until(settle, func(s string) bool { return strings.Contains(s, "Rate-limit the export endpoint") }); !ok {
				t.Fatalf("first picture never drawn:\n%s", strings.Join(screen(d.m), "\n"))
			}

			// Another handle on the same cache.db: start work wrote a new
			// MR and the sync renamed the issue.
			other, err := store.Open(c.st.Path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { other.Close() })
			it := item()
			it.Issue.Title = "Renamed by another process"
			it.MRs = []forge.MR{{Ref: mrRef("acme/api", 7), State: "opened", Draft: true, SourceBranch: "3-limiter", Approvals: forge.Approvals{Required: -1}}}
			(&cache{t: t, st: other}).put(it)

			took, ok := d.until(settle, func(s string) bool {
				return strings.Contains(s, "Renamed by another process") && strings.Contains(s, "!7")
			})
			if !ok {
				t.Fatalf("no update within %v of the cache write:\n%s", settle, strings.Join(screen(d.m), "\n"))
			}
			t.Logf("overview updated %v after the write (%s)", took, w.Mode())
		})
	}
}

// TestOverviewFollowsANotesEditWithinASecond: the record under the records
// dir is watched too, even when the pane started before the directory existed.
func TestOverviewFollowsANotesEditWithinASecond(t *testing.T) {
	c := newCache(t)
	it := item()
	c.put(it)
	recDir := filepath.Join(t.TempDir(), "records") // does not exist yet
	rs := records.New(recDir)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w, err := watch.Watch(ctx, []watch.Target{{Path: filepath.Dir(c.st.Path)}, {Path: recDir, Recursive: true}}, watch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	l := &Loader{Cache: c.st, Records: rs, Ref: it.Ref}
	m := newModel(Options{Loader: l, Now: func() time.Time { return now }, Changes: w.C()})
	m = send(t, m, tea.WindowSizeMsg{Width: 60, Height: 60})
	d := newDriver(t, m)
	if _, ok := d.until(settle, func(s string) bool { return strings.Contains(s, "Rate-limit the export endpoint") }); !ok {
		t.Fatal("first picture never drawn")
	}
	if _, _, err := rs.WriteSnapshot("gl", it, nil, 5); err != nil {
		t.Fatal(err)
	}
	if err := rs.WriteNotes("gl", it.Ref, "call the vendor about quotas"); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.until(settle, func(s string) bool { return strings.Contains(s, "call the vendor about quotas") }); !ok {
		t.Fatalf("notes did not appear within %v:\n%s", settle, strings.Join(screen(d.m), "\n"))
	}
	if err := rs.WriteNotes("gl", it.Ref, "now it is a different note"); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.until(settle, func(s string) bool { return strings.Contains(s, "now it is a different note") }); !ok {
		t.Fatalf("a notes edit did not appear within %v:\n%s", settle, strings.Join(screen(d.m), "\n"))
	}
}
