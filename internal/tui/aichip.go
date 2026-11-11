package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/budget"
)

// aiChipEvery is how often the header's AI figure is re-read from the ledger.
const aiChipEvery = 30

// aiChip is the header's `AI $0.38 / $2.00 day`: the budget period closest to
// its cap, spend against cap. It is read off the UI loop (the ledger is a
// query over the audit log) and shown once loaded.
type aiChip struct {
	ok     bool    // loaded; false hides the chip
	off    bool    // ai.mode = off
	period string  // day | week | sprint
	spent  float64 // spend in the period
	limit  float64 // its cap; 0 = no cap
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
		return aiChipMsg(pickChip(r.Caps))
	}
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
// 80% of the cap, red with a mark at the cap. The mark and the word carry the
// state, so NO_COLOR loses nothing.
func (c aiChip) render(g glyphs) string {
	switch {
	case !c.ok:
		return ""
	case c.off:
		return stFaint.Render("AI off")
	case c.limit <= 0:
		return stFaint.Render("AI " + usd(c.spent))
	}
	text := fmt.Sprintf("AI %s / %s %s", usd(c.spent), usd(c.limit), c.period)
	switch ratio := c.spent / c.limit; {
	case ratio >= 1:
		return stBad.Render(g.Bad + " " + text + " cap")
	case ratio >= 0.8:
		return stWarn.Render(g.Warn + " " + text)
	}
	return stFaint.Render(text)
}
