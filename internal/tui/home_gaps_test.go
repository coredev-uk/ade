package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/review"
	"github.com/coredev-uk/ade/internal/workspace"
)

// homeHarness is Home over sampleResult at a given width.
func homeHarness(t *testing.T, w int) *wsHarness {
	t.Helper()
	h := newWsHarness(t)
	h.m.w, h.m.h = w, 44
	return h
}

func (h *wsHarness) keys(ks ...string) {
	h.t.Helper()
	for _, k := range ks {
		next, cmd := h.m.Update(keyPress(k))
		h.m = next.(model)
		_ = cmd
	}
}

// send feeds a message and returns the command it led to.
func (h *wsHarness) send(msg tea.Msg) tea.Cmd {
	h.t.Helper()
	next, cmd := h.m.Update(msg)
	h.m = next.(model)
	return cmd
}

func (h *wsHarness) refs() []string {
	var out []string
	for _, r := range h.m.homeRows {
		out = append(out, r.ref())
	}
	return out
}

func (h *wsHarness) screen() string { return ansi.Strip(h.m.render()) }

// `/` narrows Home as you type, fuzzily and over every field; esc clears it.
func TestSearchFiltersAsYouTypeAndEscClears(t *testing.T) {
	h := homeHarness(t, 120)
	all := h.refs()
	h.keys("/")
	if !h.m.hs.search.typing {
		t.Fatal("/ does not focus the search input")
	}
	h.keys("b", "l", "g", "z") // "blg" is a subsequence of Billing; "blgz" of nothing
	if got := h.refs(); len(got) != 0 {
		t.Fatalf("blgz matched %v", got)
	}
	h.keys("backspace")
	if got := strings.Join(h.refs(), " "); got != "g/tracker#6" {
		t.Fatalf("blg matched %q, want only #6 (Billing export times out)", got)
	}
	if out := h.screen(); !strings.Contains(out, "/ blg") || !strings.Contains(out, "1 match") {
		t.Errorf("no search bar with the count:\n%s", out)
	}
	h.keys("esc")
	if h.m.hs.search.typing || h.m.searching() || len(h.refs()) != len(all) {
		t.Fatalf("esc did not clear the search: %v", h.refs())
	}
}

// enter keeps the filter and returns the keyboard to the list; esc then clears.
func TestSearchEnterKeepsTheFilter(t *testing.T) {
	h := homeHarness(t, 120)
	h.keys("/", "e", "x", "p", "o", "r", "t", "enter")
	if h.m.hs.search.typing || len(h.refs()) == 0 {
		t.Fatalf("typing=%v rows=%v", h.m.hs.search.typing, h.refs())
	}
	n := len(h.refs())
	h.keys("j") // j is a move again, not text
	if h.m.hs.search.input != "export" || len(h.refs()) != n {
		t.Fatalf("j edited the query: %q", h.m.hs.search.input)
	}
	h.keys("esc")
	if h.m.searching() {
		t.Error("esc did not clear an applied search")
	}
}

// Typing also searches the project's open MRs on the forge, after a pause and
// off the UI loop; a stale answer is dropped and the pool is asked once.
func TestSearchFindsOpenMRsOnTheForge(t *testing.T) {
	srv := fake.New(fake.Premium)
	t.Cleanup(srv.Close)
	api := gitlab.New(srv.URL, fake.Token)

	h := homeHarness(t, 120)
	manifest := filepath.Join(t.TempDir(), "demo.ade.toml")
	if err := os.WriteFile(manifest, []byte("name = \"demo\"\n[repos.api]\npath = \""+fake.GroupPath+"/api\"\napp = [\"api\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.m.app.Config.Projects = []config.ProjectRef{{Source: manifest}}
	asked := 0
	h.m.dial = func(context.Context, forge.Ref, string) (review.Backend, error) { asked++; return api, nil }
	h.m.be = &mrBackends{}

	h.keys("/")
	var tick tea.Cmd
	for _, k := range []string{"r", "e", "n"} {
		next, cmd := h.m.Update(keyPress(k))
		h.m = next.(model)
		tick = cmd
	}
	if tick == nil {
		t.Fatal("typing schedules no forge search")
	}
	gen := h.m.hs.search.gen
	// An older tick (before the last keystroke) does nothing.
	if cmd := h.send(searchTickMsg{gen: gen - 1}); cmd != nil || h.m.hs.search.loading {
		t.Fatal("a stale debounce tick started a search")
	}
	start := time.Now()
	msg := tick() // the debounce: returns after the pause
	if time.Since(start) < searchDebounce/2 {
		t.Errorf("the search fired after %v, with no pause", time.Since(start))
	}
	fetch := h.send(msg)
	if fetch == nil || !h.m.hs.search.loading {
		t.Fatal("the pause did not start the forge search")
	}
	if out := h.screen(); !strings.Contains(out, "searching open MRs") {
		t.Errorf("no 'searching' note while the forge answers:\n%s", out)
	}
	h.send(fetch())
	if h.m.hs.search.loading || !h.m.hs.search.fetched {
		t.Fatalf("search state after the answer: %+v", h.m.hs.search)
	}
	var found *homeRow
	for i, r := range h.m.homeRows {
		if r.section == secSearch && strings.Contains(r.mr.Title, "ename") {
			found = &h.m.homeRows[i]
		}
	}
	if found == nil {
		t.Fatalf("no search row for the rename MR: %v", h.refs())
	}
	if out := h.screen(); !strings.Contains(out, "Open MRs · search") {
		t.Errorf("search section missing:\n%s", out)
	}
	// Refining the query filters the pool locally: no second forge call.
	before := asked
	next, cmd := h.m.Update(keyPress("a"))
	h.m = next.(model)
	if cmd != nil || asked != before {
		t.Error("refining the query asked the forge again")
	}
}

// A forge that cannot be reached is said so in the search bar.
func TestSearchForgeFailureIsShown(t *testing.T) {
	h := homeHarness(t, 120)
	h.m.hs.search = searchState{input: "x", loading: true, gen: 3}
	h.send(searchPoolMsg{gen: 3, err: context.DeadlineExceeded})
	if out := h.screen(); !strings.Contains(out, "MR search failed") {
		t.Errorf("failure not shown:\n%s", out)
	}
}

// Every Home action is reachable by name from `:`, with its key and CLI command.
func TestPaletteListsEveryAction(t *testing.T) {
	h := homeHarness(t, 120)
	have := map[string]bool{}
	names := ""
	for _, e := range h.m.paletteEntries() {
		have[e.key] = true
		names += e.name + "\n"
	}
	for _, k := range []string{"s", "M", "c", "a", "A", "w", "y", "o", "n", "=", "r", "f", "p", "e", "/", "enter", "i", "U", "?", "q"} {
		if !have[k] {
			t.Errorf("no palette entry for key %q", k)
		}
	}
	for _, name := range []string{"Standup", "Sprint review", "Budget", "Audit log", "Go to Worktrees", "Go to Home"} {
		if !strings.Contains(names, name) {
			t.Errorf("no palette entry %q", name)
		}
	}
	h.m.page = pageBudget
	for _, e := range h.m.paletteEntries() {
		if e.needHome {
			t.Errorf("%q needs Home but is offered on the Budget page", e.name)
		}
	}
}

func TestPaletteFindsByTypingAndRuns(t *testing.T) {
	h := homeHarness(t, 120)
	h.keys(":")
	if h.m.hs.pal == nil {
		t.Fatal(": did not open the palette")
	}
	h.keys("s", "t", "n", "d", "u", "p")
	vis := h.m.hs.pal.visible()
	if len(vis) == 0 || vis[0].name != "Standup" {
		t.Fatalf("stndup found %+v", vis)
	}
	if out := h.screen(); !strings.Contains(out, "Command palette") || !strings.Contains(out, "ade standup") {
		t.Errorf("palette not drawn with its CLI hint:\n%s", out)
	}
	h.keys("enter")
	if h.m.hs.pal != nil || !h.m.isAgilePage() {
		t.Fatalf("enter did not run Standup: pal=%v agile=%v", h.m.hs.pal != nil, h.m.isAgilePage())
	}
}

func TestPaletteRunsKeyActionsOnTheSelectedRow(t *testing.T) {
	old := openURL
	var opened []string
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	t.Setenv("DISPLAY", ":0")
	t.Setenv("SSH_CONNECTION", "")
	h := homeHarness(t, 120)
	h.selectRef("g/tracker#3")
	h.keys(":", "b", "r", "o", "w", "s", "e", "r")
	if v := h.m.hs.pal.visible(); len(v) == 0 || v[0].name != "Open in browser" {
		t.Fatalf("browser entry = %+v", v)
	}
	next, cmd := h.m.Update(keyPress("enter"))
	h.m = next.(model)
	if cmd == nil {
		t.Fatal("no command")
	}
	h.send(cmd())
	if len(opened) != 1 || opened[0] != "https://example.com/g/tracker#3" {
		t.Fatalf("opened %v", opened)
	}
}

func TestPaletteEscClosesAndKeepsState(t *testing.T) {
	h := homeHarness(t, 120)
	h.keys(":", "x", "esc")
	if h.m.hs.pal != nil {
		t.Fatal("esc left the palette open")
	}
	if !h.m.onHomePage() {
		t.Fatal("left Home")
	}
}

// `f` opens a picker of the attention filters and of every facet value on Home.
func TestFilterPickerOffersFacetValues(t *testing.T) {
	h := homeHarness(t, 120)
	res := sampleResult()
	res.Items[0].Type, res.Items[0].Apps = "bug", []string{"api"}
	res.Items[1].Type, res.Items[1].Apps, res.Items[1].Workflow = "feature", []string{"web"}, "review"
	h.send(resultsMsg{results: []forgesync.Result{res}})

	h.keys("f")
	if h.m.hs.pal == nil || h.m.hs.pal.prompt != "" {
		t.Fatal("f did not open the picker")
	}
	var names []string
	for _, e := range h.m.hs.pal.all {
		names = append(names, e.name)
	}
	list := strings.Join(names, "|")
	for _, want := range []string{"Filter: blocked", "Filter: type bug", "Filter: type feature", "Filter: app api", "Filter: app web", "Filter: workflow review"} {
		if !strings.Contains(list, want) {
			t.Errorf("picker lacks %q: %s", want, list)
		}
	}
	h.keys("t", "y", "p", "e", "b", "u", "g", "enter")
	if h.m.hs.facet != "type" || h.m.hs.facetVal != "bug" {
		t.Fatalf("facet = %q %q", h.m.hs.facet, h.m.hs.facetVal)
	}
	if got := strings.Join(h.refs(), " "); got != "g/tracker#3" {
		t.Fatalf("type bug shows %q", got)
	}
	if out := h.screen(); !strings.Contains(out, "type bug") {
		t.Errorf("footer does not name the filter:\n%s", out)
	}
	// An attention filter replaces the facet.
	h.m = h.m.setFilter(hfBlocked, "", "")
	if h.m.hs.facet != "" || strings.Join(h.refs(), " ") != "g/tracker#5" {
		t.Errorf("blocked after facet: %q %v", h.m.hs.facet, h.refs())
	}
}

// `p` cycles the project filter; rows and sections of other projects go away.
func TestProjectFilterCycles(t *testing.T) {
	h := homeHarness(t, 120)
	other := sampleResult()
	other.Project, other.Tracker = "second", "g2/tracker"
	for i := range other.Items {
		other.Items[i].Project = "second"
		other.Items[i].Ref.Project = "g2/tracker"
		other.Items[i].Issue.Ref = other.Items[i].Ref
	}
	h.send(resultsMsg{results: []forgesync.Result{sampleResult(), other}})
	total := len(h.refs())
	h.keys("p")
	if h.m.hs.project != "demo" || len(h.refs()) != total/2 {
		t.Fatalf("p once: project %q rows %d of %d", h.m.hs.project, len(h.refs()), total)
	}
	if out := h.screen(); strings.Contains(out, "second ·") {
		t.Errorf("the other project's rule is still drawn:\n%s", out)
	}
	h.keys("p")
	if h.m.hs.project != "second" {
		t.Fatalf("p twice: %q", h.m.hs.project)
	}
	h.keys("p")
	if h.m.hs.project != "" || len(h.refs()) != total {
		t.Fatalf("p thrice: %q", h.m.hs.project)
	}
}

func TestProjectFilterWithOneProjectSaysSo(t *testing.T) {
	h := homeHarness(t, 120)
	h.keys("p")
	if h.m.hs.project != "" || h.lastToast().Kind != "ui.hint" {
		t.Errorf("project %q toast %+v", h.m.hs.project, h.lastToast())
	}
}

// `o` opens the browser; on a plain host (or with no display) it shows the URL.
func TestOOpensBrowserOrShowsTheURL(t *testing.T) {
	old := openURL
	var opened []string
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })

	t.Run("display", func(t *testing.T) {
		opened = nil
		t.Setenv("DISPLAY", ":0")
		t.Setenv("SSH_CONNECTION", "")
		h := homeHarness(t, 120)
		h.selectRef("g/tracker#1")
		next, cmd := h.m.Update(keyPress("o"))
		h.m = next.(model)
		h.send(cmd())
		if len(opened) != 1 || opened[0] != "https://example.com/g/tracker#1" {
			t.Fatalf("opened %v", opened)
		}
		if e := h.lastToast(); e.Kind != "ui.browser" || e.Severity != events.Info {
			t.Errorf("toast %+v", e)
		}
	})
	t.Run("plain host", func(t *testing.T) {
		opened = nil
		t.Setenv("DISPLAY", ":0")
		t.Setenv("SSH_CONNECTION", "")
		h := homeHarness(t, 120)
		h.m.app.Config.Host.Mode = "plain"
		h.selectRef("g/tracker#1")
		_, cmd := h.m.Update(keyPress("o"))
		if len(opened) != 0 {
			t.Fatal("a plain host opened a browser")
		}
		if cmd == nil {
			t.Error("the URL is not copied")
		}
	})
	t.Run("plain host shows the URL", func(t *testing.T) {
		t.Setenv("DISPLAY", ":0")
		h := homeHarness(t, 120)
		h.m.app.Config.Host.Mode = "plain"
		h.selectRef("g/tracker#1")
		h.keys("o")
		if e := h.lastToast(); !strings.Contains(e.Title, "https://example.com/g/tracker#1") {
			t.Errorf("toast %+v", e)
		}
	})
	t.Run("no display", func(t *testing.T) {
		t.Setenv("DISPLAY", "")
		t.Setenv("WAYLAND_DISPLAY", "")
		t.Setenv("SSH_CONNECTION", "")
		h := homeHarness(t, 120)
		if h.m.canOpenBrowser() {
			t.Error("a Linux session with no display claims a browser")
		}
	})
}

// `r` refreshes, unless the selected item's services strip shows a blocked
// service: then it restarts the subtree its dead requirement heads.
func TestRRefreshesOrRestartsTheBlockedSubtree(t *testing.T) {
	h := homeHarness(t, 120)
	h.selectRef("g/tracker#3")
	if _, ok := h.m.blockedChip(); ok {
		t.Fatal("a blocked chip with no services")
	}
	h.m.ws.chips = map[string][]svcChip{"g/tracker#3": {
		{node: "api.dev", port: 5031, state: "ready"},
		{node: "web.dev", state: "blocked", blocker: "api.dev", row: workspace.Row{ID: "x", Node: "web.dev", BlockedBy: "api.dev", State: workspace.StateBlocked}},
	}}
	c, ok := h.m.blockedChip()
	if !ok || c.node != "web.dev" || c.row.RestartNode() != "api.dev" {
		t.Fatalf("blocked chip = %+v ok=%v", c, ok)
	}
	next, cmd := h.m.Update(keyPress("r"))
	h.m = next.(model)
	if cmd == nil {
		t.Fatal("r on a blocked service starts nothing")
	}
	if e := h.lastToast(); e.Kind != "ws.restart" || !strings.Contains(e.Title, "api.dev") {
		t.Errorf("no restarting note naming the requirement: %+v", e)
	}
	msg, ok := cmd().(restartedMsg)
	if !ok || msg.node != "api.dev" {
		t.Fatalf("answer %+v", msg)
	}
	h.send(msg)
	if e := h.lastToast(); e.Kind != "ws.restarted" && e.Kind != "ws.restart.failed" {
		t.Errorf("outcome not reported: %+v", e)
	}
	// A failure is a failure pop-up with the reason, not silence.
	h.send(restartedMsg{node: "api.dev", err: os.ErrNotExist})
	if e := h.lastToast(); e.Kind != "ws.restart.failed" || e.Severity != events.Error {
		t.Errorf("failure toast %+v", e)
	}
}

func TestRWithoutBlockedServicesRefreshes(t *testing.T) {
	h := homeHarness(t, 120)
	h.selectRef("g/tracker#3")
	h.m.ws.chips = map[string][]svcChip{"g/tracker#3": {{node: "api.dev", state: "ready"}}}
	h.m.syncing, h.m.updating = true, true // both loops busy: refresh is a no-op command-wise
	_, cmd := h.m.Update(keyPress("r"))
	if cmd != nil {
		t.Error("r with nothing blocked should refresh, which is idle while both loops run")
	}
	if len(h.m.nc.toasts) != 0 {
		t.Error("a restart note appeared with nothing blocked")
	}
}

// Activity is collapsed by default; e shows the latest inbox events. At wide
// widths the rail shows them without any key.
func TestActivityCollapsedByDefaultThenExpands(t *testing.T) {
	h := homeHarness(t, 120)
	h.m.nc.items = []notify.Item{
		{Event: events.Event{Kind: "ci.failed", Severity: events.Error, Title: "web!1 pipeline failed: lint", Time: time.Now().Add(-5 * time.Minute)}},
		{Event: events.Event{Kind: "mr.merged", Severity: events.Success, Title: "api!2 merged", Time: time.Now().Add(-time.Hour)}, Read: true},
	}
	out := h.screen()
	if !strings.Contains(out, "Activity") || !strings.Contains(out, "2 recent") || strings.Contains(out, "pipeline failed: lint") {
		t.Fatalf("collapsed Activity:\n%s", out)
	}
	h.keys("e")
	out = h.screen()
	for _, want := range []string{"web!1 pipeline failed: lint", "api!2 merged", "5m"} {
		if !strings.Contains(out, want) {
			t.Errorf("expanded Activity lacks %q:\n%s", want, out)
		}
	}
	h.keys("e")
	if strings.Contains(h.screen(), "api!2 merged") {
		t.Error("e did not collapse Activity")
	}
}

func TestActivityFollowsTheProjectFilter(t *testing.T) {
	h := homeHarness(t, 120)
	h.m.nc.items = []notify.Item{
		{Event: events.Event{Project: "demo", Title: "mine"}},
		{Event: events.Event{Project: "other", Title: "theirs"}},
		{Event: events.Event{Title: "everyone"}},
	}
	h.m.hs.project = "demo"
	var titles []string
	for _, it := range h.m.recentEvents(8) {
		titles = append(titles, it.Title)
	}
	if strings.Join(titles, ",") != "mine,everyone" {
		t.Errorf("recent events = %v", titles)
	}
}

// Previous sprint lists unfinished items carried over from it, not finished ones.
func TestPreviousSprintSection(t *testing.T) {
	h := homeHarness(t, 200)
	res := sampleResult()
	prev := &domain.Sprint{Key: "m41", Title: "Sprint 41", Start: forge.NewDate(2026, 10, 19), Due: forge.NewDate(2026, 10, 30)}
	res.Window.Previous = prev
	mk := func(iid int, title string, st domain.Stage) domain.WorkItem {
		ref := forge.Ref{Project: "g/tracker", IID: iid}
		return domain.WorkItem{Project: "demo", Ref: ref, Issue: forge.Issue{Ref: ref, Title: title}, Stage: st, Court: domain.CourtNobody, Sprint: prev, Repos: []string{"api"}}
	}
	res.Items = append(res.Items, mk(20, "Carried over cleanup", domain.StageStarted), mk(21, "Finished last sprint", domain.StageDone))
	h.send(resultsMsg{results: []forgesync.Result{res}})
	out := h.screen()
	for _, want := range []string{"Previous sprint", "1 unfinished", "carried over from Sprint 41", "Carried over cleanup"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Finished last sprint") {
		t.Error("a finished item is listed as carried over")
	}
	// Without a previous sprint the section is absent.
	h.send(resultsMsg{results: []forgesync.Result{sampleResult()}})
	if strings.Contains(h.screen(), "Previous sprint") {
		t.Error("Previous sprint shown with no previous sprint")
	}
}

// A row carries its unresolved thread count and pipeline state as a glyph plus
// a number, in both glyph sets; epic follows the title when there is room.
func TestRowBadgeAndEpic(t *testing.T) {
	for _, g := range []glyphs{unicodeGlyphs, asciiGlyphs} {
		h := homeHarness(t, 230)
		h.m.g = g
		res := sampleResult()
		it := &res.Items[1] // #1
		it.Issue.Epic = "Exports"
		it.MRs = []forge.MR{
			{State: "opened", Unresolved: 2, Pipeline: &forge.Pipeline{Status: "failed"}},
			{State: "merged", Unresolved: 9},
		}
		h.send(resultsMsg{results: []forgesync.Result{res}})
		var line string
		for _, l := range strings.Split(h.screen(), "\n") {
			if strings.Contains(l, "Rate-limit") {
				line = l
			}
		}
		if !strings.Contains(line, g.Thread+" 2") || !strings.Contains(line, g.Bad) {
			t.Errorf("row lacks thread count and pipeline glyph (%q):\n%s", g.Thread, line)
		}
		if strings.Contains(line, g.Thread+" 11") {
			t.Errorf("merged MR's threads counted: %s", line)
		}
		if !strings.Contains(line, g.Sep+" Exports") {
			t.Errorf("row lacks its epic:\n%s", line)
		}
		if g == asciiGlyphs && !isASCII(strings.SplitN(line, "│", 2)[0]) {
			t.Errorf("ascii row has non-ascii runes: %s", line)
		}
	}
}

func TestRowBadgeStates(t *testing.T) {
	m := model{g: unicodeGlyphs}
	cases := []struct {
		name string
		it   domain.WorkItem
		want string
	}{
		{"none", domain.WorkItem{}, ""},
		{"green", domain.WorkItem{MRs: []forge.MR{{State: "opened", Pipeline: &forge.Pipeline{Status: "success"}}}}, "✓"},
		{"running", domain.WorkItem{MRs: []forge.MR{{State: "opened", Pipeline: &forge.Pipeline{Status: "running"}}}}, "⟳"},
		{"threads", domain.WorkItem{MRs: []forge.MR{{State: "opened", Unresolved: 12}}}, "✉ 12"},
	}
	for _, c := range cases {
		got := strings.TrimSpace(ansi.Strip(m.rowBadge(&c.it)))
		if got != c.want {
			t.Errorf("%s: badge %q, want %q", c.name, got, c.want)
		}
		if w := ansi.StringWidth(m.rowBadge(&c.it)); w != badgeCells {
			t.Errorf("%s: badge is %d cells, want %d", c.name, w, badgeCells)
		}
	}
}

// The sprint rule reads the load as committed, done, left, and your average.
func TestSprintRuleShowsLeftAndYourAverage(t *testing.T) {
	m := model{g: unicodeGlyphs}
	res := sampleResult()
	rule := ansi.Strip(m.projectRule(&res, 200))
	if !strings.Contains(rule, "12w committed") || !strings.Contains(rule, "10w left") || strings.Contains(rule, "your avg") {
		t.Errorf("rule without an average: %s", rule)
	}
	m.hs.vel = map[string]myVel{"demo": {avg: 8, basis: "weight"}}
	rule = ansi.Strip(m.projectRule(&res, 200))
	if !strings.Contains(rule, "10w left · 2 unweighted · your avg 8w") && !strings.Contains(rule, "10w left") {
		t.Errorf("rule: %s", rule)
	}
	if !strings.Contains(rule, "your avg 8w") {
		t.Errorf("rule lacks your average: %s", rule)
	}
	if got := (myVel{avg: 2.5, basis: "weight"}).String(); got != "your avg 2.5w" {
		t.Errorf("fractional average: %q", got)
	}
	if got := (myVel{avg: 3, basis: "count"}).String(); got != "your avg 3 items" {
		t.Errorf("count basis: %q", got)
	}
}

// At 140+ columns Home has three regions: list | preview | activity and services.
func TestThreeRegionLayoutAtWideWidths(t *testing.T) {
	h := homeHarness(t, 170)
	h.selectRef("g/tracker#3")
	h.m.ws.chips = map[string][]svcChip{"g/tracker#3": {{node: "api.dev", port: 5031, state: "ready"}, {node: "web.dev", state: "failed"}}}
	h.m.nc.items = []notify.Item{{Event: events.Event{Severity: events.Error, Title: "web!1 ci failed", Time: time.Now()}}}
	out := h.screen()
	for _, want := range []string{"Activity", "Services", "api.dev ● 5031", "web.dev ✕ failed", "web!1 ci failed", "Retry failed webhook"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide Home lacks %q:\n%s", want, out)
		}
	}
	// The preview no longer carries the strip, and Activity is not also a list section.
	if strings.Contains(ansi.Strip(h.m.homeDetail(40)), "services  api.dev") {
		t.Error("services shown in both the preview and the rail")
	}
	if strings.Count(out, "Activity") != 1 {
		t.Errorf("Activity drawn %d times", strings.Count(out, "Activity"))
	}
	for _, l := range strings.Split(out, "\n") {
		if ansi.StringWidth(l) > 170 {
			t.Fatalf("line wider than the terminal: %d", ansi.StringWidth(l))
		}
	}
	// Two regions below 140, one below 100.
	for _, w := range []int{139, 120} {
		h.m.w = w
		if strings.Contains(h.screen(), "Services ") {
			t.Errorf("rail at %d columns", w)
		}
	}
	h.m.w = 90
	if h.m.threeRegion() {
		t.Error("three regions at 90")
	}
}

// Narrow single-pane Home keeps working with a search active.
func TestNarrowHomeWithSearch(t *testing.T) {
	h := homeHarness(t, 80)
	h.keys("/", "e", "x")
	out := h.screen()
	if !strings.Contains(out, "/ ex") {
		t.Errorf("no search bar at 80 columns:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if ansi.StringWidth(l) > 80 {
			t.Fatalf("line wider than 80: %d", ansi.StringWidth(l))
		}
	}
}

func TestFuzzy(t *testing.T) {
	for _, c := range []struct {
		q, s string
		ok   bool
	}{
		{"rte", "Rate-limit the export endpoint", true},
		{"exp end", "Rate-limit the export endpoint", true},
		{"ZZ", "Rate-limit", false},
		{"", "anything", true},
		{"tre", "Rate", false},                   // order matters
		{"rate", "draft MR · mark ready", false}, // scattered letters do not count
		{"rate", "mark ready", false},
		{"rf", "Draft", false}, // two letters must be a substring
		{"dr", "Draft", true},
	} {
		if got := fuzzyMatch(c.q, c.s); got != c.ok {
			t.Errorf("fuzzyMatch(%q, %q) = %v", c.q, c.s, got)
		}
	}
	a, _ := fuzzyScore("stand", "Standup")
	b, _ := fuzzyScore("stand", "Set a weight and so on")
	if a <= b {
		t.Errorf("a prefix run (%d) must outscore a scattered match (%d)", a, b)
	}
}
