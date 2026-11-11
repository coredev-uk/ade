package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/billing"
	"github.com/coredev-uk/ade/internal/budget"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/ledger"
	"github.com/coredev-uk/ade/internal/paths"
)

// useUTC pins time.Local so dates in rendered rows do not depend on the
// machine running the tests.
func useUTC(t *testing.T) {
	t.Helper()
	old := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = old })
}

// widest is the widest line of s in terminal cells.
func widest(s string) int {
	w := 0
	for l := range strings.SplitSeq(ansi.Strip(s), "\n") {
		w = max(w, lipgloss.Width(l))
	}
	return w
}

func budFixture() budget.Report {
	now := time.Date(2026, 11, 11, 14, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	day := func(d int, cost float64, calls int) ledger.Bucket {
		return ledger.Bucket{Key: time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), Calls: calls, CostUSD: cost, TokensIn: calls * 1000, TokensOut: calls * 100}
	}
	return budget.Report{
		Now: now, Mode: "on", Profile: "claude-sub",
		Caps: []budget.Cap{
			{Period: "day", SpentUSD: 0.38, LimitUSD: 2, Remaining: 1.62, Known: true},
			{Period: "week", SpentUSD: 7.10, LimitUSD: 8, Remaining: 0.90, Known: true},
			{Period: "sprint", Known: false},
		},
		Profiles: []budget.ProfileView{
			{
				Name: "claude-sub", Active: true, Driver: "omp", Source: "omp", WeekUSD: 1.1,
				Models: map[string]string{"explain": "haiku-x", "draft": "sonnet-x", "plan": "opus-x", "implement": "sonnet-x", "review": "sonnet-x"},
				Provider: &billing.Merged{
					Snapshot:   billing.Snapshot{Source: "litellm", AsOf: now.Add(-3 * time.Minute), Scope: "key", Spend: new(0.0070), Limit: new(0.01), ResetAt: &reset, Window: "month"},
					LocalDelta: 0.001, Spend: new(0.0080), Remaining: new(0.0020),
				},
			},
			{
				Name: "kimi", Driver: "openai-compat", Source: "moonshot",
				Provider: &billing.Merged{Snapshot: billing.Snapshot{Source: "moonshot", Balance: new(4.8)}, Spend: new(0.12)},
				Stale:    true, ProviderErr: "timeout after 5s",
			},
			{Name: "claude-api", Driver: "anthropic", Source: "anthropic", ProviderErr: "Authorization: Bearer sk-ant-0123456789abcdef rejected (401)"},
		},
		ByDay: []ledger.Bucket{day(5, 0, 0), day(6, 0.05, 2), day(7, 0, 0), day(8, 1.20, 9), day(9, 0.62, 5), day(10, 0.40, 4), day(11, 0.38, 6)},
		ByFeature: []ledger.Bucket{
			{Key: "review", Calls: 9, CostUSD: 1.40, TokensIn: 9000, TokensOut: 900},
			{Key: "summarize", Calls: 12, CacheHits: 4, CostUSD: 0.0026, TokensIn: 1840, TokensOut: 160},
		},
		ByModel:  []ledger.Bucket{{Key: "sonnet-x", Calls: 21, CostUSD: 1.40}, {Key: "haiku-x", Calls: 12, CostUSD: 0.0026}},
		TopItems: []ledger.Bucket{{Key: "g/tracker#1", Calls: 8, CostUSD: 0.90}, {Key: "g/api!4", Calls: 3, CostUSD: 0.30}},
		Stops: []ledger.Entry{{
			ID: "01", TS: now.Add(-2 * time.Hour), Action: "budget.stop", Feature: "review", Profile: "claude-api", Model: "sonnet-x",
			Item: "g/tracker#1", Outcome: "error", Error: "Budget has been exceeded for this key",
		}},
		Rates: []ledger.Rates{
			{Profile: "claude-sub", Drafts: 12, Accepted: 7, Edited: 2, Denied: 3},
			{Profile: "kimi", Drafts: 8, Accepted: 0, Edited: 1, Denied: 6, Pending: 1},
		},
		CacheHits: 4, Calls: 21,
	}
}

func budModel(g glyphs, w, h int) model {
	rep := budFixture()
	m := model{g: g, nc: newNotifCenter(nil), w: w, h: h, page: pageBudget}
	m.bud = budgetState{rep: &rep, live: true}
	return m
}

func assertHas(t *testing.T, what, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("%s lacks %q:\n%s", what, want, out)
		}
	}
}

// The whole report in one list, at the width of the single pane: every
// section carries its figures, and every state a glyph and a word.
func TestBudgetListShowsEverySection(t *testing.T) {
	useUTC(t)
	m := budModel(unicodeGlyphs, 120, 40)
	out := ansi.Strip(m.budgetList(120, 90))
	assertHas(t, "budget list", out,
		"provider figures as of",
		"Caps", "day $0.38 of $2.00", "week $7.10 of $8.00",
		"✓ ok · $1.62 left", "▲ near cap · $0.90 left", "no sprint known",
		"Profiles", "ai on", "active claude-sub",
		"active", "gateway $0.0080 of $0.01 · resets 12 Nov",
		"moonshot $0.12 spent · balance $4.80 · ▲ stale · timed out",
		"✕ no figure · key rejected",
		"By day", "last 7 days", "Wed 11 Nov", "today", "no calls", "$1.20", "━",
		"By feature", "last 30 days · 21 calls · 4 cache hits", "review", "summarize", "$0.0026", "12 calls · 4 cached",
		"By model", "sonnet-x", "haiku-x",
		"Top items", "tracker#1", "api!4", "$0.90",
		"Recent stops", "✕ stopped", "claude-api", "Budget has been exceeded for this key",
		"Review triage", "claude-sub 12 drafts · 58% accepted · 17% edited · 25% denied",
		"kimi       8 drafts · 0% accepted · 14% edited · 86% denied · 1 pending · ▲ mostly denied",
	)
	if strings.Contains(out, "sk-ant-0123456789abcdef") {
		t.Errorf("a credential leaked into the page:\n%s", out)
	}
}

func TestBudgetPageRendersAtEachSize(t *testing.T) {
	useUTC(t)
	cases := []struct {
		name string
		g    glyphs
		w, h int
		env  bool
		want []string
	}{
		{"80x24", unicodeGlyphs, 80, 24, false, []string{"Budget", "▲ near cap", "gateway $0.0080", "Wed 11 Nov"}},
		{"120x40", unicodeGlyphs, 120, 40, false, []string{"Budget", "▲ near cap", "gateway $0.0080 of $0.01 · resets 12 Nov", "Top items", "Day cap", "spent     $0.38"}},
		{"80x24 ascii", asciiGlyphs, 80, 24, false, []string{"Budget", "! near cap", "+ ok", "gateway $0.0080 of $0.01 - resets 12 Nov", "====="}},
		{"120x40 NO_COLOR ascii", asciiGlyphs, 120, 40, true, []string{"Budget", "! near cap", "x stopped", "o kimi"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.env {
				t.Setenv("NO_COLOR", "1")
			}
			m := budModel(c.g, c.w, c.h)
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

// The detail pane follows the selection; below 100 columns tab swaps it in.
func TestBudgetDetailFollowsSelectionAndTabSwaps(t *testing.T) {
	useUTC(t)
	m := budModel(unicodeGlyphs, 120, 40)
	m.bud.sel = 3 // first profile
	out := ansi.Strip(m.render())
	assertHas(t, "profile detail", out,
		"claude-sub", "driver omp", "explain   haiku-x", "review    sonnet-x",
		"this week $1.10", "source    gateway", "scope     key",
		"spend     $0.0080", "provider  $0.0070", "ledger    +$0.0010 since", "cap       $0.01", "left      $0.0020",
		"window    month · resets 12 Nov 12:00")

	n := budModel(unicodeGlyphs, 80, 24)
	n.bud.sel = 3
	if out := ansi.Strip(n.render()); strings.Contains(out, "explain   haiku-x") {
		t.Fatalf("a narrow terminal shows the list, not the details:\n%s", out)
	}
	n, _ = press(n, "tab")
	out = ansi.Strip(n.render())
	assertHas(t, "narrow detail", out, "explain   haiku-x", "claude-sub")
	n, _ = press(n, "esc")
	if n.page != pageBudget || n.showDetail {
		t.Errorf("esc closes the details first: page %d showDetail %v", n.page, n.showDetail)
	}
}

func TestBudgetCapStatesCarryGlyphAndWord(t *testing.T) {
	cases := []struct {
		cap  budget.Cap
		uni  string
		asc  string
		bare string
	}{
		{budget.Cap{Period: "day", SpentUSD: 0.5, LimitUSD: 2, Remaining: 1.5, Known: true}, "✓ ok", "+ ok", "$1.50 left"},
		{budget.Cap{Period: "day", SpentUSD: 1.7, LimitUSD: 2, Remaining: 0.3, Known: true}, "▲ near cap", "! near cap", "$0.30 left"},
		{budget.Cap{Period: "day", SpentUSD: 2, LimitUSD: 2, Known: true}, "✕ cap reached", "x cap reached", "calls stopped"},
		{budget.Cap{Period: "week", SpentUSD: 8.4, LimitUSD: 8, Remaining: -0.4, Known: true}, "✕ over cap by $0.40", "x over cap by $0.40", "calls stopped"},
		{budget.Cap{Period: "sprint", SpentUSD: 3, Known: true}, "no cap set", "no cap set", "$3.00 spent"},
		{budget.Cap{Period: "sprint", LimitUSD: 20}, "no sprint known", "no sprint known", "cap $20.00 not enforced"},
	}
	for _, c := range cases {
		for _, g := range []glyphs{unicodeGlyphs, asciiGlyphs} {
			m := model{g: g}
			row := ansi.Strip(m.capRow(c.cap, func(s string, _ bool) string { return s }, false, 70))
			want := c.uni
			if g == asciiGlyphs {
				want = c.asc
			}
			assertHas(t, c.cap.Period, row, want, c.bare)
			if got := widest(row); got > 70 {
				t.Errorf("cap row is %d cells, over 70: %q", got, row)
			}
		}
	}
	// The bar shows spend as a station line: baseline, travelled, train, cap.
	m := model{g: unicodeGlyphs}
	row := ansi.Strip(m.capRow(budget.Cap{Period: "day", SpentUSD: 1, LimitUSD: 2, Known: true}, func(s string, _ bool) string { return s }, false, 70))
	if !strings.Contains(row, "●━━━━━━◉─────○") || !strings.Contains(row, "50%") {
		t.Errorf("half-spent cap bar: %q", row)
	}
	over := ansi.Strip(m.capRow(budget.Cap{Period: "day", SpentUSD: 3, LimitUSD: 2, Known: true}, func(s string, _ bool) string { return s }, false, 70))
	if !strings.Contains(over, "●━━━━━━━━━━━━◉ 150%") {
		t.Errorf("over-cap bar fills to the train: %q", over)
	}
}

func TestBudgetEmptyStatesAreWords(t *testing.T) {
	useUTC(t)
	m := budModel(unicodeGlyphs, 100, 30)
	rep := budFixture()
	rep.Calls, rep.CacheHits = 0, 0
	rep.ByDay, rep.ByFeature, rep.ByModel, rep.TopItems, rep.Stops, rep.Rates = nil, nil, nil, nil, nil, nil
	rep.Profiles = nil
	m.bud.rep = &rep
	out := ansi.Strip(m.budgetList(100, 40))
	assertHas(t, "empty budget", out, "no AI calls yet", "no budget stops", "no agent review drafts yet", "no AI profiles configured")
	if strings.Contains(out, "By day") {
		t.Errorf("spend sections collapse to one empty line:\n%s", out)
	}
	if n := len(budRows(&rep)); n != 3 {
		t.Errorf("only the caps are selectable, got %d rows", n)
	}

	m.bud.rep, m.bud.err = nil, errors.New("ledger is locked")
	assertHas(t, "failed first read", ansi.Strip(m.budgetBody(100, 10)), "could not read the budget", "ledger is locked", "r tries again")
	m.bud.err = nil
	assertHas(t, "first read", ansi.Strip(m.budgetBody(100, 10)), "reading the ledger")
}

func TestBudgetStatusNamesFreshness(t *testing.T) {
	m := budModel(unicodeGlyphs, 120, 40)
	m.bud.live = false
	assertHas(t, "offline", ansi.Strip(m.budgetStatus()), "▲ stale", "cached provider figures", "r reads providers now")
	m.bud.refreshing = true
	assertHas(t, "refreshing", ansi.Strip(m.budgetStatus()), "⟳ refreshing providers", "figures as of")
	m.bud.refreshing, m.bud.err = false, errors.New("gateway unreachable")
	assertHas(t, "failed", ansi.Strip(m.budgetStatus()), "✕ refresh failed: gateway unreachable")
	m.bud.err, m.bud.live = nil, true
	rep := budFixture()
	rep.Mode = "off"
	m.bud.rep = &rep
	assertHas(t, "ai off", ansi.Strip(m.budgetStatus()), "▲ AI is off", "ai.mode = off")
}

func TestBudgetKeys(t *testing.T) {
	m := model{g: unicodeGlyphs, nc: newNotifCenter(nil), w: 120, h: 40, page: pageHome}
	m.bud.deps = func() (budget.Deps, error) { return budget.Deps{}, errors.New("no deps in this test") }

	m, cmd := press(m, "b")
	if m.page != pageBudget || cmd == nil || !m.bud.loading {
		t.Fatalf("b: page %d, cmd %v, loading %v", m.page, cmd != nil, m.bud.loading)
	}
	// A failed first read says so and leaves the page usable.
	next, _ := m.Update(cmd())
	m = next.(model)
	if m.bud.err == nil || m.bud.loading {
		t.Fatalf("a failed build: err %v loading %v", m.bud.err, m.bud.loading)
	}
	m, _ = press(m, "b")
	if m.page != pageHome {
		t.Errorf("b again returns to Home, page %d", m.page)
	}

	m.page = pageTrees
	m, _ = press(m, "b")
	m, _ = press(m, "esc")
	if m.page != pageTrees {
		t.Errorf("esc returns to the page b was pressed on, page %d", m.page)
	}

	// t keeps flipping Home and Worktrees from the page underneath.
	m.page = pageHome
	m, _ = press(m, "b")
	m, _ = press(m, "t")
	if m.page != pageTrees {
		t.Errorf("t from Budget over Home gives Worktrees, page %d", m.page)
	}
	m, _ = press(m, "t")
	if m.page != pageHome {
		t.Errorf("t on Worktrees still flips to Home, page %d", m.page)
	}

	// L goes straight to the Log and back to the page before Budget.
	m, _ = press(m, "b")
	m, cmd = press(m, "L")
	if m.page != pageLog || cmd == nil {
		t.Fatalf("L from Budget: page %d cmd %v", m.page, cmd != nil)
	}
	m, _ = press(m, "L")
	if m.page != pageHome {
		t.Errorf("L again returns to the page Budget was opened over, page %d", m.page)
	}

	// Selection moves through the rows and stops at the ends.
	rep := budFixture()
	m.page, m.bud.rep = pageBudget, &rep
	n := len(budRows(&rep))
	m, _ = press(m, "G")
	if m.bud.sel != n-1 {
		t.Errorf("G selects the last row, got %d of %d", m.bud.sel, n)
	}
	m, _ = press(m, "j")
	if m.bud.sel != n-1 {
		t.Errorf("j stops at the last row, got %d", m.bud.sel)
	}
	m, _ = press(m, "g")
	m, _ = press(m, "k")
	if m.bud.sel != 0 {
		t.Errorf("g then k stays on the first row, got %d", m.bud.sel)
	}

	// r reads the providers now, once at a time.
	gen := m.bud.gen
	m, cmd = press(m, "r")
	if cmd == nil || !m.bud.refreshing || m.bud.gen != gen+1 {
		t.Fatalf("r: cmd %v refreshing %v gen %d→%d", cmd != nil, m.bud.refreshing, gen, m.bud.gen)
	}
	if _, cmd = press(m, "r"); cmd != nil {
		t.Errorf("r while a refresh runs starts nothing")
	}
}

// An answer for an older request must not overwrite a newer one.
func TestBudgetDropsStaleAnswers(t *testing.T) {
	m := budModel(unicodeGlyphs, 120, 40)
	m.bud.gen = 5
	old := budFixture()
	old.Profile = "from-an-old-request"
	next, cmd := m.Update(budgetMsg{gen: 4, phase: budgetOnline, rep: old})
	m = next.(model)
	if cmd != nil || m.bud.rep.Profile != "claude-sub" {
		t.Errorf("stale answer applied: profile %q cmd %v", m.bud.rep.Profile, cmd != nil)
	}
}

// enter on a top item jumps to it on Home; one Home does not know is a no-op.
func TestBudgetEnterJumpsToTopItem(t *testing.T) {
	m := budModel(unicodeGlyphs, 120, 40)
	m.setResults([]forgesync.Result{sampleResult()})
	rows := budRows(m.bud.rep)
	item := func(i int) int {
		for n, r := range rows {
			if r.kind == bkItem && r.idx == i {
				return n
			}
		}
		t.Fatalf("no top item %d", i)
		return 0
	}

	m.bud.sel = item(1) // g/api!4: not on Home
	got, cmd := press(m, "enter")
	if got.page != pageBudget || cmd != nil {
		t.Errorf("an unknown item is a no-op: page %d cmd %v", got.page, cmd != nil)
	}

	m.bud.sel = item(0) // g/tracker#1
	got, _ = press(m, "enter")
	if got.page != pageHome || got.homeRows[got.homeSel].ref() != "g/tracker#1" {
		t.Errorf("enter on g/tracker#1: page %d, selected %q", got.page, got.homeRows[got.homeSel].ref())
	}
	if _, ok := m.homeTargetFor("g/api!4"); ok {
		t.Errorf("g/api!4 should not be on Home")
	}
}

// The page is built off the UI loop: an offline report first, then the
// provider read, with the real ledger over a temp audit log.
func TestBudgetPageBuildsOfflineThenRefreshes(t *testing.T) {
	cfg := config.Defaults()
	cfg.AI.Profile = "claude-sub"
	cfg.AI.Profiles = map[string]config.AIProfile{"claude-sub": {
		Driver: "omp", Models: map[string]string{"explain": "haiku-x", "draft": "sonnet-x"},
		Billing: config.AIBilling{Source: "omp", WeeklySoftUSD: 5},
	}}
	a := &app.App{Paths: paths.Paths{CacheDir: t.TempDir()}, Config: &cfg}
	t.Cleanup(func() { a.Close() })
	log, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	a.Audit = log
	now := time.Now()
	for i, cost := range []float64{0.0026, 0.0040} {
		err := log.Append(audit.Entry{
			TS: now.Add(-time.Duration(i+1) * time.Minute), Action: "ai.summarize", Target: "g/tracker#1", Item: "g/tracker#1",
			AI: &audit.AIInfo{Profile: "claude-sub", Driver: "omp", Model: "haiku-x", TokensIn: 1000, TokensOut: 100, CostUSD: cost, CostSource: "reported"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	m := model{ctx: context.Background(), app: a, g: unicodeGlyphs, nc: newNotifCenter(nil), w: 120, h: 40}

	m, cmd := press(m, "b")
	next, cmd := m.Update(cmd()) // the offline read
	m = next.(model)
	if m.bud.rep == nil || m.bud.live || cmd == nil || !m.bud.refreshing {
		t.Fatalf("after the offline read: rep %v live %v refresh started %v", m.bud.rep != nil, m.bud.live, cmd != nil)
	}
	if m.bud.rep.Calls != 2 {
		t.Errorf("offline report has %d calls, want 2", m.bud.rep.Calls)
	}
	out := ansi.Strip(m.render())
	assertHas(t, "offline page", out, "⟳ refreshing providers", "figures as of", "summarize", "$0.0066")

	next, cmd = m.Update(cmd()) // the provider read
	m = next.(model)
	if !m.bud.live || m.bud.refreshing || cmd != nil {
		t.Fatalf("after the provider read: live %v refreshing %v", m.bud.live, m.bud.refreshing)
	}
	out = ansi.Strip(m.render())
	assertHas(t, "refreshed page", out, "provider figures as of", "claude-sub", "subscription")
	if strings.Contains(out, "stale") {
		t.Errorf("a fresh read is not stale:\n%s", out)
	}
}

// Every row's details render, whatever the provider reported: snapshots carry
// nil for what is unknown.
func TestBudgetDetailRendersForEveryRow(t *testing.T) {
	useUTC(t)
	m := budModel(unicodeGlyphs, 120, 40)
	rep := budFixture()
	rep.Profiles = append(rep.Profiles,
		budget.ProfileView{Name: "bare", Driver: "omp"},
		budget.ProfileView{Name: "soft", Driver: "omp", Source: "omp", Provider: &billing.Merged{
			Snapshot: billing.Snapshot{Source: "omp", SoftLimit: new(5.0), Note: "list-price equivalent, not billed"}, Spend: new(1.5),
		}},
		budget.ProfileView{Name: "odd", Driver: "x", Source: "x", Provider: &billing.Merged{Spend: new(0.5)}},
	)
	m.bud.rep = &rep
	for _, w := range []int{80, 120} {
		m.w = w
		for i, r := range budRows(&rep) {
			m.bud.sel = i
			out := ansi.Strip(m.render())
			if got := widest(out); got > w {
				t.Errorf("row %+v at %d: a line is %d cells wide", r, w, got)
			}
			if w == 80 {
				m.showDetail = true
				if strings.TrimSpace(ansi.Strip(strings.Join(m.budgetDetail(78), "\n"))) == "" {
					t.Errorf("row %+v has no details", r)
				}
				m.showDetail = false
			}
		}
	}
	m.w, m.bud.sel = 120, 3+3 // "bare"... the soft profile is the second added
	m.bud.sel = len(rep.Caps) + 4
	assertHas(t, "soft limit", ansi.Strip(m.render()), "subscription $1.50 of $5.00 soft limit", "soft      $5.00 (never blocks)", "x $0.50 spent", "list-price equivalent")
}

// A failing provider shows its short cause on the row and the full,
// credential-free reason in the detail pane; the page's status line says a
// figure is missing or stale in words.
func TestBudgetProviderErrorDetailAndStatus(t *testing.T) {
	useUTC(t)
	m := budModel(unicodeGlyphs, 120, 40)
	for i, r := range budRows(m.bud.rep) {
		if r.kind == bkProfile && m.bud.rep.Profiles[r.idx].Name == "claude-api" {
			m.bud.sel = i
		}
	}
	detail := ansi.Strip(strings.Join(m.budgetDetail(60), "\n"))
	assertHas(t, "failed profile detail", detail, "provider read failed: key rejected", "Authorization: Bearer ***", "no figure yet")
	if strings.Contains(detail, "sk-ant-") {
		t.Errorf("a credential reached the screen:\n%s", detail)
	}
	assertHas(t, "status", ansi.Strip(m.budgetStatus()), "✕ no provider figure for claude-api", "▲ stale: cached provider figures as of", "r reads providers now")

	rep := budFixture()
	rep.Profiles = rep.Profiles[:1] // only the profile with a fresh figure
	m.bud.rep = &rep
	assertHas(t, "settled status", ansi.Strip(m.budgetStatus()), "provider figures as of")
	if strings.Contains(ansi.Strip(m.budgetStatus()), "stale") {
		t.Errorf("a fresh figure is not stale: %s", ansi.Strip(m.budgetStatus()))
	}
}

// A detail taller than the pane scrolls: the clipped pane says how much is
// below, J/K move it, and the last line is reachable.
func TestBudgetDetailScrollsToItsEnd(t *testing.T) {
	m := budModel(unicodeGlyphs, 60, 14) // narrow, short: the profile detail does not fit
	for i, r := range budRows(m.bud.rep) {
		if r.kind == bkProfile && m.bud.rep.Profiles[r.idx].Name == "kimi" {
			m.bud.sel = i
		}
	}
	m.showDetail = true
	full := m.budgetDetail(m.toolDetailWidth())
	h := m.toolBodyH()
	if len(full) <= h {
		t.Fatalf("fixture too short to clip: %d lines for %d rows", len(full), h)
	}
	assertHas(t, "clipped", ansi.Strip(m.budgetBody(60, h)), "more lines", "J scrolls")
	for range len(full) {
		m, _ = press(m, "J")
	}
	end := ansi.Strip(m.budgetBody(60, h))
	if strings.Contains(end, "more lines") {
		t.Errorf("at the end nothing is left below:\n%s", end)
	}
	assertHas(t, "scrolled end", end, strings.TrimSpace(ansi.Strip(full[len(full)-1])))
	m, _ = press(m, "K")
	if m.bud.dscroll >= m.maxDetailScroll(len(full)) {
		t.Errorf("K scrolls back up: %d", m.bud.dscroll)
	}
	m, _ = press(m, "k")
	if m.bud.dscroll != 0 {
		t.Errorf("moving the selection resets the scroll: %d", m.bud.dscroll)
	}
}
