package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Home's budget banner: one line under the header while AI is paused for the
// budget, because a local cap is reached or a provider refused a call for
// budget today. It is no modal and no pop-up: the alert came once
// (ai/daystop.go), and this is the standing state. It is drawn from the
// header chip's value, so the two always agree, and it goes away with the
// chip's re-read when the day ends or spend is back under the cap.

// banner words the pause, "" when AI is not paused.
func (c aiChip) banner(g glyphs, w int) string {
	if !c.ok || c.off {
		return ""
	}
	capped := c.capReached()
	if !capped && len(c.stopped) == 0 {
		return ""
	}
	var facts []string
	if len(c.stopped) > 0 {
		facts = append(facts, strings.Join(c.stopped, ", "))
	}
	// A provider refusal pauses the calls nobody asked for until tomorrow;
	// a reached cap refuses every new call until it starts over.
	paused := "automatic AI paused until tomorrow"
	if capped {
		facts = append(facts, capWord(c.period)+" cap "+usd(c.limit))
		until := c.until
		if until == "" {
			until = "the cap starts over"
		}
		paused = "AI paused until " + until
	}
	sep := " " + g.Sep + " "
	line := stBad.Render(g.Bad+" AI budget reached") + sep + strings.Join(append(facts, paused), sep) +
		stFaint.Render(sep+"b shows the budget")
	return " " + ansi.Truncate(line, max(1, w-2), g.Ell)
}

// capWord names a budget period's cap the way the config does.
func capWord(period string) string {
	switch period {
	case "day":
		return "daily"
	case "week":
		return "weekly"
	}
	return period
}

// homeBanner is the banner when Home is the page showing, "" elsewhere.
func (m model) homeBanner(w int) string {
	if m.page != pageHome || m.mr != nil || m.isAgilePage() {
		return ""
	}
	return m.chip.banner(m.g, w)
}
