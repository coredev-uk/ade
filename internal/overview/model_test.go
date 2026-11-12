package overview

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/records"
)

// pane is a model on a temp cache with the item loaded, sized w x h.
func pane(t *testing.T, it domain.WorkItem, w, h int) (model, *cache) {
	t.Helper()
	c := newCache(t)
	c.put(it)
	rs := records.New(t.TempDir())
	if _, _, err := rs.WriteSnapshot("gl", it, nil, 5); err != nil {
		t.Fatal(err)
	}
	if err := rs.WriteNotes("gl", it.Ref, "ask support about 50 req/min"); err != nil {
		t.Fatal(err)
	}
	l := &Loader{Cache: c.st, Records: rs, Ref: it.Ref}
	m := newModel(Options{Loader: l, Now: func() time.Time { return now }})
	m = send(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	m = send(t, m, run(t, m.pollCmd()))
	return m, c
}

func send(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(model)
}

// run executes a command and returns its message.
func run(t *testing.T, c tea.Cmd) tea.Msg {
	t.Helper()
	if c == nil {
		t.Fatal("no command")
	}
	return c()
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func screen(m model) []string { return strings.Split(plain(m.render()), "\n") }

func longItem() domain.WorkItem {
	it := item()
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "paragraph %d of a long description\n\n", i)
	}
	it.Issue.Description = b.String()
	return it
}

func TestLongTextIsClippedToThePaneAndScrolls(t *testing.T) {
	m, _ := pane(t, longItem(), 50, 20)
	s := screen(m)
	if len(s) != 20 {
		t.Fatalf("%d lines in a 20-row pane:\n%s", len(s), strings.Join(s, "\n"))
	}
	if got := s[18]; !strings.HasPrefix(got, " … ") || !strings.HasSuffix(got, " more") {
		t.Errorf("last body line = %q, want a faint ellipsis with the rest", got)
	}
	if !strings.Contains(s[19], "synced 4m ago") || !strings.Contains(s[19], "j/k scroll") || !strings.Contains(s[19], "q quit") {
		t.Errorf("footer = %q", s[19])
	}
	top := s[0]

	m = send(t, m, key("j"))
	if s = screen(m); s[0] != " … 1 above" || s[1] == top {
		t.Errorf("after j: %q / %q", s[0], s[1])
	}
	m = send(t, m, key("k"))
	if screen(m)[0] != top {
		t.Error("k did not return to the top")
	}
	m = send(t, m, key("k"))
	if screen(m)[0] != top {
		t.Error("scrolled above the top")
	}
	m = send(t, m, key("pgdown"))
	if m.off != 18 {
		t.Errorf("pgdown offset = %d, want a page less one line", m.off)
	}

	m = send(t, m, key("G"))
	s = screen(m)
	if !strings.Contains(s[18], "ask support about 50 req/min") || strings.Contains(strings.Join(s, "\n"), " more\n") {
		t.Errorf("at the end:\n%s", strings.Join(s, "\n"))
	}
	if !strings.HasPrefix(s[0], " … ") || !strings.HasSuffix(s[0], " above") {
		t.Errorf("first line at the end = %q", s[0])
	}
	m = send(t, m, key("j"))
	if m.off != m.clamp(1<<20) {
		t.Error("scrolled past the end")
	}
	m = send(t, m, key("pgup"))
	m = send(t, m, key("g"))
	if m.off != 0 {
		t.Errorf("g: offset %d", m.off)
	}
}

func TestShortViewIsNotClipped(t *testing.T) {
	m, _ := pane(t, item(), 50, 60)
	s := strings.Join(screen(m), "\n")
	if strings.Contains(s, "… ") || !strings.Contains(s, "ask support") {
		t.Errorf("a pane taller than the view clipped it:\n%s", s)
	}
	if m = send(t, m, key("j")); m.off != 0 {
		t.Errorf("scrolled a view that fits: %d", m.off)
	}
}

func TestResizeKeepsTheOffsetInRange(t *testing.T) {
	m, _ := pane(t, longItem(), 50, 20)
	m = send(t, m, key("G"))
	m = send(t, m, tea.WindowSizeMsg{Width: 50, Height: 60})
	if m.off != m.clamp(m.off) || len(screen(m)) > 60 {
		t.Errorf("offset %d after growing", m.off)
	}
}

func TestQuitAndReloadKeys(t *testing.T) {
	m, _ := pane(t, item(), 50, 30)
	for _, k := range []string{"q", "ctrl+c"} {
		_, cmd := m.Update(key(k))
		if cmd == nil {
			t.Fatalf("%s: no command", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s did not quit", k)
		}
	}
	_, cmd := m.Update(key("r"))
	lm, ok := run(t, cmd).(loadedMsg)
	if !ok || !lm.reloaded {
		t.Errorf("r = %+v: want a forced reload", lm)
	}
}

func TestChangeSignalReloadsOnlyOnChange(t *testing.T) {
	m, c := pane(t, item(), 50, 30)

	_, cmd := m.Update(changedMsg{open: true})
	if lm := run(t, cmd).(loadedMsg); lm.reloaded {
		t.Error("a signal with an unchanged cache reloaded")
	}

	it := item()
	it.Issue.Title = "Renamed by a sync"
	it.Court, it.Reason = domain.CourtYou, "pipeline failed"
	c.put(it)
	_, cmd = m.Update(changedMsg{open: true})
	lm := run(t, cmd).(loadedMsg)
	if !lm.reloaded {
		t.Fatal("a signal after a sync did not reload")
	}
	m = send(t, m, lm)
	if s := strings.Join(screen(m), "\n"); !strings.Contains(s, "Renamed by a sync") || !strings.Contains(s, "✕ pipeline failed") {
		t.Errorf("view did not follow the cache:\n%s", s)
	}
}

func TestItemLeavingTheCacheKeepsTheLastView(t *testing.T) {
	m, c := pane(t, item(), 50, 30)
	c.put()
	_, cmd := m.Update(changedMsg{open: true})
	m = send(t, m, run(t, cmd))
	s := strings.Join(screen(m), "\n")
	if !strings.Contains(s, "Rate-limit the export endpoint") || !strings.Contains(s, "not in cache any more") {
		t.Errorf("view after the item left:\n%s", s)
	}
	// It comes back with the next sync.
	c.put(item())
	_, cmd = m.Update(changedMsg{open: true})
	m = send(t, m, run(t, cmd))
	if s := strings.Join(screen(m), "\n"); strings.Contains(s, "not in cache any more") {
		t.Errorf("still gone after it returned:\n%s", s)
	}
}

func TestItemNotCachedYetSaysSoAndFillsInLater(t *testing.T) {
	c := newCache(t)
	l := &Loader{Cache: c.st, Records: records.New(t.TempDir()), Ref: item().Ref}
	m := newModel(Options{Loader: l, Now: func() time.Time { return now }})
	m = send(t, m, run(t, m.pollCmd()))
	s := strings.Join(screen(m), "\n")
	for _, want := range []string{"acme/tracker#15", "not in the cache yet", "never synced"} {
		if !strings.Contains(s, want) {
			t.Errorf("view lacks %q:\n%s", want, s)
		}
	}
	c.put(item())
	_, cmd := m.Update(changedMsg{open: true})
	m = send(t, m, run(t, cmd))
	if s := strings.Join(screen(m), "\n"); !strings.Contains(s, "Rate-limit the export endpoint") || strings.Contains(s, "not in the cache yet") {
		t.Errorf("view after the sync:\n%s", s)
	}
}

func TestReloadFailureShowsAboveTheFooterAndKeepsTheView(t *testing.T) {
	m, _ := pane(t, item(), 50, 30)
	m = send(t, m, loadedMsg{reloaded: true, err: errors.New("database is locked")})
	s := screen(m)
	if !strings.Contains(s[len(s)-2], "✕ database is locked") || !strings.Contains(strings.Join(s, "\n"), "Rate-limit the export endpoint") {
		t.Errorf("screen:\n%s", strings.Join(s, "\n"))
	}
	m = send(t, m, loadedMsg{reloaded: true, snap: m.snap})
	if strings.Contains(strings.Join(screen(m), "\n"), "database is locked") {
		t.Error("the failure stayed after a good reload")
	}
}

func TestEditKeyRunsTheEditorAndReloads(t *testing.T) {
	c := newCache(t)
	it := item()
	c.put(it)
	rs := records.New(t.TempDir())
	if _, _, err := rs.WriteSnapshot("gl", it, nil, 5); err != nil {
		t.Fatal(err)
	}
	var opened string
	m := newModel(Options{
		Loader: &Loader{Cache: c.st, Records: rs, Ref: it.Ref},
		Now:    func() time.Time { return now },
		Edit: func(s Snapshot) (*exec.Cmd, error) {
			opened = s.NotesPath
			return exec.Command("true"), nil
		},
	})
	m = send(t, m, run(t, m.pollCmd()))
	if s := strings.Join(screen(m), "\n"); !strings.Contains(s, "e notes") {
		t.Errorf("footer lacks the e key:\n%s", s)
	}
	_, cmd := m.Update(key("e"))
	if cmd == nil {
		t.Fatal("e did nothing")
	}
	if opened != rs.ItemPath("gl", it.Ref) {
		t.Errorf("editor opened %q", opened)
	}

	// Without an editor hook the key is not offered.
	m.opts.Edit = nil
	if s := strings.Join(screen(m), "\n"); strings.Contains(s, "e notes") {
		t.Errorf("e shown without an editor:\n%s", s)
	}
}

func TestFooterDropsKeysFromTheRightWhenNarrow(t *testing.T) {
	m, _ := pane(t, item(), 30, 40)
	f := screen(m)
	last := f[len(f)-1]
	if ansi.StringWidth(last) > 30 || !strings.Contains(last, "synced 4m ago") {
		t.Errorf("footer = %q", last)
	}
}

func TestViewIsAnAltScreenWithATitle(t *testing.T) {
	m, _ := pane(t, item(), 50, 30)
	v := m.View()
	if !v.AltScreen || v.WindowTitle == "" {
		t.Errorf("view = alt %v title %q", v.AltScreen, v.WindowTitle)
	}
}
