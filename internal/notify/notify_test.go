package notify

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
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

func TestToastRules(t *testing.T) {
	a := newApp(t, config.Notify{Toast: []string{"git.*.failed", "ci.*"}})
	var toasts []string
	stop, err := Attach(context.Background(), a, Channels{
		Toast: func(e events.Event) { toasts = append(toasts, e.Kind) },
	})
	if err != nil {
		t.Fatal(err)
	}
	a.Bus.Publish(events.New("git.pull.failed", events.Error, "pull failed"))
	a.Bus.Publish(events.New("task.done", events.Success, "done"))
	a.Bus.Publish(events.New("ci.failed", events.Error, "pipeline failed"))
	stop()
	if strings.Join(toasts, ",") != "git.pull.failed,ci.failed" {
		t.Errorf("toasts = %v, want only the matching kinds", toasts)
	}
	st, _ := a.Cache()
	if n, _ := NewInbox(st).UnreadCount(); n != 3 {
		t.Errorf("inbox unread = %d, want all 3 recorded regardless of toast rules", n)
	}
}

func TestNilChannelsStillRecord(t *testing.T) {
	a := newApp(t, config.Notify{Toast: []string{"*"}})
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
	a := newApp(t, config.Notify{Toast: []string{"*"}})
	var toasts int
	stop, err := Attach(context.Background(), a, Channels{Toast: func(events.Event) { toasts++ }})
	if err != nil {
		t.Fatal(err)
	}
	e := events.New("git.update.done", events.Success, "all repos up to date")
	e.Transient = true
	a.Bus.Publish(e)
	stop()
	st, _ := a.Cache()
	n, err := NewInbox(st).UnreadCount()
	if err != nil || n != 0 || toasts != 1 {
		t.Fatalf("unread=%d err=%v toasts=%d; want 0, nil, 1", n, err, toasts)
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
