package tui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/store"
	"github.com/coredev-uk/ade/internal/watch"
)

// settle bounds how long a file change may take to reach the screen. The
// polls these watchers replaced ran every 2 s and 30 s; 3 s still rules the
// 30 s one out and leaves room for a loaded CI runner (measured: ~150 ms).
const settle = 3 * time.Second

var failingWatcher = func() (*fsnotify.Watcher, error) { return nil, errors.New("inotify unavailable") }

// watchModes are the two ways the dashboard learns of a file change.
var watchModes = []struct {
	name string
	opts watch.Options
	mode string
}{
	{"fsnotify", watch.Options{}, watch.ModeFS},
	{"poll fallback", watch.Options{PollInterval: 200 * time.Millisecond, NewWatcher: failingWatcher}, watch.ModePoll},
}

// loop runs a model the way the Bubble Tea runtime does: commands run in
// their own goroutines and their messages come back through Update, one at a
// time. Only what the test starts runs (not Init: no git, no forge).
type loop struct {
	m    model
	msgs chan tea.Msg
}

func newLoop(m model, start tea.Cmd) *loop {
	l := &loop{m: m, msgs: make(chan tea.Msg, 64)}
	l.run(start)
	return l
}

func (l *loop) run(c tea.Cmd) {
	if c == nil {
		return
	}
	go func() {
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			for _, c := range msg {
				l.run(c)
			}
		default:
			l.msgs <- msg
		}
	}()
}

// until applies messages until ok holds of the model or the deadline passes,
// and reports how long it took.
func (l *loop) until(within time.Duration, ok func(model) bool) (time.Duration, bool) {
	start := time.Now()
	deadline := time.After(within)
	for {
		if ok(l.m) {
			return time.Since(start), true
		}
		select {
		case msg := <-l.msgs:
			if _, tick := msg.(tickMsg); tick {
				continue
			}
			next, cmd := l.m.Update(msg)
			l.m = next.(model)
			l.run(cmd)
		case <-deadline:
			return time.Since(start), false
		}
	}
}

func watchedApp(t *testing.T) (*app.App, *store.Store) {
	t.Helper()
	cfg := config.Defaults()
	rec := t.TempDir()
	al, err := audit.Open(rec, "user")
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{Paths: paths.Paths{CacheDir: t.TempDir()}, Config: &cfg, RecordsDir: rec, Audit: al, Bus: events.NewBus()}
	t.Cleanup(func() { a.Close() })
	st, err := a.Cache()
	if err != nil {
		t.Fatal(err)
	}
	return a, st
}

func startWatch(t *testing.T, a *app.App, st *store.Store, o watch.Options) *fileWatch {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fw, err := newFileWatch(ctx, a, st.Path, o)
	if err != nil {
		t.Fatal(err)
	}
	return fw
}

// A write to cache.db by another process (another handle on the same file:
// `ade notify send`, `ade sync`, start work) reaches the dashboard within a
// second, with no polling: the inbox, the toast and Home's work items.
func TestCacheWriteByAnotherHandleUpdatesTheDashboardWithinASecond(t *testing.T) {
	for _, tc := range watchModes {
		t.Run(tc.name, func(t *testing.T) {
			a, st := watchedApp(t)
			m := model{ctx: context.Background(), app: a, cache: st, g: unicodeGlyphs, w: 160, h: 40, nc: newNotifCenter(notify.NewInbox(st)), page: pageHome}
			m.fw = startWatch(t, a, st, tc.opts)
			if got := m.fw.cache.Mode(); got != tc.mode {
				t.Fatalf("watch mode = %q, want %q", got, tc.mode)
			}
			l := newLoop(m, tea.Batch(m.nc.poll(), m.loadResults(), m.watchStart()))
			if _, ok := l.until(settle, func(m model) bool { return m.nc.primed && m.homeLoaded }); !ok {
				t.Fatal("first read of the cache never arrived")
			}

			// The other process.
			other, err := store.Open(st.Path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { other.Close() })
			e := events.New("ci.failed", events.Error, "web!1 pipeline failed: lint")
			if err := notify.NewInbox(other).Record(e); err != nil {
				t.Fatal(err)
			}
			took, ok := l.until(settle, func(m model) bool {
				return len(m.nc.items) == 1 && m.nc.unread == 1 && len(m.nc.toasts) == 1
			})
			if !ok {
				t.Fatalf("inbox row not seen within %v: items %d unread %d toasts %d", settle, len(l.m.nc.items), l.m.nc.unread, len(l.m.nc.toasts))
			}
			t.Logf("inbox row seen %v after the write (%s)", took, tc.mode)

			res := sampleResult()
			putResult(t, other, res)
			if _, ok := l.until(settle, func(m model) bool { return len(m.homeRows) > 0 }); !ok {
				t.Fatalf("work items not seen within %v of the cache write", settle)
			}
			if out := ansi.Strip(l.m.render()); !strings.Contains(out, "Rate-limit the export") {
				t.Errorf("Home does not show the written work item:\n%s", out)
			}
		})
	}
}

// putResult writes a sync result the way a sync does.
func putResult(t *testing.T, st *store.Store, r forgesync.Result) {
	t.Helper()
	items := r.Items
	r.Items = nil
	state, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	for _, it := range items {
		b, _ := json.Marshal(it)
		if _, err := tx.Exec(`INSERT INTO work_items (project, ref, json) VALUES (?, ?, ?)`, r.Project, it.Ref.String(), string(b)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO sync_state (project, json) VALUES (?, ?)`, r.Project, string(state)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// The header's AI figure follows the audit log: an AI call recorded by this
// process or by another one moves it at once, without the 30 s poll it used.
func TestAIChipUpdatesRightAfterACall(t *testing.T) {
	for _, tc := range watchModes {
		t.Run(tc.name, func(t *testing.T) {
			a, st := watchedApp(t)
			a.Config.AI.Profile = "claude-sub"
			a.Config.AI.Profiles = map[string]config.AIProfile{"claude-sub": {
				Driver: "omp", Models: map[string]string{"explain": "haiku-x"},
				Billing: config.AIBilling{Source: "omp", WeeklySoftUSD: 5},
			}}
			m := model{ctx: context.Background(), app: a, cache: st, g: unicodeGlyphs, w: 160, h: 40, nc: newNotifCenter(notify.NewInbox(st))}
			m.fw = startWatch(t, a, st, tc.opts)
			if got := m.fw.audit.Mode(); got != tc.mode {
				t.Fatalf("watch mode = %q, want %q", got, tc.mode)
			}
			l := newLoop(m, tea.Batch(m.loadAIChip(), m.watchStart()))
			if _, ok := l.until(settle, func(m model) bool { return m.chip.ok }); !ok {
				t.Fatal("the chip never loaded")
			}
			if l.m.chip.spent != 0 {
				t.Fatalf("chip before any call: %+v", l.m.chip)
			}

			call := func(al *audit.Log, cost float64) {
				t.Helper()
				err := al.Append(audit.Entry{
					TS: time.Now(), Action: "ai.summarize", Item: "g/tracker#1",
					AI: &audit.AIInfo{Profile: "claude-sub", Driver: "omp", Model: "haiku-x", TokensIn: 1000, TokensOut: 100, CostUSD: cost, CostSource: "reported"},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			spent := func(want float64) func(model) bool {
				return func(m model) bool { return m.chip.spent > want-1e-9 && m.chip.spent < want+1e-9 }
			}

			call(a.Audit, 0.25) // this process
			took, ok := l.until(settle, spent(0.25))
			if !ok {
				t.Fatalf("chip after a call = %+v, want $0.25 within %v", l.m.chip, settle)
			}
			t.Logf("chip moved %v after the call (%s)", took, tc.mode)

			other := &audit.Log{Dir: a.Audit.Dir, Actor: "user"} // another ade process
			call(other, 0.50)
			if _, ok := l.until(settle, spent(0.75)); !ok {
				t.Fatalf("chip after another process's call = %+v, want $0.75 within %v", l.m.chip, settle)
			}
			if got := ansi.Strip(l.m.chip.render(unicodeGlyphs)); !strings.Contains(got, "$0.75") {
				t.Errorf("rendered chip = %q", got)
			}
		})
	}
}

// The "day" figure starts over at the end of the local day without any write.
func TestUntilNextDayLandsJustPastLocalMidnight(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*3600)
	now := time.Date(2026, 11, 12, 23, 30, 0, 0, loc)
	if got := untilNextDay(now, loc); got != 30*time.Minute+time.Second {
		t.Errorf("untilNextDay = %v, want 30m1s", got)
	}
	// The same instant in the user's zone, whatever zone the clock is in.
	if got := untilNextDay(now.UTC(), loc); got != 30*time.Minute+time.Second {
		t.Errorf("untilNextDay(UTC clock) = %v, want 30m1s", got)
	}
}

// Nothing in the tick loop reads the cache or the audit log any more.
func TestTickDoesNotPollTheCacheOrTheAuditLog(t *testing.T) {
	a, st := watchedApp(t)
	m := model{ctx: context.Background(), app: a, cache: st, g: unicodeGlyphs, nc: newNotifCenter(notify.NewInbox(st))}
	for _, sec := range []int{0, 2, 4, 30, 58} {
		_, cmd := m.Update(tickMsg(time.Date(2026, 11, 12, 10, 0, sec, 0, time.UTC)))
		if cmd == nil {
			t.Fatal("tick must re-arm")
		}
		for _, msg := range drain(cmd) {
			switch msg.(type) {
			case inboxMsg, aiChipMsg, resultsMsg:
				t.Fatalf("second %d: the tick read %T", sec, msg)
			}
		}
	}
}

// drain runs the commands a model returned and collects the messages that
// arrive within a moment (the one-second tick timer does not).
func drain(cmd tea.Cmd) []tea.Msg {
	out := make(chan tea.Msg, 16)
	var msgs []tea.Msg
	var spawn func(c tea.Cmd)
	spawn = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			msg := c()
			if b, ok := msg.(tea.BatchMsg); ok {
				for _, c := range b {
					spawn(c)
				}
				return
			}
			out <- msg
		}()
	}
	spawn(cmd)
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case msg := <-out:
			msgs = append(msgs, msg)
		case <-deadline:
			return msgs
		}
	}
}
