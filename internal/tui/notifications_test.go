package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/store"
)

func repoStatus(name string) project.RepoStatus {
	return project.RepoStatus{
		Name: name, Present: true, InManifest: true,
		Baseline: &project.TreeStatus{Name: "main", Branch: "main", HasUpstream: true},
	}
}

// A ci.failed event must surface four ways inside the dashboard: a sticky
// pop-up, an inbox row, the unread count, and a marker on the row it is about.
func TestCIFailedSurfacesEverywhere(t *testing.T) {
	cfg := config.Defaults()
	a := &app.App{Paths: paths.Paths{CacheDir: t.TempDir()}, Config: &cfg, Bus: events.NewBus()}
	t.Cleanup(func() { a.Close() })
	st, err := a.Cache()
	if err != nil {
		t.Fatal(err)
	}
	nc := newNotifCenter(notify.NewInbox(st))
	stop, err := notify.Attach(context.Background(), a, notify.Channels{Toast: nc.onEvent})
	if err != nil {
		t.Fatal(err)
	}
	e := events.New("ci.failed", events.Error, "web!1 pipeline failed: lint")
	e.Project, e.Subject = "demo", "web"
	a.Bus.Publish(e)
	stop()
	nc.onInbox(nc.poll()().(inboxMsg))

	if len(nc.toasts) != 1 || !nc.toasts[0].expires.IsZero() {
		t.Fatalf("toasts = %+v, want one sticky failure pop-up", nc.toasts)
	}
	if len(nc.items) != 1 || nc.items[0].Kind != "ci.failed" {
		t.Fatalf("inbox = %+v, want the ci.failed row", nc.items)
	}
	if nc.unread != 1 {
		t.Fatalf("unread = %d, want 1", nc.unread)
	}
	r := row{project: "demo", repo: repoStatus("web"), base: true}
	if rowUnread(nc.marks(), r) != 1 {
		t.Fatalf("row marker missing: marks = %v", nc.marks())
	}
	m := model{nc: nc, g: unicodeGlyphs}
	if line := ansi.Strip(m.treeLine(r, false, true, true, 80, 1)); !strings.Contains(line, " new ") {
		t.Errorf("tree line has no unread marker: %q", line)
	}
}

// Rows recorded by another ade process pop up once; rows present when the
// dashboard starts never do.
func TestOtherProcessRowsPopUpOnce(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in := notify.NewInbox(st)
	old := events.New("git.clone.ok", events.Success, "cloned")
	if err := in.Record(old); err != nil {
		t.Fatal(err)
	}
	nc := newNotifCenter(in)
	nc.onInbox(nc.poll()().(inboxMsg))
	if len(nc.toasts) != 0 {
		t.Fatalf("pre-existing rows popped up: %+v", nc.toasts)
	}
	fresh := events.New("task.done", events.Success, "api.test passed")
	if err := in.Record(fresh); err != nil {
		t.Fatal(err)
	}
	nc.onInbox(nc.poll()().(inboxMsg))
	nc.onInbox(nc.poll()().(inboxMsg))
	if len(nc.toasts) != 1 || nc.toasts[0].ev.ID != fresh.ID {
		t.Fatalf("toasts = %+v, want exactly the new row once", nc.toasts)
	}
	nc.tick(time.Now().Add(toastTTL + time.Second))
	if len(nc.toasts) != 0 {
		t.Errorf("non-failure toast did not expire")
	}
}

func TestFilterAndJump(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in := notify.NewInbox(st)
	ok := events.New("git.pull.ok", events.Success, "pulled")
	bad := events.New("git.fetch.failed", events.Error, "fetch failed")
	bad.Project, bad.Subject = "demo", "api"
	for _, e := range []events.Event{ok, bad} {
		if err := in.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	nc := newNotifCenter(in)
	nc.onInbox(nc.poll()().(inboxMsg))
	nc.open = true
	nc.key("f") // unread
	nc.key("f") // problems
	if vis := nc.visible(); len(vis) != 1 || vis[0].ID != bad.ID {
		t.Fatalf("problems filter = %+v", vis)
	}
	_, jump := nc.key("enter")
	if jump == nil || jump.Subject != "api" || nc.open {
		t.Fatalf("jump = %+v open=%v", jump, nc.open)
	}
	if it, _ := in.Get(bad.ID); !it.Read {
		t.Error("jumping should mark the item read")
	}
}
