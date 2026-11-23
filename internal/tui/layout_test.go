package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/project"
)

// The layout matrix: every page and dialog at the terminal sizes people run
// ADE at. A frame is exactly the screen (no row over, none missing), no line
// is wider than the screen, and where it is cut it is cut with an ellipsis.

var layoutSizes = [][2]int{{60, 20}, {80, 24}, {100, 30}, {110, 30}, {120, 24}, {160, 45}, {200, 50}}

type layoutScenario struct {
	name  string
	build func(t *testing.T) model
}

func layoutScenarios() []layoutScenario {
	home := func(keys ...string) func(t *testing.T) model {
		return func(t *testing.T) model {
			h := homeHarness(t, 120)
			h.m.homeLoaded = true
			h.m.syncing = false
			h.keys(keys...)
			return h.m
		}
	}
	mr := func(tab string) func(t *testing.T) model {
		return func(t *testing.T) model {
			e := newMREnv(t, fake.Premium)
			e.open(grp + "/tracker#1")
			e.press("6", "5", "4", "3", "2", "1")
			e.m.mr.item.Issue.Description = richDescription
			e.m.mr.data.mr.Description = richDescription
			e.press(tab)
			return e.m
		}
	}
	sw := func(confirm bool) func(t *testing.T) model {
		return func(t *testing.T) model {
			e := newSWEnv(t)
			e.selectRow(trk(3).String())
			e.press("s")
			if confirm {
				e.press("enter")
			}
			return e.m
		}
	}
	scs := []layoutScenario{
		{"home", home()},
		{"home_tab", home("tab")},
		{"home_search", home("/", "b")},
		{"home_help", home("?")},
		{"home_palette", home(":")},
		{"home_activity", home("e")},
		{"worktrees", home("t")},
		{"mr_help", func(t *testing.T) model { m := mr("1")(t); m.help = true; return m }},
		{"startwork", sw(false)},
		{"startwork_confirm", sw(true)},
		{"classify", func(t *testing.T) model { e := newClassifyEnv(t); e.press("c"); return e.m }},
		{"weight", func(t *testing.T) model { e := newWeightEnv(t, "weight::5"); e.open(model.openWeight); return e.m }},
		{"notes", func(t *testing.T) model { e, _ := newNotesEnv(t); e.press("N"); return e.m }},
		{"sprint_move", func(t *testing.T) model { e := newMoveEnv(t); e.press(">"); return e.m }},
		{"budget", func(t *testing.T) model { return budModel(unicodeGlyphs, 120, 40) }},
		{"standup", func(t *testing.T) model {
			m := agileModel(unicodeGlyphs)
			m.ag = agileUI{kind: agileStandup, standup: standupFixture()}
			return m
		}},
		{"sprint_review", func(t *testing.T) model {
			m := agileModel(unicodeGlyphs)
			m.ag = agileUI{kind: agileReview, review: reviewFixture()}
			return m
		}},
		{"notifications", func(t *testing.T) model {
			m := home()(t)
			now := time.Now()
			for i, sev := range []events.Severity{events.Error, events.Warn, events.Info, events.Info} {
				e := events.New(fmt.Sprintf("ci.failed%d", i), sev, "web!1 pipeline failed: lint with a fairly long title to see wrapping")
				e.Project, e.Subject, e.Time = "demo", "web", now.Add(-time.Duration(i)*time.Hour)
				m.nc.items = append(m.nc.items, notify.Item{Event: e})
			}
			m.nc.unread, m.nc.open = 3, true
			return m
		}},
		{"toasts", func(t *testing.T) model {
			m := home()(t)
			for _, sev := range []events.Severity{events.Error, events.Warn, events.Info} {
				e := events.New("ci.failed", sev, "web!1 pipeline failed: lint with a fairly long title")
				e.Project, e.Subject = "demo", "web"
				m.nc.push(e)
			}
			return m
		}},
		{"header_busy", func(t *testing.T) model {
			m := home()(t)
			m.updating, m.syncing = true, true
			m.progress.Total, m.progress.Done = 4, 2
			m.chip = aiChip{ok: true, period: "day", spent: 1.7, limit: 2}
			m.nc.unread = 12
			return m
		}},
	}
	for _, tab := range []string{"1", "2", "3", "4", "5", "6"} {
		scs = append(scs, layoutScenario{"mr" + tab, mr(tab)})
	}
	return scs
}

func TestLayoutMatrixFitsEveryScreen(t *testing.T) {
	useUTC(t)
	for _, sc := range layoutScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			m := sc.build(t)
			for _, sz := range layoutSizes {
				m.w, m.h = sz[0], sz[1]
				frame := ansi.Strip(m.render())
				lines := strings.Split(frame, "\n")
				if len(lines) != sz[1] {
					t.Errorf("%dx%d: the frame is %d rows, want %d:\n%s", sz[0], sz[1], len(lines), sz[1], frame)
				}
				for i, l := range lines {
					if lipgloss.Width(l) > sz[0] {
						t.Errorf("%dx%d: row %d is %d cells wide: %q", sz[0], sz[1], i+1, lipgloss.Width(l), l)
					}
				}
			}
		})
	}
}

// A page that hides its detail pane says which key brings it back, at every
// size where it is hidden.
func TestSinglePaneFootersNameTheKeyToTheDetail(t *testing.T) {
	useUTC(t)
	h := homeHarness(t, 80)
	h.m.homeLoaded = true
	for _, w := range []int{60, 80, 100, twoPaneMinW - 1} {
		if foot := ansi.Strip(h.m.footer(w)); !strings.Contains(foot, "tab details") || !strings.Contains(foot, "? keys") {
			t.Errorf("Home footer at %d columns: %q", w, foot)
		}
	}
	h.m.page = pageTrees
	for _, w := range []int{60, twoPaneMinW - 1} {
		if foot := ansi.Strip(h.m.footer(w)); !strings.Contains(foot, "tab details") || !strings.Contains(foot, "? keys") {
			t.Errorf("Worktrees footer at %d columns: %q", w, foot)
		}
	}
	for _, w := range []int{60, twoPaneMinW - 1} {
		m := budModel(unicodeGlyphs, w, 24)
		if foot := ansi.Strip(m.footer(w)); !strings.Contains(foot, "tab details") || !strings.Contains(foot, "b back") || !strings.Contains(foot, "? keys") {
			t.Errorf("Budget footer at %d columns: %q", w, foot)
		}
		m.page, m.lg = pageLog, logState{loaded: true}
		if foot := ansi.Strip(m.footer(w)); !strings.Contains(foot, "tab details") || !strings.Contains(foot, "L back") || !strings.Contains(foot, "? keys") {
			t.Errorf("Log footer at %d columns: %q", w, foot)
		}
	}
	// And the wide footers still carry what they did.
	if foot := ansi.Strip(h.m.footer(200)); !strings.Contains(foot, "q quit") {
		t.Errorf("wide footer lost q quit: %q", foot)
	}
}

// Every footer stays on one line and keeps the way out and the help, however
// narrow the screen: hints go, not those two.
func TestFootersKeepTheHelpWhenTheScreenIsNarrow(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	m.ag = agileUI{kind: agileStandup, standup: standupFixture()}
	for _, w := range []int{60, 80, 100, 120, 200} {
		for _, kind := range []agileKind{agileStandup, agileReview} {
			m.ag.kind = kind
			foot := ansi.Strip(m.agileFooter(w))
			if lipgloss.Width(foot) > w || !strings.Contains(foot, "esc back") || !strings.Contains(foot, "? keys") {
				t.Errorf("agile footer at %d columns: %q", w, foot)
			}
		}
	}
	m.nc.open = true
	for _, w := range []int{60, 80, 120} {
		foot := ansi.Strip(m.footer(w))
		if lipgloss.Width(foot) > w || !strings.Contains(foot, "esc close") {
			t.Errorf("notification centre footer at %d columns: %q", w, foot)
		}
	}
}

// Where a line cannot hold everything, the header drops the project count,
// then the sync activity, then the AI chip; the unread count stays.
func TestHeaderDropsSegmentsByPriority(t *testing.T) {
	h := homeHarness(t, 120)
	m := h.m
	m.updating = true
	m.progress.Total, m.progress.Done = 4, 2
	m.chip = aiChip{ok: true, period: "day", spent: 1.7, limit: 2}
	m.nc.unread = 12
	m.projects = append(m.projects, project.ProjectStatus{})
	for _, c := range []struct {
		w    int
		want []string
		not  []string
	}{
		{160, []string{"1 project", "updating 2/4", "AI $1.70", "12 unread"}, nil},
		{90, []string{"updating 2/4", "AI $1.70", "12 unread"}, []string{"1 project"}},
		{70, []string{"AI $1.70", "12 unread"}, []string{"1 project", "updating"}},
		{50, []string{"12 unread"}, []string{"AI $1.70", "updating", "1 project"}},
	} {
		got := ansi.Strip(m.header(c.w))
		if lipgloss.Width(got) > c.w {
			t.Errorf("header at %d columns is %d wide: %q", c.w, lipgloss.Width(got), got)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("header at %d columns lacks %q: %q", c.w, w, got)
			}
		}
		for _, n := range c.not {
			if strings.Contains(got, n) {
				t.Errorf("header at %d columns still has %q: %q", c.w, n, got)
			}
		}
	}
	// The page name is cut with an ellipsis only after everything else went.
	m.mr = &mrPage{}
	if got := ansi.Strip(m.header(20)); lipgloss.Width(got) > 20 || !strings.Contains(got, "12 unread") {
		t.Errorf("header at 20 columns: %q", got)
	}
}

// A dialog taller than the screen loses its middle, not its key row, and says so.
func TestClipDialogKeepsTheTitleAndTheKeyRow(t *testing.T) {
	m := model{g: unicodeGlyphs}
	var body []string
	for i := range 20 {
		body = append(body, fmt.Sprintf("row %d", i))
	}
	body = append(body, "", "enter ok   esc cancel")
	d := m.dialog("Title", body, 40)
	lines := strings.Split(d, "\n")
	got := clipDialog(unicodeGlyphs, lines, 12)
	if len(got) != 12 {
		t.Fatalf("clipped to %d rows, want 12:\n%s", len(got), ansi.Strip(strings.Join(got, "\n")))
	}
	out := ansi.Strip(strings.Join(got, "\n"))
	for _, want := range []string{"Title", "more rows", "enter ok   esc cancel", "╰"} {
		if !strings.Contains(out, want) {
			t.Errorf("clipped dialog lacks %q:\n%s", want, out)
		}
	}
	for _, l := range got {
		if lipgloss.Width(l) != 40 {
			t.Errorf("row %q is %d wide, want 40", ansi.Strip(l), lipgloss.Width(l))
		}
	}
	// Blank spacer rows go before content does.
	spaced := m.dialog("T", []string{"a", "", "b", "", "c", "enter ok"}, 20)
	if got := clipDialog(unicodeGlyphs, strings.Split(spaced, "\n"), 9); len(got) != 9 || strings.Contains(ansi.Strip(strings.Join(got, "\n")), "more rows") {
		t.Errorf("spacers were not dropped first:\n%s", ansi.Strip(strings.Join(got, "\n")))
	}
}

// Help taller than the screen scrolls: j/k move it, every key is reachable, esc closes.
func TestHelpScrollsOnAShortScreen(t *testing.T) {
	for _, onMR := range []bool{false, true} {
		var m model
		if onMR {
			m = buildScenario(t, "mr1")
			if m.mr == nil {
				t.Fatal("scenario mr1 is not the MR page")
			}
		} else {
			h := homeHarness(t, 60)
			h.m.homeLoaded = true
			m = h.m
		}
		m.w, m.h = 60, 20
		m, _ = press(m, "?")
		if !m.help || m.helpMax() == 0 {
			t.Fatalf("mr=%v: help max %d: a 60x20 screen needs scrolling", onMR, m.helpMax())
		}
		first := ansi.Strip(m.render())
		if !strings.Contains(first, "more · j/k scrolls") || !strings.Contains(ansi.Strip(m.footer(60)), "j/k scroll") {
			t.Errorf("mr=%v: no scroll cue:\n%s", onMR, first)
		}
		m, _ = press(m, "G")
		last := ansi.Strip(m.render())
		if m.helpTop != m.helpMax() || !strings.Contains(last, "above") || strings.Contains(last, "more · j/k") {
			t.Errorf("mr=%v: G did not reach the end (top %d of %d):\n%s", onMR, m.helpTop, m.helpMax(), last)
		}
		if lines := strings.Count(last, "\n") + 1; lines != 20 {
			t.Errorf("mr=%v: the frame is %d rows, want 20", onMR, lines)
		}
		m, _ = press(m, "g")
		if m.helpTop != 0 {
			t.Errorf("mr=%v: g left the window at %d", onMR, m.helpTop)
		}
		m, _ = press(m, "esc")
		if m.help {
			t.Errorf("mr=%v: esc did not close the help", onMR)
		}
		m, _ = press(m, "?")
		if m.helpTop != 0 {
			t.Errorf("mr=%v: help reopened scrolled to %d", onMR, m.helpTop)
		}
	}
	// A tall screen shows it whole.
	h := homeHarness(t, 160)
	h.m.w, h.m.h, h.m.help = 160, 45, true
	if h.m.helpMax() != 0 {
		t.Errorf("help scrolls on a 160x45 screen")
	}
}

// Pop-ups: one box on a short screen, two on a medium one, three on a tall one.
func TestToastLimitFollowsTheHeight(t *testing.T) {
	for _, c := range []struct{ h, want int }{{16, 1}, {23, 1}, {24, 2}, {31, 2}, {32, 3}, {50, 3}} {
		if got := toastLimit(c.h); got != c.want {
			t.Errorf("toastLimit(%d) = %d, want %d", c.h, got, c.want)
		}
	}
	m := buildScenario(t, "toasts") // three pop-ups queued
	m.w, m.h = 80, 20
	out := ansi.Strip(m.render())
	if strings.Count(out, "pipeline failed") != 1 || !strings.Contains(out, "+2 earlier") {
		t.Errorf("a 20-row screen should show one pop-up and count the rest:\n%s", out)
	}
}

// The preview keeps the court line and the notes on a short screen by drawing
// the station list without its connector rows.
func TestPreviewStationsGoCompactWhenThePaneIsShort(t *testing.T) {
	h := homeHarness(t, 120)
	h.m.homeLoaded = true
	h.m.w, h.m.h = 120, 24
	pane := ansi.Strip(h.m.homeDetail(40, 12))
	if strings.Contains(pane, unicodeGlyphs.Vertical) || !strings.Contains(pane, "merged") || !strings.Contains(pane, "you · 2 unresolved threads") {
		t.Errorf("compact stations lost something or kept connectors:\n%s", pane)
	}
	whole := ansi.Strip(h.m.homeDetail(40, 40))
	if !strings.Contains(whole, unicodeGlyphs.Vertical) {
		t.Errorf("a tall pane should keep the connectors:\n%s", whole)
	}
	// The MR page's overview never goes compact: it scrolls.
	it := h.m.homeRows[h.m.homeSel].item
	if it == nil {
		t.Skip("selected row has no item")
	}
	if full := ansi.Strip(h.m.itemDetail(it, 40, false)); !strings.Contains(full, unicodeGlyphs.Vertical) {
		t.Errorf("itemDetail lost its connectors:\n%s", full)
	}
}

// Home's list never gives the title less than minTitleW cells while the court
// column has room to give.
func TestHomeTitleKeepsAReadableWidthWhenNarrow(t *testing.T) {
	h := homeHarness(t, 80)
	h.m.homeLoaded = true
	for _, w := range []int{60, 66, 72, 80} {
		out := ansi.Strip(h.m.homeList(w, 20))
		if !strings.Contains(out, "Rate-limit the") {
			t.Errorf("list at %d columns cut the title to a few letters:\n%s", w, out)
		}
	}
}

// A tab bar wider than the screen drops the other tabs' badges, then their names.
func TestMRTabBarFitsNarrowScreens(t *testing.T) {
	m := buildScenario(t, "mr2") // the Threads tab
	if m.mr == nil {
		t.Fatal("not the MR page")
	}
	for _, w := range []int{60, 40, 30, 24} {
		bar := ansi.Strip(m.mrTabs(w))
		if lipgloss.Width(bar) > w {
			t.Errorf("tab bar at %d columns is %d wide: %q", w, lipgloss.Width(bar), bar)
		}
		if !strings.Contains(bar, "[Threads") {
			t.Errorf("tab bar at %d columns lost the current tab: %q", w, bar)
		}
	}
	if bar := ansi.Strip(m.mrTabs(30)); strings.Contains(bar, "History") || !strings.Contains(bar, "6") {
		t.Errorf("at 30 columns the other tabs should be their numbers: %q", bar)
	}
}

func TestBreakpointsAreOrdered(t *testing.T) {
	if minW >= twoPaneMinW || twoPaneMinW >= wideMinW {
		t.Fatalf("breakpoints out of order: %d %d %d", minW, twoPaneMinW, wideMinW)
	}
	// The list at the first two-pane width leaves its title cell usable, the
	// preview at least 34 cells.
	if listW := twoPaneMinW * 66 / 100; listW < 72 || twoPaneMinW-listW-4 < 34 {
		t.Errorf("two-pane at %d columns: list %d, preview %d", twoPaneMinW, listW, twoPaneMinW-listW-4)
	}
	if narrowW(twoPaneMinW) || !narrowW(twoPaneMinW-1) || !shortScreen(shortH-1) || shortScreen(shortH) {
		t.Error("predicates disagree with the constants")
	}
}

func buildScenario(t *testing.T, name string) model {
	t.Helper()
	for _, sc := range layoutScenarios() {
		if sc.name == name {
			return sc.build(t)
		}
	}
	t.Fatalf("no scenario %q", name)
	return model{}
}
