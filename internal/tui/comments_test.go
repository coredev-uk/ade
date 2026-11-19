package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/review"
)

// commentsEnv is Home after a sync of the hierarchy fake in which two people
// commented on one tracker issue (and GitLab added a system note); the other
// trackers have no comments. Its forge is the same fake.
type commentsEnv struct {
	t         *testing.T
	srv       *fake.Server
	m         model
	commented forge.Ref
	quiet     forge.Ref
}

func newCommentsEnv(t *testing.T, w, h int, firstBody string) *commentsEnv {
	t.Helper()
	var commented, quiet forge.Ref
	srv, a, res, _ := syncHierarchyEnv(t, func(srv *fake.Server, c fake.HierarchyCases) {
		commented, quiet = c.HierarchyOnly.Tracker, c.LinkOnly.Tracker
		if _, err := srv.AddNote(commented, "", "reviewer-rae", firstBody); err != nil {
			t.Fatal(err)
		}
		srv.Advance(time.Hour)
		if err := srv.AddSystemNote(commented, "added ~bug label", srv.Now()); err != nil {
			t.Fatal(err)
		}
		srv.Advance(time.Hour)
		if _, err := srv.AddNote(commented, "", "sandbox-dev", "Confirmed on staging, see the logs"); err != nil {
			t.Fatal(err)
		}
	})
	st, err := a.Cache()
	if err != nil {
		t.Fatal(err)
	}
	api := gitlab.New(srv.URL, fake.Token)
	m := model{
		ctx: context.Background(), app: a, cache: st, g: unicodeGlyphs, w: w, h: h,
		nc: newNotifCenter(notify.NewInbox(st)), be: &mrBackends{},
		dial: func(context.Context, forge.Ref, string) (review.Backend, error) { return api, nil },
	}
	m.setResults([]forgesync.Result{res})
	m.homeLoaded = true
	return &commentsEnv{t: t, srv: srv, m: m, commented: commented, quiet: quiet}
}

func (e *commentsEnv) selectItem(ref forge.Ref) {
	e.t.Helper()
	for i, r := range e.m.homeRows {
		if r.item != nil && r.item.Ref == ref {
			e.m.homeSel = i
			return
		}
	}
	e.t.Fatalf("no Home row for %s", ref)
}

// press sends a key and feeds back what its command answers (one step: the
// view's only command is the forge read).
func (e *commentsEnv) press(k string) {
	e.t.Helper()
	next, cmd := e.m.Update(keyMsg(k))
	e.m = next.(model)
	if cmd == nil {
		return
	}
	if msg, ok := cmd().(commentsMsg); ok {
		next, _ = e.m.Update(msg)
		e.m = next.(model)
	}
}

func (e *commentsEnv) screen() string { return ansi.Strip(e.m.render()) }

// Home's detail pane lists the tracker issue's human comments after the
// description: the count, then the newest first, author and first line. An
// issue nobody commented on has no comments block.
func TestHomeDetailShowsTheTrackerIssueComments(t *testing.T) {
	e := newCommentsEnv(t, 160, 44, "Support says the CSV export fails too.\n\nMore detail below.")
	e.selectItem(e.commented)
	detail := ansi.Strip(e.m.homeDetail(60))
	for _, want := range []string{"comments · 2 · C shows all", "Rae Reviewer · ", "Sandbox Dev · ", "Support says the CSV export fails too.", "Confirmed on staging"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "added ~bug label") || strings.Contains(detail, "More detail below") {
		t.Errorf("detail shows a system note or more than the first line:\n%s", detail)
	}
	if strings.Index(detail, "Sandbox Dev") > strings.Index(detail, "Rae Reviewer") {
		t.Errorf("detail does not put the newest comment first:\n%s", detail)
	}

	e.selectItem(e.quiet)
	if detail := ansi.Strip(e.m.homeDetail(60)); strings.Contains(detail, "comments ·") {
		t.Errorf("an issue without comments shows a comments block:\n%s", detail)
	}
}

// `C` opens every comment of the tracker issue, oldest first with its body,
// at once from the record and then as the forge has them; it scrolls, and esc
// goes back to Home.
func TestCommentsViewListsScrollsAndGoesBack(t *testing.T) {
	long := "Support says the CSV export fails too.\n\n" + strings.Repeat("- one more observation from the support call\n", 30)
	e := newCommentsEnv(t, 100, 30, long)
	e.selectItem(e.commented)
	// A comment made after the sync: the view reads the forge.
	if _, err := e.srv.AddNote(e.commented, "", "reviewer-rae", "Also affects **PDF** exports"); err != nil {
		t.Fatal(err)
	}

	next, cmd := e.m.Update(keyMsg("C"))
	e.m = next.(model)
	if e.m.cm == nil || cmd == nil {
		t.Fatal("C did not open the comments view with a forge read")
	}
	if s := e.screen(); !strings.Contains(s, "2 comments · ⟳ reading the forge") || !strings.Contains(s, "Rae Reviewer") {
		t.Errorf("the stored comments do not show while the forge is read:\n%s", s)
	}
	next, _ = e.m.Update(cmd())
	e.m = next.(model)
	if got := len(e.m.cm.comments); got != 3 {
		t.Fatalf("view has %d comments after the read, want 3 (no system note)", got)
	}
	s := e.screen()
	for _, want := range []string{"Comments · " + e.commented.String(), "3 comments · read just now", "Support says the CSV export fails too.", "one more observation", "j/k scroll", "o browser", "esc back"} {
		if !strings.Contains(s, want) {
			t.Errorf("view lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "added ~bug label") {
		t.Errorf("view shows the system note:\n%s", s)
	}

	// The end of the list: the later comments, oldest first, with their bodies.
	e.press("G")
	s = e.screen()
	if !strings.Contains(s, "lines above") || !strings.Contains(s, "Confirmed on staging") || !strings.Contains(s, "Also affects PDF exports") {
		t.Errorf("G does not scroll to the newest comments:\n%s", s)
	}
	if strings.Index(s, "Confirmed on staging") > strings.Index(s, "Also affects PDF exports") {
		t.Errorf("comments not oldest first:\n%s", s)
	}
	e.press("g")
	e.press("j")
	if e.m.cm.scroll != 1 || !strings.Contains(e.screen(), "1 line above") {
		t.Errorf("j scrolled to %d", e.m.cm.scroll)
	}

	e.press("esc")
	if e.m.cm != nil {
		t.Fatal("esc did not close the view")
	}
	if s := e.screen(); !strings.Contains(s, "comments · 2 · C shows all") {
		t.Errorf("esc does not return to Home's detail:\n%s", s)
	}
}

// A forge that cannot be read leaves the stored comments on screen and says why.
func TestCommentsViewKeepsTheStoredCopyWhenTheForgeFails(t *testing.T) {
	e := newCommentsEnv(t, 100, 30, "Support says the CSV export fails too.")
	e.selectItem(e.commented)
	e.m.dial = func(context.Context, forge.Ref, string) (review.Backend, error) { return nil, forge.ErrAuth }
	e.press("C")
	s := e.screen()
	for _, want := range []string{"2 comments · as of the last sync", "the forge rejected the token · showing what the last sync kept", "Confirmed on staging"} {
		if !strings.Contains(s, want) {
			t.Errorf("view lacks %q:\n%s", want, s)
		}
	}
	e.press("esc")

	// An item without comments says so.
	e.selectItem(e.quiet)
	e.press("C")
	if s := e.screen(); !strings.Contains(s, "no comments on the tracker issue yet") {
		t.Errorf("empty view:\n%s", s)
	}
}
