package tui

import (
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

func TestLastDayIsNotAskedForWithoutAI(t *testing.T) {
	cfg := config.Defaults() // no AI profile
	m := model{app: &app.App{Config: &cfg}}
	if cmd := m.loadLastDay(); cmd != nil || m.ld.asked != "" {
		t.Fatal("asked for a last-day summary with AI not set up")
	}
	cfg.AI.Profiles = map[string]config.AIProfile{"sub": {}}
	cfg.AI.Mode = "off"
	m = model{app: &app.App{Config: &cfg}}
	if cmd := m.loadLastDay(); cmd != nil {
		t.Fatal("asked for a last-day summary with ai.mode = off")
	}
}
