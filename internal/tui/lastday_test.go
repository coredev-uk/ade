package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/agile"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
)

func TestLastDayBlockShowsTheSummaryOrTheFacts(t *testing.T) {
	from := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	day := &agile.LastDay{Date: "2026-11-04", From: from, Narrative: "**Shipped**\n- api!12 rate limit merged"}
	m := model{g: unicodeGlyphs, w: 100, h: 40, ld: lastDayUI{day: day}}
	got := ansi.Strip(strings.Join(m.lastDayLines(100), "\n"))
	for _, want := range []string{"Last working day", "Wed 4 Nov", "AI summary", "Shipped", "api!12 rate limit merged"} {
		if !strings.Contains(got, want) {
			t.Errorf("block lacks %q:\n%s", want, got)
		}
	}

	facts := make([]agile.Entry, 7)
	for i := range facts {
		facts[i] = agile.Entry{At: from.Add(time.Duration(9+i) * time.Hour), Text: "pushed 1-rate-limit"}
	}
	m.ld = lastDayUI{day: &agile.LastDay{Date: "2026-11-04", From: from, Done: facts}, note: "AI summary paused: 92% of the day budget is spent"}
	got = ansi.Strip(strings.Join(m.lastDayLines(100), "\n"))
	for _, want := range []string{"09:00  pushed 1-rate-limit", "+ 2 more", "AI summary paused: 92% of the day budget"} {
		if !strings.Contains(got, want) {
			t.Errorf("block without AI lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "AI summary\n") || strings.Contains(got, "· AI summary") {
		t.Errorf("block claims an AI summary it does not have:\n%s", got)
	}

	if lines := (model{g: unicodeGlyphs}).lastDayLines(100); lines != nil {
		t.Errorf("nothing loaded and nothing loading: want no block, got %q", lines)
	}
}

// The facts are not an AI feature: Home asks for the block with AI off, with
// no profile and with a profile that is not selected. Only the summary on top
// of them needs a model.
func TestLastDayFactsAreAskedForWithoutAI(t *testing.T) {
	cfg := config.Defaults() // no AI profile
	m := model{app: &app.App{Config: &cfg}}
	if cmd := m.loadLastDay(); cmd == nil || !m.ld.busy || m.ld.asked == "" {
		t.Fatal("did not ask for the last working day with AI not set up")
	}
	cfg.AI.Profiles = map[string]config.AIProfile{"sub": {}}
	cfg.AI.Mode = "off"
	m = model{app: &app.App{Config: &cfg}}
	if cmd := m.loadLastDay(); cmd == nil {
		t.Fatal("did not ask for the last working day with ai.mode = off")
	}
}

func TestLastDaySummaryLeavesTheFactsAloneWithoutAI(t *testing.T) {
	from := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	l := agile.LastDay{Date: "2026-11-04", From: from, Done: []agile.Entry{{At: from.Add(9 * time.Hour), Text: "pushed 1-rate-limit"}}}
	for _, tc := range []struct {
		name string
		mode string
		prof map[string]config.AIProfile
		sel  string
		note string
	}{
		{"off", "off", map[string]config.AIProfile{"sub": {Driver: "omp"}}, "sub", ""},
		{"no profile configured", "ask", nil, "", ""},
		{"profiles but none selected", "ask", map[string]config.AIProfile{"sub": {Driver: "omp"}}, "", "no AI profile is selected"},
		{"selected profile missing", "ask", map[string]config.AIProfile{"sub": {Driver: "omp"}}, "gone", "is not defined"},
	} {
		cfg := config.Defaults()
		cfg.AI.Mode, cfg.AI.Profiles, cfg.AI.Profile = tc.mode, tc.prof, tc.sel
		msg := lastDaySummary(context.Background(), &app.App{Config: &cfg}, "2026-11-04", l)
		if msg.day == nil || len(msg.day.Done) != 1 || msg.day.Narrative != "" {
			t.Errorf("%s: the facts must survive without a summary: %+v", tc.name, msg.day)
		}
		if tc.note == "" && msg.note != "" || !strings.Contains(msg.note, tc.note) {
			t.Errorf("%s: note = %q, want %q", tc.name, msg.note, tc.note)
		}
	}
	m := model{g: unicodeGlyphs, w: 100, h: 40}
	m.onLastDay(lastDayMsg{today: "2026-11-04", day: &l})
	m.ld.asked = "2026-11-04"
	m.onLastDay(lastDayMsg{today: "2026-11-04", day: &l})
	if got := ansi.Strip(strings.Join(m.lastDayLines(100), "\n")); !strings.Contains(got, "09:00  pushed 1-rate-limit") {
		t.Errorf("the block lacks the day's facts:\n%s", got)
	}
}
