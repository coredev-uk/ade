package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/audit"
)

// --- shared by the Log page and the History tab -----------------------------------

// outcomeMark is an entry's outcome as a glyph and a word; every outcome has
// both, so NO_COLOR and the ascii glyph set lose nothing.
func outcomeMark(g glyphs, e audit.Entry) (mark, word string) {
	switch e.Outcome {
	case "ok", "":
		return stOK.Render(g.OK), "ok"
	case "error":
		return stBad.Render(g.Bad), "error"
	case "cancelled":
		return stWarn.Render(g.Warn), "cancelled"
	case "skipped":
		return stFaint.Render(g.Ahead), "skipped"
	}
	return stFaint.Render(g.Info), e.Outcome
}

// outcomeTail is what follows the outcome word: a dry run, and for AI
// entries a cache hit or the cost.
func outcomeTail(g glyphs, e audit.Entry) string {
	var parts []string
	if e.DryRun {
		parts = append(parts, "dry run")
	}
	if a := e.AI; a != nil {
		switch {
		case a.CacheHit:
			parts = append(parts, "cache hit")
		case a.CostUSD > 0:
			parts = append(parts, usd(a.CostUSD))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return stFaint.Render(g.sep() + strings.Join(parts, g.sep()))
}

// entrySubject is what the entry is about: its target, else its item, else
// its project, shortened like a ref when it is one.
func entrySubject(e audit.Entry) string {
	s := e.Target
	if s == "" {
		s = e.Item
	}
	if s == "" {
		s = e.Project
	}
	return refShort(strings.TrimPrefix(s, "work:"))
}

// relAge is a relative time that needs no clock beyond "now": "12m ago",
// "3h ago", "2d ago", then the date.
func relAge(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 45*time.Second:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", max(1, int(d.Minutes())))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Local().Format("2 Jan")
}

// --- history tab ------------------------------------------------------------------

// historyMsg carries one item's audit entries, newest first, to Update.
type historyMsg struct {
	item    string
	entries []audit.Entry
	err     error
}

// loadHistory reads the newest limit audit entries recorded for item (0: all)
// off the UI loop. A nil log answers with an error rather than an empty list.
func loadHistory(log *audit.Log, item string, limit int) tea.Cmd {
	return func() tea.Msg {
		if log == nil {
			return historyMsg{item: item, err: errNoAudit}
		}
		es, err := log.Query(audit.Filter{Item: item, Limit: limit})
		newestFirst(es)
		return historyMsg{item: item, entries: es, err: err}
	}
}

// historyView draws entries (newest first) one per line, exactly w×h cells:
// relative time, outcome glyph, the action in words, and on the right the
// outcome word with the cost for ai.* entries. When more entries exist than
// lines, the last line says how many were left out.
func historyView(g glyphs, entries []audit.Entry, w, h int) string {
	if len(entries) == 0 {
		return fit(g, " "+stFaint.Render("No recorded actions on this item yet."), w, h)
	}
	shown, more := entries, 0
	if len(entries) > h && h > 1 {
		shown = entries[:h-1]
		more = len(entries) - len(shown)
	}
	now := time.Now()
	out := make([]string, 0, len(shown)+1)
	for _, e := range shown {
		mark, word := outcomeMark(g, e)
		left := " " + stFaint.Render(pad(g, relAge(now, e.TS), 9)) + " " + mark + " " + strings.ReplaceAll(e.Action, ".", " ")
		out = append(out, rightAligned(g, left, word+outcomeTail(g, e), w))
	}
	if more > 0 {
		out = append(out, " "+stFaint.Render(fmt.Sprintf("+%d earlier %s ade audit shows them all", more, g.Sep)))
	}
	return fit(g, strings.Join(out, "\n"), w, h)
}

// --- log page ---------------------------------------------------------------------

func entriesWord(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

func (m model) logHeaderLeft() string {
	return stBold.Render("ADE") + "  " + stFaint.Render("home "+m.g.Sep+" worktrees "+m.g.Sep+" ") + stBold.Render("Log")
}

func (m model) logFooter(w int) string {
	l := m.lg
	if l.editing {
		return " " + stBold.Render("/") + " " + l.text + stBold.Render(m.g.Vertical) + "   " + keys("enter", "apply", "esc", "cancel", "ctrl+u", "clear")
	}
	if w < twoPaneMinW {
		if m.showDetail {
			return " " + keys("j/k", "move", "J/K", "scroll", "enter", "go to item", "tab", "list", "L", "back", "?", "keys")
		}
		return " " + keys("j/k", "move", "enter", "go to item", "/", "filter", "a", "actions", "s", "since", "L", "back", "?", "keys")
	}
	return " " + keys("j/k", "move", "enter", "go to target", "/", "filter", "a", "actions: "+l.group.String(), "s", "since: "+l.since.String(), "c", "clear", "J/K", "detail", "r", "reload", "L", "back", "?", "keys")
}

// toolDetailWidth is the width of the detail pane (or of the single pane's
// detail view) at the current terminal size.
func (m model) toolDetailWidth() int {
	w := max(m.w, minW)
	if m.w == 0 {
		w = 120
	}
	if w < twoPaneMinW {
		return w - 2
	}
	return w - w*64/100 - 4
}

// logBody draws the page to exactly w×h cells: the list on the left and the
// selected entry in full on the right (single pane below 100 columns, tab or
// enter swaps).
func (m model) logBody(w, h int) string {
	g, l := m.g, m.lg
	if l.err != nil && len(l.entries) == 0 {
		return toolError(g, "could not read the audit log", l.err, "r tries again", w, h)
	}
	if !l.loaded {
		return fit(m.g, " "+stFaint.Render(m.g.T("reading the audit log…")), w, h)
	}
	if w < twoPaneMinW {
		if m.showDetail {
			return fit(m.g, scrollDetail(g, m.logDetail(w-2), l.dscroll, h), w, h)
		}
		return m.logList(w, h)
	}
	listW := w * 64 / 100
	right := fit(m.g, scrollDetail(g, m.logDetail(w-listW-4), l.dscroll, h), w-listW-3, h)
	return columns(stRule.Render(m.g.Pane), h, m.logList(listW, h), right)
}

// toolBodyH is the height the body of a tool page gets: the screen less the
// header, the rule and the footer.
func (m model) toolBodyH() int {
	h := max(m.h, minH)
	if m.w == 0 {
		h = 36
	}
	return h - 3
}

// maxDetailScroll is the furthest a detail of n lines scrolls, so the last
// line ends on the last row of the pane.
func (m model) maxDetailScroll(n int) int { return max(n-m.toolBodyH(), 0) }

// scrollDetail draws lines from top, at most h rows. When more lines remain
// below the last row says how many, so a clipped pane is never mistaken for
// a complete one.
func scrollDetail(g glyphs, lines []string, top, h int) string {
	top = clamp(top, 0, max(len(lines)-1, 0))
	lines = lines[top:]
	if len(lines) > h && h > 1 {
		more := len(lines) - (h - 1)
		lines = append(lines[:h-1:h-1], stFaint.Render(fmt.Sprintf("%s %d more lines %s J scrolls", g.Down, more, g.Sep)))
	}
	return strings.Join(lines, "\n")
}

// toolError shows a failed read: the cause wrapped whole under the title
// (the end of an error names the reason), and what to do about it.
func toolError(g glyphs, title string, err error, fix string, w, h int) string {
	l := []string{" " + stBad.Render(g.Bad+" "+title)}
	for _, ln := range hardWrap(err.Error(), max(w-4, 10)) {
		l = append(l, "   "+ln)
	}
	return fit(g, strings.Join(append(l, "", "   "+stFaint.Render(fix)), "\n"), w, h)
}

// logList draws the filtered entries, grouped under one rule per day, with
// the selected row kept in view.
func (m model) logList(w, h int) string {
	g, l := m.g, m.lg
	groups := []string{entriesWord(len(l.vis))}
	if l.filtered() {
		groups = []string{fmt.Sprintf("%d of %d entries", len(l.vis), len(l.entries))}
	}
	if l.full {
		groups[0] += fmt.Sprintf(" (newest %d)", logLimit)
	}
	groups = append(groups, "since "+l.since.String())
	if l.group != groupAll {
		groups = append(groups, "actions "+l.group.String())
	}
	if t := strings.TrimSpace(l.text); t != "" {
		groups = append(groups, fmt.Sprintf("matching %q", t))
	}
	if l.err != nil {
		groups = append([]string{g.Warn + " reload failed"}, groups...)
	}
	if l.loading {
		groups = append([]string{g.Sync + " reading"}, groups...)
	}
	out := []string{guideRule(g, "Log", groups, w)}
	if len(l.vis) == 0 {
		msg := fmt.Sprintf("No audit entries in the last %s. s widens the window.", l.since)
		if l.since == sinceAll {
			msg = "No audit entries yet."
		}
		if len(l.entries) > 0 {
			msg = "No entry matches the filters. c clears them."
		}
		out = append(out, "", "   "+stFaint.Render(msg))
		return fit(m.g, strings.Join(out, "\n"), w, h)
	}

	cols := m.logCols(w)
	selLine := 0
	day, dayStart := "", 0
	perDay := map[string]int{}
	dayOf := func(e audit.Entry) string { return e.TS.Local().Format("2006-01-02") }
	for _, idx := range l.vis {
		perDay[dayOf(l.entries[idx])]++
	}
	for i, idx := range l.vis {
		e := l.entries[idx]
		if d := dayOf(e); d != day {
			day, dayStart = d, len(out)
			out = append(out, "   "+guideRule(g, fmtDay(d), dayCount(len(perDay), perDay[d]), w-3))
		}
		if i == l.sel {
			selLine = len(out)
			if selLine == dayStart+1 {
				selLine = dayStart // keep the day's rule in view with its first row
			}
		}
		out = append(out, m.logRow(e, i == l.sel, cols))
	}
	top := 0
	if selLine >= h-1 {
		top = selLine - h + 2
	}
	return fit(m.g, strings.Join(out[min(top, len(out)):], "\n"), w, h)
}

// logCols are the column widths of the list, fitted to its width.
type logCols struct{ actor, action, status, subject int }

func (m model) logCols(w int) logCols {
	l := m.lg
	var c logCols
	for _, idx := range l.vis {
		e := l.entries[idx]
		c.action = max(c.action, lipgloss.Width(e.Action))
		mark, word := outcomeMark(m.g, e)
		c.status = max(c.status, lipgloss.Width(mark+" "+word+outcomeTail(m.g, e)))
	}
	c.action, c.status = min(c.action, 24), min(c.status, 22)
	if w >= 72 {
		c.actor = 8
	}
	room := func() int {
		a := 0
		if c.actor > 0 {
			a = c.actor + 1
		}
		return w - 1 - 3 - 5 - 2 - a - c.action - 1 - c.status - 1
	}
	if room() < 12 {
		c.action = max(12, c.action-(12-room()))
	}
	if room() < 8 {
		c.actor = 0
	}
	c.subject = max(room(), 0)
	return c
}

// logRow is one entry: time, actor, action, subject, and the outcome in
// words (with cost or "cache hit" for AI entries) at the right.
func (m model) logRow(e audit.Entry, sel bool, c logCols) string {
	g := m.g
	lead := "   "
	action := pad(m.g, e.Action, c.action)
	if sel {
		lead = " " + stBold.Render(g.Selected) + " "
		action = stBold.Render(action)
	}
	line := lead + stFaint.Render(e.TS.Local().Format("15:04")) + "  "
	if c.actor > 0 {
		line += stFaint.Render(pad(m.g, e.Actor, c.actor)) + " "
	}
	mark, word := outcomeMark(g, e)
	status := mark + " " + word + outcomeTail(g, e)
	line += action + " " + pad(m.g, entrySubject(e), c.subject) + " " + strings.Repeat(" ", max(0, c.status-lipgloss.Width(status))) + status
	return line
}

// logDetail is the selected entry with every field it has: identity, the ai
// block, and the redacted params pretty-printed.
func (m model) logDetail(w int) []string {
	e, ok := m.lg.selected()
	if !ok {
		return nil
	}
	g := m.g
	w = max(w, 20)
	mark, word := outcomeMark(g, e)
	l := []string{
		stBold.Render(e.Action),
		mark + " " + word + stFaint.Render(g.sep()+e.TS.Local().Format("Mon 2 Jan 2006 15:04:05")+g.sep()+relAge(time.Now(), e.TS)),
		"",
	}
	const keyW = 10
	field := func(k, v string, style lipgloss.Style) {
		if v == "" {
			return
		}
		for i, line := range hardWrap(v, w-keyW) {
			key := strings.Repeat(" ", keyW)
			if i == 0 {
				key = stFaint.Render(pad(m.g, k, keyW))
			}
			l = append(l, key+style.Render(line))
		}
	}
	plain := lipgloss.NewStyle()
	field("id", e.ID, stFaint)
	field("actor", e.Actor, plain)
	field("target", e.Target, plain)
	field("item", e.Item, plain)
	field("project", e.Project, plain)
	if e.DryRun {
		field("dry run", "yes, nothing was changed", plain)
	}
	field("outcome", e.Outcome, plain)
	field("error", e.Error, stBad)
	field("url", e.URL, plain)
	if a := e.AI; a != nil {
		l = append(l, "", stBold.Render("AI"))
		field("profile", a.Profile, plain)
		field("driver", a.Driver, plain)
		field("model", a.Model, plain)
		field("tokens", fmt.Sprintf("%d in %s %d out %s %d cache", a.TokensIn, g.Sep, a.TokensOut, g.Sep, a.TokensCache), plain)
		cost := usd(a.CostUSD)
		if a.CostSource != "" {
			cost += " (" + a.CostSource + ")"
		}
		field("cost", cost, plain)
		field("cache hit", map[bool]string{true: "yes", false: "no"}[a.CacheHit], plain)
		field("artifact", a.Artifact, plain)
		field("dest host", a.DestHost, plain)
		if a.Bytes > 0 {
			field("bytes", fmt.Sprint(a.Bytes), plain)
		}
	}
	if len(e.Params) > 0 {
		l = append(l, "", stBold.Render("Params"))
		if b, err := json.MarshalIndent(e.Params, "", "  "); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				l = append(l, strings.Split(ansi.Hardwrap(line, w-1, true), "\n")...)
			}
		}
	}
	l = append(l, "")
	if ref, ok := m.entryRef(e); ok {
		l = append(l, stFaint.Render("enter opens "+ref+" on Home"))
	} else {
		l = append(l, stFaint.Render("enter: nothing on Home for this entry"))
	}
	return l
}

// hardWrap wraps s at spaces, then breaks any word still wider than w after
// a "/" (a path keeps whole segments together), and cuts only a segment
// that is itself too wide, so a long URL or path is shown whole across lines
// instead of cut with an ellipsis.
func hardWrap(s string, w int) []string {
	var out []string
	for _, line := range wrap(s, w) {
		for lipgloss.Width(line) > w {
			head := ansi.Truncate(line, w, "")
			cut := max(len(head), 1)
			if i := strings.LastIndex(head, "/"); i > 0 {
				cut = i + 1
			}
			out = append(out, line[:cut])
			line = line[cut:]
		}
		out = append(out, line)
	}
	return out
}

// dayCount is the guide word of a day's rule: the day's entry count, unless
// the window is a single day, when the Log rule above already says it.
func dayCount(days, n int) []string {
	if days == 1 {
		return nil
	}
	return []string{entriesWord(n)}
}
