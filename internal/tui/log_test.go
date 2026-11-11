package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
)

// logFixture writes a small audit log over several windows (the oldest lands
// in an earlier month's file) and returns it with the time it is relative to.
func logFixture(t *testing.T) (*audit.Log, time.Time) {
	t.Helper()
	l, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var seed []audit.Entry
	add := func(ago time.Duration, e audit.Entry) {
		e.TS = now.Add(-ago)
		seed = append(seed, e)
	}
	add(10*time.Minute, audit.Entry{Action: "forge.thread.reply", Target: "g/api!1", Item: "g/tracker#1", Params: map[string]any{"chars": 120, "discussion": "abc123"}})
	add(20*time.Minute, audit.Entry{
		Action: "ai.summarize", Target: "g/tracker#1", Item: "g/tracker#1",
		AI: &audit.AIInfo{
			Profile: "claude-sub", Driver: "omp", Model: "haiku-x", TokensIn: 1840, TokensOut: 160, TokensCache: 20,
			CostUSD: 0.0026, CostSource: "reported", Artifact: "items/work/g/tracker/1/ai/summary.md", DestHost: "api.example.com", Bytes: 2048,
		},
	})
	add(30*time.Minute, audit.Entry{
		Action: "ai.summarize", Target: "g/tracker#3", Item: "g/tracker#3",
		AI: &audit.AIInfo{Profile: "claude-sub", Driver: "omp", Model: "haiku-x", CostSource: "reported", CacheHit: true},
	})
	add(40*time.Minute, audit.Entry{Action: "forge.mr.merge", Target: "g/api!1", Item: "g/tracker#1", Outcome: "error", Error: "405 Method Not Allowed: pipeline must succeed"})
	add(50*time.Minute, audit.Entry{Action: "forge.issue.create", Target: "g/tracker#9", DryRun: true, Outcome: "skipped"})
	add(2*time.Hour, audit.Entry{Action: "review.comment.accept", Target: "draft-1", Item: "g/api!1"})
	add(3*time.Hour, audit.Entry{Action: "budget.stop", Target: "g/tracker#1", Item: "g/tracker#1", Outcome: "error", Error: "Budget has been exceeded"})
	add(5*time.Hour, audit.Entry{Action: "git.pull", Target: "demo/api/main", Project: "demo"})
	add(3*24*time.Hour, audit.Entry{Action: "project.add", Target: "manifest.toml"})
	add(40*24*time.Hour, audit.Entry{Action: "git.clone", Target: "demo/web", Project: "demo"})
	// The log is appended in time order, which Query's limit relies on.
	slices.SortFunc(seed, func(a, b audit.Entry) int { return a.TS.Compare(b.TS) })
	for _, e := range seed {
		if err := l.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	return l, now
}

// openLogPage presses L on a model over l and applies the load.
func openLogPage(t *testing.T, g glyphs, l *audit.Log, w, h int) model {
	t.Helper()
	m := model{g: g, nc: newNotifCenter(nil), w: w, h: h, page: pageHome}
	m.lg.log = l
	m, cmd := press(m, "L")
	if m.page != pageLog || cmd == nil || !m.lg.loading {
		t.Fatalf("L: page %d cmd %v loading %v", m.page, cmd != nil, m.lg.loading)
	}
	return applyLog(t, m, cmd)
}

func applyLog(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	next, _ := m.Update(cmd())
	m = next.(model)
	if m.lg.loading {
		t.Fatal("still loading after the answer")
	}
	return m
}

func visActions(m model) []string {
	var out []string
	for _, i := range m.lg.vis {
		out = append(out, m.lg.entries[i].Action)
	}
	return out
}

func special(m model, k tea.KeyPressMsg) model {
	next, _ := m.key(k)
	return next.(model)
}

func typeText(m model, s string) model {
	for _, r := range s {
		m, _ = press(m, string(r))
	}
	return m
}

func TestLogListsNewestFirstWithinTheDefaultWindow(t *testing.T) {
	l, _ := logFixture(t)
	m := openLogPage(t, unicodeGlyphs, l, 120, 40)
	want := "forge.thread.reply ai.summarize ai.summarize forge.mr.merge forge.issue.create review.comment.accept budget.stop git.pull"
	if got := strings.Join(visActions(m), " "); got != want {
		t.Errorf("entries (1d, newest first):\n got  %s\n want %s", got, want)
	}
	if m.lg.since != sinceDay {
		t.Errorf("default window is 1d, got %s", m.lg.since)
	}

	empty, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	m = openLogPage(t, unicodeGlyphs, empty, 120, 40)
	assertHas(t, "empty log", ansi.Strip(m.render()), "No audit entries in the last 1d. s widens the window.")
}

func TestLogFilters(t *testing.T) {
	l, _ := logFixture(t)
	count := func(m model) int { return len(m.lg.vis) }

	t.Run("text matches action, target, item and error, ignoring case", func(t *testing.T) {
		for _, c := range []struct {
			q    string
			want int
		}{
			{"thread", 1},        // action
			{"tracker#1", 4},     // target or item
			{"BUDGET", 1},        // action and error, one entry
			{"pipeline must", 1}, // error text
			{"draft-1", 1},       // target
			{"zzz", 0},
		} {
			m := openLogPage(t, unicodeGlyphs, l, 120, 40)
			m, _ = press(m, "/")
			if !m.lg.editing {
				t.Fatal("/ opens the filter prompt")
			}
			m = typeText(m, c.q)
			if got := count(m); got != c.want {
				t.Errorf("%q matches %d entries, want %d: %v", c.q, got, c.want, visActions(m))
			}
		}
	})

	t.Run("action groups cycle with a", func(t *testing.T) {
		m := openLogPage(t, unicodeGlyphs, l, 120, 40)
		for _, c := range []struct {
			group string
			want  []string
		}{
			{"forge.*", []string{"forge.thread.reply", "forge.mr.merge", "forge.issue.create"}},
			{"ai.*", []string{"ai.summarize", "ai.summarize"}},
			{"review.*", []string{"review.comment.accept"}},
			{"budget.*", []string{"budget.stop"}},
			{"git.* project.*", []string{"git.pull"}},
			{"errors only", []string{"forge.mr.merge", "budget.stop"}},
			{"all", nil},
		} {
			m, _ = press(m, "a")
			if m.lg.group.String() != c.group {
				t.Fatalf("group = %s, want %s", m.lg.group, c.group)
			}
			if c.want != nil && strings.Join(visActions(m), ",") != strings.Join(c.want, ",") {
				t.Errorf("%s shows %v, want %v", c.group, visActions(m), c.want)
			}
		}
		if count(m) != 8 {
			t.Errorf("all shows %d entries, want 8", count(m))
		}
	})

	t.Run("filters combine and c clears them", func(t *testing.T) {
		m := openLogPage(t, unicodeGlyphs, l, 120, 40)
		for range 6 { // errors only
			m, _ = press(m, "a")
		}
		m, _ = press(m, "/")
		m = typeText(m, "merge")
		if got := visActions(m); len(got) != 1 || got[0] != "forge.mr.merge" {
			t.Errorf("errors only + merge = %v", got)
		}
		m = special(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.lg.editing || m.lg.text != "merge" {
			t.Errorf("enter keeps the filter and closes the prompt: editing %v text %q", m.lg.editing, m.lg.text)
		}
		assertHas(t, "filtered list", ansi.Strip(m.render()), "1 of 8 entries", "actions errors only", `matching "merge"`)
		m, _ = press(m, "c")
		if m.lg.group != groupAll || m.lg.text != "" || count(m) != 8 {
			t.Errorf("c clears: group %s text %q shown %d", m.lg.group, m.lg.text, count(m))
		}
	})

	t.Run("since cycles 7d, all, 1h, 1d and reloads", func(t *testing.T) {
		m := openLogPage(t, unicodeGlyphs, l, 120, 40)
		for _, c := range []struct {
			since string
			want  int
		}{{"7d", 9}, {"all", 10}, {"1h", 5}, {"1d", 8}} {
			var cmd tea.Cmd
			m, cmd = press(m, "s")
			if m.lg.since.String() != c.since || cmd == nil {
				t.Fatalf("since = %s cmd %v, want %s", m.lg.since, cmd != nil, c.since)
			}
			m = applyLog(t, m, cmd)
			if got := count(m); got != c.want {
				t.Errorf("since %s shows %d entries, want %d: %v", c.since, got, c.want, visActions(m))
			}
		}
	})

	t.Run("the prompt takes every key; esc restores the previous text", func(t *testing.T) {
		m := openLogPage(t, unicodeGlyphs, l, 120, 40)
		m, _ = press(m, "/")
		m = typeText(m, "ai")
		m = special(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		m, _ = press(m, "/")
		if m.lg.prev != "ai" {
			t.Fatalf("prev = %q", m.lg.prev)
		}
		m, cmd := press(m, "q") // not quit: it is text
		if cmd != nil || m.lg.text != "aiq" || count(m) != 0 {
			t.Errorf("q in the prompt: text %q shown %d cmd %v", m.lg.text, count(m), cmd != nil)
		}
		assertHas(t, "prompt footer", ansi.Strip(m.footer(120)), "/ aiq", "enter apply", "esc cancel")
		m = special(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
		if m.lg.text != "ai" || count(m) != 3 { // ai.summarize twice, and git.pull's demo/api/main
			t.Errorf("backspace: text %q shown %d", m.lg.text, count(m))
		}
		m = special(m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
		if m.lg.text != "" || count(m) != 8 {
			t.Errorf("ctrl+u: text %q shown %d", m.lg.text, count(m))
		}
		m = special(m, tea.KeyPressMsg{Code: tea.KeyEscape})
		if m.lg.editing || m.lg.text != "ai" || count(m) != 3 {
			t.Errorf("esc: editing %v text %q shown %d", m.lg.editing, m.lg.text, count(m))
		}
	})
}

func TestLogSelectionSurvivesFiltering(t *testing.T) {
	l, _ := logFixture(t)
	m := openLogPage(t, unicodeGlyphs, l, 120, 40)
	for range 3 {
		m, _ = press(m, "j")
	}
	sel, _ := m.lg.selected()
	if sel.Action != "forge.mr.merge" {
		t.Fatalf("selected %s", sel.Action)
	}
	m, _ = press(m, "a") // forge.*: the selected entry stays selected
	if got, _ := m.lg.selected(); got.ID != sel.ID {
		t.Errorf("selection moved from %s to %s when the filter changed", sel.Action, got.Action)
	}
}

func TestLogPageRendersAtEachSize(t *testing.T) {
	useUTC(t)
	l, now := logFixture(t)
	today := fmtDay(now.Local().Format("2006-01-02"))
	cases := []struct {
		name string
		g    glyphs
		w, h int
		env  bool
		want []string
	}{
		{"80x24", unicodeGlyphs, 80, 24, false, []string{"ADE", "Log", "8 entries", "since 1d", today, "forge.thread.reply", "✓ ok", "✕ error", "$0.0026", "✓ ok · cache hit", "○ skipped · dry run"}},
		{"120x40", unicodeGlyphs, 120, 40, false, []string{"Log", "forge.mr.merge", "✕ error", "$0.0026", "cache hit", "│", "forge.thread.reply", "Params", `"chars": 120`}},
		{"80x24 ascii", asciiGlyphs, 80, 24, false, []string{"x error", "+ ok", "o skipped - dry run", "+ ok - cache hit"}},
		{"120x40 NO_COLOR ascii", asciiGlyphs, 120, 40, true, []string{"x error", "+ ok - $0.0026", "o skipped - dry run"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.env {
				t.Setenv("NO_COLOR", "1")
			}
			m := openLogPage(t, c.g, l, c.w, c.h)
			out := ansi.Strip(m.render())
			if got := len(strings.Split(out, "\n")); got != c.h {
				t.Errorf("%d lines, want %d", got, c.h)
			}
			if got := widest(out); got > c.w {
				t.Errorf("a line is %d cells wide, over %d:\n%s", got, c.w, out)
			}
			assertHas(t, c.name, out, c.want...)
		})
	}
}

// The right pane shows every field of the selection, the ai block included.
func TestLogDetailShowsEveryField(t *testing.T) {
	useUTC(t)
	l, _ := logFixture(t)
	m := openLogPage(t, unicodeGlyphs, l, 140, 50)
	m, _ = press(m, "j") // ai.summarize with the full ai block
	out := ansi.Strip(m.render())
	kv := func(k, v string) string { return pad(k, 10) + v }
	assertHas(t, "ai detail", out,
		"ai.summarize", "✓ ok",
		kv("actor", "user"), kv("target", "g/tracker#1"), kv("item", "g/tracker#1"), kv("outcome", "ok"),
		"AI", kv("profile", "claude-sub"), kv("driver", "omp"), kv("model", "haiku-x"),
		kv("tokens", "1840 in · 160 out · 20 cache"), kv("cost", "$0.0026 (reported)"), kv("cache hit", "no"),
		kv("artifact", "items/work/g/tracker/1/ai/summary.md"), kv("dest host", "api.example.com"), kv("bytes", "2048"),
		"enter: nothing on Home for this entry",
	)
	m, _ = press(m, "j") // the cache hit
	assertHas(t, "cache hit detail", ansi.Strip(m.render()), kv("cache hit", "yes"), kv("cost", "$0.00 (reported)"))
	m, _ = press(m, "j") // the failed merge
	assertHas(t, "error detail", ansi.Strip(m.render()), "✕ error", kv("error", "405 Method Not Allowed"))
	m, _ = press(m, "j") // the dry run
	assertHas(t, "dry run detail", ansi.Strip(m.render()), kv("dry run", "yes, nothing was changed"), "○ skipped")
}

// A long value is wrapped, not cut with an ellipsis; J/K scroll a long pane.
func TestLogDetailWrapsLongValuesAndScrolls(t *testing.T) {
	l, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	long := "https://example.com/" + strings.Repeat("a-very-long-path-segment/", 8) + "end"
	params := map[string]any{}
	for i := range 30 {
		params[string(rune('a'+i%26))+string(rune('a'+i/26))] = i
	}
	if err := l.Append(audit.Entry{TS: time.Now(), Action: "forge.mr.create", Target: "g/api!2", URL: long, Params: params}); err != nil {
		t.Fatal(err)
	}
	m := openLogPage(t, unicodeGlyphs, l, 120, 24)
	out := ansi.Strip(m.render())
	if !strings.Contains(out, "end") || widest(out) > 120 {
		t.Errorf("the whole url shows, wrapped, within the width:\n%s", out)
	}
	if strings.Contains(out, `"za"`) || !strings.Contains(out, "Params") {
		t.Errorf("a long pane is cut at the bottom:\n%s", out)
	}
	before := ansi.Strip(m.render())
	for range 12 {
		m, _ = press(m, "J")
	}
	if after := ansi.Strip(m.render()); after == before || strings.Contains(after, "id        ") {
		t.Errorf("J scrolls the detail pane:\n%s", after)
	}
	for range 20 {
		m, _ = press(m, "K")
	}
	if got := ansi.Strip(m.render()); got != before {
		t.Errorf("K scrolls back to the top")
	}
}

func logHomeModel(t *testing.T, l *audit.Log) model {
	t.Helper()
	res := sampleResult()
	res.Items[1].MRs = []forge.MR{{Ref: forge.Ref{Project: "g/api", IID: 1, Kind: forge.KindMR}, Title: "Rate-limit export", State: "opened"}}
	res.Items[1].Children = []forge.Issue{{Ref: forge.Ref{Project: "g/api", IID: 7}, Title: "Child"}}
	m := openLogPage(t, unicodeGlyphs, l, 120, 40)
	m.setResults([]forgesync.Result{res})
	return m
}

func selectAction(t *testing.T, m model, action string) model {
	t.Helper()
	for i, idx := range m.lg.vis {
		if m.lg.entries[idx].Action == action {
			m.lg.sel = i
			return m
		}
	}
	t.Fatalf("no %s entry in %v", action, visActions(m))
	return m
}

func TestLogEnterJumpsToTargets(t *testing.T) {
	l, _ := logFixture(t)

	t.Run("a work item is selected on Home", func(t *testing.T) {
		m := selectAction(t, logHomeModel(t, l), "ai.summarize") // target g/tracker#1
		m, _ = press(m, "enter")
		if m.page != pageHome || m.homeRows[m.homeSel].ref() != "g/tracker#1" || m.mr != nil {
			t.Errorf("page %d selected %q mr open %v", m.page, m.homeRows[m.homeSel].ref(), m.mr != nil)
		}
	})

	t.Run("an MR opens its page", func(t *testing.T) {
		m := selectAction(t, logHomeModel(t, l), "forge.thread.reply") // target g/api!1
		m, cmd := press(m, "enter")
		if m.page != pageHome || m.mr == nil || m.mr.data == nil || m.mr.data.ref.String() != "g/api!1" || cmd == nil {
			t.Fatalf("page %d mr %v cmd %v", m.page, m.mr != nil, cmd != nil)
		}
		if m.homeRows[m.homeSel].ref() != "g/tracker#1" {
			t.Errorf("Home has %q selected behind the MR page", m.homeRows[m.homeSel].ref())
		}
	})

	t.Run("an entry whose target is not on Home shows its detail", func(t *testing.T) {
		m := selectAction(t, logHomeModel(t, l), "git.pull")
		m, cmd := press(m, "enter")
		if m.page != pageLog || !m.showDetail || cmd != nil {
			t.Errorf("page %d showDetail %v cmd %v", m.page, m.showDetail, cmd != nil)
		}
		m.w, m.h = 80, 24
		assertHas(t, "narrow detail", ansi.Strip(m.render()), "git.pull", "target    demo/api/main", "enter: nothing on Home")
		m, _ = press(m, "esc")
		if m.page != pageLog || m.showDetail {
			t.Errorf("esc closes the detail first: page %d showDetail %v", m.page, m.showDetail)
		}
	})

	t.Run("the item field is the fallback", func(t *testing.T) {
		m := selectAction(t, logHomeModel(t, l), "review.comment.accept") // target draft-1, item g/api!1
		m, _ = press(m, "enter")
		if m.mr == nil || m.mr.data.ref.String() != "g/api!1" {
			t.Errorf("enter used the target instead of the item: mr %v", m.mr != nil)
		}
	})

	t.Run("a Home filter that hides the item is cleared", func(t *testing.T) {
		m := logHomeModel(t, l)
		m.homeFilter = hfBlocked
		m.rebuildHome("")
		if len(m.homeRows) != 1 {
			t.Fatalf("blocked filter shows %d rows", len(m.homeRows))
		}
		m = selectAction(t, m, "budget.stop") // target g/tracker#1
		m, _ = press(m, "enter")
		if m.homeFilter != hfAll || m.homeRows[m.homeSel].ref() != "g/tracker#1" {
			t.Errorf("filter %d selected %q", m.homeFilter, m.homeRows[m.homeSel].ref())
		}
	})
}

func TestHomeTargetFor(t *testing.T) {
	l, _ := logFixture(t)
	m := logHomeModel(t, l)
	for _, c := range []struct {
		ref    string
		ok     bool
		openMR bool
		row    string
	}{
		{"g/tracker#3", true, false, "g/tracker#3"},
		{"work:g/tracker#3", true, false, "g/tracker#3"}, // the plan's prefix
		{"g/api#7", true, false, "g/tracker#1"},          // a child issue selects its tracker item
		{"g/api!1", true, true, "g/tracker#1"},           // an MR opens its page
		{"g/tracker#99", false, false, ""},
		{"", false, false, ""},
	} {
		got, ok := m.homeTargetFor(c.ref)
		if ok != c.ok {
			t.Errorf("%q: ok = %v", c.ref, ok)
			continue
		}
		if ok && (got.openMR != c.openMR || m.homeRows[got.row].ref() != c.row) {
			t.Errorf("%q: openMR %v row %q, want %v %q", c.ref, got.openMR, m.homeRows[got.row].ref(), c.openMR, c.row)
		}
	}
}

func TestLogKeysLeaveAndReturn(t *testing.T) {
	l, _ := logFixture(t)
	m := openLogPage(t, unicodeGlyphs, l, 120, 40)
	m, _ = press(m, "L")
	if m.page != pageHome {
		t.Errorf("L again returns to Home, page %d", m.page)
	}
	m.page = pageTrees
	m, _ = press(m, "L")
	m, _ = press(m, "esc")
	if m.page != pageTrees {
		t.Errorf("esc returns to the page L was pressed on, page %d", m.page)
	}
	m.page = pageHome
	m, _ = press(m, "L")
	m, _ = press(m, "t")
	if m.page != pageTrees {
		t.Errorf("t from the Log over Home gives Worktrees, page %d", m.page)
	}
	// b from the Log opens Budget over the same page.
	m.page = pageHome
	m, _ = press(m, "L")
	m, _ = press(m, "b")
	if m.page != pageBudget {
		t.Fatalf("b from the Log: page %d", m.page)
	}
	m, _ = press(m, "esc")
	if m.page != pageHome {
		t.Errorf("esc from Budget (opened from the Log over Home) returns to Home, page %d", m.page)
	}
	// An unreadable log says so.
	m = model{g: unicodeGlyphs, nc: newNotifCenter(nil), w: 120, h: 40}
	m, cmd := press(m, "L")
	m = applyLog(t, m, cmd)
	assertHas(t, "no log", ansi.Strip(m.render()), "could not read the audit log", "no audit log", "r tries again")
}

func TestHistoryLoadsNewestFirstForOneItem(t *testing.T) {
	l, _ := logFixture(t)
	msg := loadHistory(l, "g/tracker#1", 0)().(historyMsg)
	var got []string
	for _, e := range msg.entries {
		got = append(got, e.Action)
	}
	if msg.err != nil || msg.item != "g/tracker#1" || strings.Join(got, " ") != "forge.thread.reply ai.summarize forge.mr.merge budget.stop" {
		t.Errorf("history = %v (item %q, err %v)", got, msg.item, msg.err)
	}
	msg = loadHistory(l, "g/tracker#1", 2)().(historyMsg)
	if len(msg.entries) != 2 || msg.entries[0].Action != "forge.thread.reply" || msg.entries[1].Action != "ai.summarize" {
		t.Errorf("limit 2 keeps the newest two: %+v", msg.entries)
	}
	if msg := loadHistory(nil, "g/tracker#1", 0)().(historyMsg); msg.err == nil {
		t.Errorf("a nil log is an error, not an empty history")
	}
}

func TestHistoryViewLines(t *testing.T) {
	l, _ := logFixture(t)
	es := loadHistory(l, "g/tracker#1", 0)().(historyMsg).entries
	for _, g := range []glyphs{unicodeGlyphs, asciiGlyphs} {
		out := ansi.Strip(historyView(g, es, 70, 8))
		if lines := strings.Split(out, "\n"); len(lines) != 8 || widest(out) > 70 {
			t.Errorf("view is %d lines, %d cells wide, want 8x70:\n%s", len(lines), widest(out), out)
		}
		ok, bad := map[bool]string{true: "✓", false: "+"}[g == unicodeGlyphs], map[bool]string{true: "✕", false: "x"}[g == unicodeGlyphs]
		assertHas(t, "history", out,
			"10m ago", ok+" forge thread reply", "ok",
			"ai summarize", "ok"+g.sep()+"$0.0026",
			bad+" forge mr merge", "error",
			bad+" budget stop")
		first := strings.Split(out, "\n")[0]
		if !strings.Contains(first, "forge thread reply") {
			t.Errorf("newest first, got %q", first)
		}
	}

	out := ansi.Strip(historyView(unicodeGlyphs, es, 60, 3))
	assertHas(t, "short history", out, "+2 earlier")
	if len(strings.Split(out, "\n")) != 3 {
		t.Errorf("height is exact:\n%s", out)
	}

	hit := audit.Entry{TS: time.Now().Add(-3 * time.Hour), Action: "ai.explain", AI: &audit.AIInfo{CacheHit: true}}
	assertHas(t, "cache hit", ansi.Strip(historyView(unicodeGlyphs, []audit.Entry{hit}, 60, 2)), "3h ago", "ai explain", "ok · cache hit")
	assertHas(t, "empty", ansi.Strip(historyView(unicodeGlyphs, nil, 60, 3)), "No recorded actions on this item yet.")
}

func TestHardWrapBreaksAPathAtSlashesNotMidWord(t *testing.T) {
	got := hardWrap("items/sandbox-gitlab/ade-demo-env/tracker#11/ai/summary.md", 24)
	for _, ln := range got {
		if lipgloss.Width(ln) > 24 {
			t.Errorf("line %q is wider than 24", ln)
		}
	}
	if strings.Join(got, "") != "items/sandbox-gitlab/ade-demo-env/tracker#11/ai/summary.md" {
		t.Errorf("nothing may be lost: %q", got)
	}
	for _, ln := range got[:len(got)-1] {
		if !strings.HasSuffix(ln, "/") {
			t.Errorf("%q does not end at a slash (in %q)", ln, got)
		}
	}
	// A segment wider than the pane is still shown whole across lines.
	if got := hardWrap("averyveryverylongsegmentwithoutanyslash", 12); strings.Join(got, "") != "averyveryverylongsegmentwithoutanyslash" {
		t.Errorf("%q", got)
	}
}

func TestDayRuleDropsTheCountWhenOneDayIsShown(t *testing.T) {
	if dayCount(1, 43) != nil {
		t.Error("a single day repeats the Log rule's count")
	}
	if got := dayCount(2, 43); len(got) != 1 || got[0] != "43 entries" {
		t.Errorf("%v", got)
	}
}

func TestNarrowLogFooterNamesWhatEnterGoesTo(t *testing.T) {
	l, _ := logFixture(t)
	m := openLogPage(t, unicodeGlyphs, l, 60, 20)
	f := ansi.Strip(m.logFooter(60))
	if !strings.Contains(f, "enter go to item") || strings.Contains(f, "go to ") && !strings.Contains(f, "go to item") {
		t.Errorf("footer %q", f)
	}
}

func TestLogErrorIsWrappedNotCut(t *testing.T) {
	m := model{g: unicodeGlyphs, nc: newNotifCenter(nil), w: 50, h: 20, page: pageLog}
	m.lg = logState{loaded: true, err: errors.New("open /tmp/ade-demo/data/ade/audit/2026-11.jsonl: permission denied")}
	out := ansi.Strip(m.logBody(50, 12))
	assertHas(t, "narrow log error", out, "could not read the audit log", "permission denied", "2026-11.jsonl")
	if strings.Contains(out, "…") {
		t.Errorf("the error was cut:\n%s", out)
	}
}
