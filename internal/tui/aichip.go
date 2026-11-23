package tui

import (
	"context"
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/budget"
	"github.com/coredev-uk/ade/internal/ledger"
)

// aiChip is the header's `AI $0.38 / $2.00 day`: the budget period closest to
// its cap, spend against cap, and whether a provider refused a call today. It
// is read off the UI loop (the ledger is a query over the audit log) and shown
// once loaded; it is re-read when the audit log changes, i.e. right after an
// AI call, and when the local day ends (watch.go). Home's budget banner
// (aibanner.go) is drawn from the same value, so the two always agree.
type aiChip struct {
	ok     bool    // loaded; false hides the chip
	off    bool    // ai.mode = off
	period string  // day | week | sprint
	spent  float64 // spend in the period
	limit  float64 // its cap; 0 = no cap
	// stopped lists the configured profiles a provider refused for budget
	// today (a budget.stop not from ADE's own caps), sorted.
	stopped []string
	// until is when the last reached cap starts over: "tomorrow", a date, or
	// "the next sprint"; empty while no cap is reached.
	until string
}

type aiChipMsg aiChip

// loadAIChip reads the figure. It yields nothing when AI is not set up at all
// (no profile configured), so a machine without AI shows no chip.
func (m model) loadAIChip() tea.Cmd {
	a := m.app
	if a == nil {
		return nil
	}
	return func() tea.Msg {
		cfg := a.Config
		if cfg.AI.Mode == "off" {
			return aiChipMsg{ok: true, off: true}
		}
		if len(cfg.AI.Profiles) == 0 {
			return aiChipMsg{}
		}
		d, err := budget.ForApp(a)
		if err != nil {
			return aiChipMsg{}
		}
		r, err := budget.Build(context.Background(), d, budget.Options{Offline: true, Days: 1, Window: time.Hour})
		if err != nil {
			return aiChipMsg{}
		}
		c := pickChip(r.Caps)
		c.until = pausedUntil(r.Caps, r.Now, d.Loc)
		if stops, err := ai.StopsToday(a.Audit, r.Now, d.Loc); err == nil {
			for name, s := range stops {
				if _, ok := cfg.AI.Profiles[name]; ok && s.Provider != nil {
					c.stopped = append(c.stopped, name)
				}
			}
			slices.Sort(c.stopped)
		}
		return aiChipMsg(c)
	}
}

// pausedUntil words when the longest reached cap starts over, "" when none is
// reached: the caps refuse new calls until then.
func pausedUntil(caps []budget.Cap, now time.Time, loc *time.Location) string {
	until := ""
	for _, c := range caps { // day, week, sprint: a later reached one pauses longer
		if !c.Known || c.LimitUSD <= 0 || c.SpentUSD < c.LimitUSD {
			continue
		}
		switch c.Period {
		case "day":
			until = "tomorrow"
		case "week":
			until = ledger.WeekStart(now, loc).AddDate(0, 0, 7).Format("Mon 2 Jan")
		case "sprint":
			until = "the next sprint"
		}
	}
	return until
}

// pickChip chooses the period nearest its cap; with no cap anywhere, today's spend.
func pickChip(caps []budget.Cap) aiChip {
	best := aiChip{ok: true, period: "day"}
	for _, c := range caps {
		if c.Period == "day" {
			best.spent = c.SpentUSD
		}
	}
	ratio := -1.0
	for _, c := range caps {
		if !c.Known || c.LimitUSD <= 0 {
			continue
		}
		if r := c.SpentUSD / c.LimitUSD; r > ratio {
			ratio = r
			best = aiChip{ok: true, period: c.Period, spent: c.SpentUSD, limit: c.LimitUSD}
		}
	}
	return best
}

// render words the chip: faint while there is room, amber with a mark from
// 80% of the cap, red with a mark at the cap or when a provider refused a call
// for budget today. The mark and the word carry the state, so NO_COLOR loses
// nothing.
func (c aiChip) render(g glyphs) string {
	switch {
	case !c.ok:
		return ""
	case c.off:
		return stFaint.Render("AI off")
	}
	text := "AI " + usd(c.spent)
	if c.limit > 0 {
		text = fmt.Sprintf("AI %s / %s %s", usd(c.spent), usd(c.limit), c.period)
	}
	switch {
	case c.capReached():
		return stBad.Render(g.Bad + " " + text + " cap")
	case len(c.stopped) > 0:
		return stBad.Render(g.Bad + " " + text + " stopped")
	case c.limit > 0 && c.spent/c.limit >= 0.8:
		return stWarn.Render(g.Warn + " " + text)
	}
	return stFaint.Render(text)
}

// capReached reports whether the shown period's spend is at its cap.
func (c aiChip) capReached() bool { return c.limit > 0 && c.spent/c.limit >= 1 }
