package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/agile"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/budget"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/ledger"
)

// bannerHome is Home with AI set up on a gateway profile and an audit log, so
// the chip (and with it the banner) is read the way the dashboard reads it.
func bannerHome(t *testing.T) (*wsHarness, *audit.Log) {
	t.Helper()
	h := homeHarness(t, 120)
	h.m.homeLoaded, h.m.syncing = true, false
	log, err := audit.Open(filepath.Join(t.TempDir(), "data"), "user")
	if err != nil {
		t.Fatal(err)
	}
	a := h.m.app
	a.Audit = log
	a.Config.AI.Mode, a.Config.AI.Profile = "auto", "gw"
	a.Config.AI.Profiles = map[string]config.AIProfile{"gw": {Driver: "openai-compat"}}
	return h, log
}

func (h *wsHarness) reloadChip() {
	h.t.Helper()
	h.send(h.m.loadAIChip()())
}

// screenTop is the first three lines of the screen, as text.
func (h *wsHarness) screenTop() []string {
	lines := strings.Split(ansi.Strip(h.m.render()), "\n")
	return lines[:3]
}

func stopEntry(profile, source string) audit.Entry {
	return audit.Entry{
		Action: ledger.StopAction, Item: "g/a!3", Outcome: "error", Error: "Budget has been exceeded",
		Params: map[string]any{"feature": "summarize", "profile": profile, "source": source},
	}
}

func countKind(evs []events.Event, kind string) int {
	n := 0
	for _, ev := range evs {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

func TestBudgetBannerShowsOnlyWhileAIIsPaused(t *testing.T) {
	h, log := bannerHome(t)
	h.reloadChip()
	top := h.screenTop()
	if strings.Contains(strings.Join(top, "\n"), "AI budget reached") || strings.TrimSpace(top[1]) != "" {
		t.Fatalf("a banner with nothing stopped:\n%s", strings.Join(top, "\n"))
	}

	// ADE's own refusal of one expensive call is no pause: smaller calls
	// still fit, so no banner.
	log.Append(stopEntry("gw", "local"))
	h.reloadChip()
	if top := h.screenTop(); strings.Contains(strings.Join(top, "\n"), "AI budget reached") {
		t.Fatalf("a local refusal raised the banner:\n%s", strings.Join(top, "\n"))
	}

	// The gateway refused a call today: one line under the header, and the
	// chip agrees.
	log.Append(stopEntry("gw", "gateway"))
	h.reloadChip()
	top = h.screenTop()
	if want := "✕ AI budget reached · gw · automatic AI paused until tomorrow · b shows the budget"; strings.TrimSpace(top[1]) != want {
		t.Fatalf("banner line = %q, want %q", strings.TrimSpace(top[1]), want)
	}
	if !strings.Contains(top[0], "✕ AI $0.00 / $2.00 day stopped") || strings.TrimSpace(top[2]) != "" {
		t.Fatalf("header and spacing:\n%s", strings.Join(top, "\n"))
	}

	// Only Home carries it.
	h.m.page = pageTrees
	if top := h.screenTop(); strings.Contains(strings.Join(top, "\n"), "AI budget reached") {
		t.Fatalf("the banner left Home:\n%s", strings.Join(top, "\n"))
	}
}

func TestBudgetBannerNamesTheReachedCapAndWhenItStartsOver(t *testing.T) {
	h, log := bannerHome(t)
	h.m.app.Config.Budget.DailyUSD = 0.01
	log.Append(audit.Entry{Action: "ai.summarize", Item: "x", AI: &audit.AIInfo{Profile: "gw", Model: "m", CostUSD: 0.02, CostSource: "reported"}})
	h.reloadChip()
	top := h.screenTop()
	if want := "✕ AI budget reached · daily cap $0.01 · AI paused until tomorrow · b shows the budget"; strings.TrimSpace(top[1]) != want {
		t.Fatalf("banner line = %q, want %q", strings.TrimSpace(top[1]), want)
	}
	if !strings.Contains(top[0], "✕ AI $0.02 / $0.01 day cap") {
		t.Fatalf("the chip disagrees: %q", top[0])
	}

	// Raising the cap (or the day rolling over) clears it on the next read.
	h.m.app.Config.Budget.DailyUSD = 1
	h.reloadChip()
	if top := h.screenTop(); strings.Contains(strings.Join(top, "\n"), "AI budget reached") {
		t.Fatalf("the banner outlived the cap:\n%s", strings.Join(top, "\n"))
	}
}

func TestBudgetBannerWordsAndPausedUntil(t *testing.T) {
	g := unicodeGlyphs
	for _, tc := range []struct {
		chip aiChip
		want string
	}{
		{aiChip{}, ""},
		{aiChip{ok: true, off: true, stopped: []string{"gw"}}, ""},
		{aiChip{ok: true, period: "day", spent: 1.7, limit: 2}, ""},
		{aiChip{ok: true, period: "day", spent: 0.4, stopped: []string{"claude", "gw"}}, "✕ AI budget reached · claude, gw · automatic AI paused until tomorrow · b shows the budget"},
		{aiChip{ok: true, period: "week", spent: 8, limit: 8, until: "Mon 23 Nov", stopped: []string{"gw"}}, "✕ AI budget reached · gw · weekly cap $8.00 · AI paused until Mon 23 Nov · b shows the budget"},
	} {
		if got := strings.TrimSpace(ansi.Strip(tc.chip.banner(g, 200))); got != tc.want {
			t.Errorf("banner(%+v) = %q, want %q", tc.chip, got, tc.want)
		}
	}
	if got := ansi.Strip(aiChip{ok: true, period: "day", stopped: []string{"gw"}}.banner(asciiGlyphs, 200)); !strings.HasPrefix(got, " x AI budget reached - gw - ") {
		t.Errorf("ascii banner = %q", got)
	}

	loc := time.UTC
	now := time.Date(2026, 11, 19, 15, 0, 0, 0, loc) // a Thursday
	if got := pausedUntil(nil, now, loc); got != "" {
		t.Errorf("no caps: %q", got)
	}
	day := budget.Cap{Period: "day", SpentUSD: 2, LimitUSD: 2, Known: true}
	week := budget.Cap{Period: "week", SpentUSD: 9, LimitUSD: 10, Known: true}
	if got := pausedUntil([]budget.Cap{day, week}, now, loc); got != "tomorrow" {
		t.Errorf("daily cap reached: %q", got)
	}
	week.SpentUSD = 10
	if got := pausedUntil([]budget.Cap{day, week}, now, loc); got != ledger.WeekStart(now, loc).AddDate(0, 0, 7).Format("Mon 2 Jan") {
		t.Errorf("weekly cap reached: %q", got)
	}
}

// On a day the profile was stopped, Home's last-day block makes no AI call:
// it lists the day's facts and says why.
func TestLastDaySummaryMakesNoCallOnAStoppedDay(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAI("auto")
	a := e.m.app
	from := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	day := agile.LastDay{Date: "2026-11-04", From: from, Done: []agile.Entry{{At: from.Add(9 * time.Hour), Text: "pushed 1-rate-limit"}}}

	// Without a stop the summary is asked for (the stub answers nothing
	// useful, but the call is made).
	lastDaySummary(context.Background(), a, "2026-11-05", day)
	if n := len(stub.asked()); n != 1 {
		t.Fatalf("calls before a stop = %d, want 1", n)
	}

	if err := a.Audit.Append(stopEntry("sub", "omp")); err != nil {
		t.Fatal(err)
	}
	msg := lastDaySummary(context.Background(), a, "2026-11-05", day)
	if n := len(stub.asked()); n != 1 {
		t.Fatalf("a call went out on a stopped day: %v", stub.asked())
	}
	if msg.day == nil || msg.day.Narrative != "" || !strings.Contains(msg.note, "budget was reached today") {
		t.Fatalf("msg = %+v", msg)
	}

	// The sprint review's automatic narrative is skipped the same way.
	m := e.m
	m.ag.kind, m.ag.review = agileReview, &agile.Review{}
	_, cmd := m.narrateAuto()
	if cmd == nil {
		t.Fatal("no narrative command")
	}
	if nm, ok := cmd().(narrativeMsg); !ok || nm.err != errAutoSkipped {
		t.Fatalf("auto narrative on a stopped day: %+v", nm)
	}
	if n := len(stub.asked()); n != 1 {
		t.Fatalf("the sprint narrative called the model: %v", stub.asked())
	}
}

// On a stopped day the MR page's summary, which starts by itself, is not
// asked for; s still asks.
func TestMRSummaryWaitsOnAStoppedDay(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAI("auto")
	if err := e.m.app.Audit.Append(stopEntry("sub", "omp")); err != nil {
		t.Fatal(err)
	}
	e.open(mrRef("api", 2).String())
	e.want("no summary", "AI budget reached today; s asks anyway", "Stacked on !1")
	if len(stub.asked()) != 0 {
		t.Fatalf("the automatic summary called the model: %v", stub.asked())
	}
	if n := len(e.audited(ledger.StopAction)); n != 1 {
		t.Fatalf("budget.stop entries = %d: a paused automatic call is not a refused call", n)
	}
	e.press("s")
	e.want("Renames the bucket file")
	if got := stub.asked(); len(got) != 1 || got[0] != "sum" {
		t.Fatalf("s did not ask: %v", got)
	}
}
