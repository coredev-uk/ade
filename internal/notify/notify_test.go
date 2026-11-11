package notify

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/store"
)

func newApp(t *testing.T, n config.Notify) *app.App {
	t.Helper()
	a := &app.App{
		Paths:  paths.Paths{CacheDir: t.TempDir()},
		Config: &config.Config{Notify: n},
		Bus:    events.NewBus(),
	}
	t.Cleanup(func() { a.Close() })
	return a
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func TestChannelRules(t *testing.T) {
	a := newApp(t, config.Notify{
		Toast:    []string{"*"},
		Terminal: []string{"git.*.failed"},
	})
	var toasts []string
	term := &syncBuf{}
	stop, err := Attach(context.Background(), a, Channels{
		Toast:    func(e events.Event) { toasts = append(toasts, e.Kind) },
		Terminal: term,
	})
	if err != nil {
		t.Fatal(err)
	}
	a.Bus.Publish(events.New("git.pull.failed", events.Error, "pull failed"))
	a.Bus.Publish(events.New("task.done", events.Success, "done"))
	stop()

	if len(toasts) != 2 {
		t.Errorf("toasts = %v, want both events", toasts)
	}
	out := term.b.String()
	if !strings.Contains(out, "pull failed") || strings.Contains(out, "done") {
		t.Errorf("terminal got %q, want only the git.*.failed event", out)
	}
}

func TestNilChannelsStillRecord(t *testing.T) {
	a := newApp(t, config.Notify{Toast: []string{"*"}, Terminal: []string{"*"}, Desktop: []string{"*"}})
	stop, err := Attach(context.Background(), a, Channels{})
	if err != nil {
		t.Fatal(err)
	}
	a.Bus.Publish(events.New("x.y", events.Info, "t"))
	stop()
	st, _ := a.Cache()
	n, err := NewInbox(st).UnreadCount()
	if err != nil || n != 1 {
		t.Fatalf("unread = %d, %v", n, err)
	}
}

func TestTransientToastsButIsNotRecorded(t *testing.T) {
	a := newApp(t, config.Notify{Toast: []string{"*"}, Terminal: []string{"*"}})
	var toasts int
	var term strings.Builder
	stop, err := Attach(context.Background(), a, Channels{Toast: func(events.Event) { toasts++ }, Terminal: &term})
	if err != nil {
		t.Fatal(err)
	}
	e := events.New("git.update.done", events.Success, "all repos up to date")
	e.Transient = true
	a.Bus.Publish(e)
	stop()
	st, _ := a.Cache()
	n, err := NewInbox(st).UnreadCount()
	if err != nil || n != 0 || toasts != 1 || term.Len() != 0 {
		t.Fatalf("unread=%d err=%v toasts=%d terminal=%q; want 0, nil, 1, empty", n, err, toasts, term.String())
	}
}

func TestQuietMutesTerminalNotToast(t *testing.T) {
	n := &notifier{
		cfg: config.Notify{Toast: []string{"*"}, Terminal: []string{"*"}, Quiet: "22:00-07:00"},
		now: func() time.Time { return time.Date(2026, 1, 1, 23, 0, 0, 0, time.Local) },
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n.inbox = NewInbox(st)
	term := &syncBuf{}
	toasted := false
	n.ch = Channels{Toast: func(events.Event) { toasted = true }, Terminal: term}
	n.handle(context.Background(), events.New("a.b", events.Error, "boom"))
	if !toasted {
		t.Error("toast should ignore quiet hours")
	}
	if term.b.Len() != 0 {
		t.Errorf("terminal written during quiet hours: %q", term.b.String())
	}
}

func TestInQuiet(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.Local) }
	cases := []struct {
		window string
		t      time.Time
		want   bool
	}{
		{"", at(3, 0), false},
		{"garbage", at(3, 0), false},
		{"25:00-07:00", at(3, 0), false},
		{"09:00-17:00", at(9, 0), true},
		{"09:00-17:00", at(16, 59), true},
		{"09:00-17:00", at(17, 0), false},
		{"09:00-17:00", at(8, 59), false},
		{"22:00-07:00", at(23, 30), true},
		{"22:00-07:00", at(0, 0), true},
		{"22:00-07:00", at(6, 59), true},
		{"22:00-07:00", at(7, 0), false},
		{"22:00-07:00", at(12, 0), false},
		{"22:00-22:00", at(22, 0), false},
	}
	for _, c := range cases {
		if got := inQuiet(c.window, c.t); got != c.want {
			t.Errorf("inQuiet(%q, %s) = %v, want %v", c.window, c.t.Format("15:04"), got, c.want)
		}
	}
}

func TestStopDrainsPublishedEvents(t *testing.T) {
	a := newApp(t, config.Notify{})
	stop, err := Attach(context.Background(), a, Channels{})
	if err != nil {
		t.Fatal(err)
	}
	const total = 1000
	for i := 0; i < total; i++ {
		a.Bus.Publish(events.New("bulk.event", events.Info, "e"))
	}
	stop()
	stop() // idempotent
	st, _ := a.Cache()
	items, err := NewInbox(st).List(ListOpts{})
	if err != nil || len(items) != total {
		t.Fatalf("recorded %d of %d (err %v)", len(items), total, err)
	}
}

func TestTerminalSanitised(t *testing.T) {
	e := events.New("x.y", events.Error, "bad\x1b]0;pwn\x07 ti;tle")
	e.Detail = "line1\x1b[31m red\nsecond line"
	seq := terminalSequence(e)
	// Exactly the sequence's own ESC and BEL bytes may remain.
	if got := strings.Count(seq, "\x1b"); got != 1 {
		t.Errorf("ESC count = %d in %q, want 1 (the OSC introducer only)", got, seq)
	}
	if got := strings.Count(seq, "\a"); got != 1 {
		t.Errorf("BEL count = %d in %q, want 1 (the OSC terminator only)", got, seq)
	}
	want := "\x1b]9;bad]0,pwn ti,tle: line1[31m red\a"
	if seq != want {
		t.Errorf("got %q\nwant %q", seq, want)
	}
}

func TestInboxPersistsAcrossOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	open := func() (*Inbox, *store.Store) {
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		return NewInbox(st), st
	}
	in, st := open()
	var ids []string
	for _, p := range []string{"a", "b", "a"} {
		e := events.New("k.v", events.Warn, "title "+p)
		e.Project, e.Detail = p, "detail"
		ids = append(ids, e.ID)
		if err := in.Record(e); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if err := in.Record(events.Event{ID: ids[0], Time: time.Now(), Kind: "dup", Title: "dup"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := in.MarkRead(ids[1]); n != 1 {
		t.Errorf("MarkRead changed %d", n)
	}
	st.Close()

	in, st = open()
	defer st.Close()
	all, err := in.List(ListOpts{})
	if err != nil || len(all) != 3 {
		t.Fatalf("List = %d, %v", len(all), err)
	}
	if all[0].ID != ids[2] || all[0].Severity != events.Warn || all[0].Detail != "detail" {
		t.Errorf("newest-first / fields wrong: %+v", all[0])
	}
	if all[0].Kind != "k.v" {
		t.Errorf("duplicate id overwrote row: %+v", all[0])
	}
	if un, _ := in.List(ListOpts{UnreadOnly: true}); len(un) != 2 {
		t.Errorf("unread = %d, want 2", len(un))
	}
	if pa, _ := in.List(ListOpts{Project: "a", Limit: 1}); len(pa) != 1 || pa[0].ID != ids[2] {
		t.Errorf("project/limit filter wrong: %+v", pa)
	}
	after, _ := in.After(ids[0])
	if len(after) != 2 || after[0].ID != ids[1] || after[1].ID != ids[2] {
		t.Errorf("After = %+v", after)
	}
	if n, _ := in.MarkAllRead(); n != 2 {
		t.Errorf("MarkAllRead = %d, want 2", n)
	}
	if n, _ := in.UnreadCount(); n != 0 {
		t.Errorf("unread = %d", n)
	}
}

func TestGetByIDAndPrefix(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in := NewInbox(st)
	for _, id := range []string{"01AAAA", "01AABB", "02ZZZZ"} {
		if err := in.Record(events.Event{ID: id, Time: time.Now(), Kind: "k", Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	if it, err := in.Get("01AAAA"); err != nil || it.ID != "01AAAA" {
		t.Errorf("exact: %v %v", it.ID, err)
	}
	if it, err := in.Get("02"); err != nil || it.ID != "02ZZZZ" {
		t.Errorf("prefix: %v %v", it.ID, err)
	}
	if _, err := in.Get("01A"); err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Errorf("ambiguous prefix should fail distinctly, got %v", err)
	}
	if _, err := in.Get("99"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing: %v", err)
	}
	if _, err := in.Get("%"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("LIKE wildcard must be escaped: %v", err)
	}
}

func TestDesktopRequiresDisplayAndBinary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")
	if desktopBinary() != "" {
		t.Error("no notify-send on PATH should disable desktop")
	}
	out := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + out + "\n"
	if err := os.WriteFile(filepath.Join(dir, "notify-send"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := desktopBinary()
	if bin == "" {
		t.Fatal("notify-send on PATH with DISPLAY set should enable desktop")
	}
	t.Setenv("DISPLAY", "")
	if desktopBinary() != "" {
		t.Error("no display should disable desktop")
	}
	sendDesktop(context.Background(), bin, events.New("a.b", events.Error, "title"))
	got, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(got), "--urgency=critical") {
		t.Errorf("args = %q, %v", got, err)
	}
}

func TestInboxRecordRedactsSecrets(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in := NewInbox(st)
	e := events.New("git.fetch.failed", events.Error, "fetch failed")
	e.Detail = "fatal: unable to access 'https://oauth2:glpat-abcdefghij0123456789@gitlab.com/a/b.git/'"
	if err := in.Record(e); err != nil {
		t.Fatal(err)
	}
	it, err := in.Get(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(it.Detail, "glpat") || !strings.Contains(it.Detail, "https://***@gitlab.com") {
		t.Errorf("detail not redacted: %q", it.Detail)
	}
}
