package tui

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/coredev-uk/ade/internal/agile"
	"github.com/coredev-uk/ade/internal/domain"
)

const (
	barW     = 24 // cells of a progress or burn-down bar
	narrowW  = 100
	keyColW  = 11
	labelPad = "   "
)

// agileHeaderLeft is the header's left side while an agile page is open.
func (m model) agileHeaderLeft() string {
	name := "Standup"
	if m.ag.kind == agileReview {
		name = "Sprint review"
	}
	return stBold.Render("ADE") + "  " + stFaint.Render("home "+m.g.Sep+" worktrees "+m.g.Sep+" ") + stBold.Render(name)
}

func (m model) agileFooter(w int) string {
	narrow := w < narrowW
	switch {
	case m.ag.kind == agileStandup && narrow:
		return " " + keys("j/k", "scroll", "r", "reload", "n", "narrative", "y", "copy", "esc", "back", "?", "keys")
	case m.ag.kind == agileStandup:
		return " " + keys("j/k", "scroll", "r", "reload", "n", "AI narrative", "y", "copy as Markdown", "esc", "back", "t", "home", "i", "notifications", "?", "keys", "q", "quit")
	case w < 150: // the review's keys no longer fit whole below this
		return " " + keys("j/k", "scroll", "[ ]", "sprint", "m", "mine", "J/K", "item", ">", "move", "M", "move mine", "esc", "back", "?", "keys")
	}
	return " " + keys("j/k", "scroll", "[ ]", "sprint", "m", "mine: "+onOff(m.ag.mine), "J/K", "carried item", ">", "move to next sprint", "M", "move mine", "p", "project", "r", "reload", "n", "AI narrative", "y", "copy", "esc", "back", "?", "keys", "q", "quit")
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// agileBody draws the open page, scrolled, to exactly w×h cells.
func (m model) agileBody(w, h int) string {
	var lines []string
	if m.ag.kind == agileStandup {
		lines = m.standupLines(w)
	} else {
		lines = m.reviewLines(w)
	}
	top := min(m.ag.scroll, max(0, len(lines)-h))
	if top > 0 {
		lines = lines[top:]
	}
	return fit(m.g, strings.Join(lines, "\n"), w, h)
}

// rightAligned puts right at the right edge of a w-cell line; left is cut with
// an ellipsis when the two do not fit.
func rightAligned(g glyphs, left, right string, w int) string {
	if right == "" {
		return left
	}
	room := w - lipgloss.Width(right) - 2
	return pad(g, left, room) + " " + right
}

func refShort(ref string) string {
	proj, rest, ok := strings.Cut(ref, "#")
	sep := "#"
	if !ok {
		proj, rest, ok = strings.Cut(ref, "!")
		sep = "!"
	}
	if !ok {
		return ref
	}
	if i := strings.LastIndex(proj, "/"); i >= 0 {
		proj = proj[i+1:]
	}
	return proj + sep + rest
}

func notice(g glyphs, msg string) string { return " " + stFaint.Render(g.Info+" "+msg) }

func loadError(g glyphs, err error, w int) []string {
	if errors.Is(err, agile.ErrNoCache) {
		return []string{notice(g, "Nothing cached yet. U syncs now; r reloads this page.")}
	}
	var out []string
	for i, l := range wrap("could not build this page: "+err.Error(), w-5) {
		lead := "   "
		if i == 0 {
			lead = " " + stBad.Render(g.Bad) + " "
		}
		out = append(out, lead+l)
	}
	return out
}

// --- standup -------------------------------------------------------------------

func (m model) standupLines(w int) []string {
	g := m.g
	s := m.ag.standup
	if s == nil {
		if m.ag.standupErr != nil {
			return loadError(g, m.ag.standupErr, w)
		}
		return []string{notice(g, m.g.T("reading standup…"))}
	}
	guide := fmt.Sprintf("%s %s since %s", fmtDay(s.Date), g.Sep, fmtDay(s.Yesterday))
	if m.ag.loading {
		guide = g.Sync + " refreshing " + g.Sep + " " + guide
	}
	out := []string{section("Standup", guide, w, g)}
	if m.ag.standupErr != nil {
		out = append(out, " "+stWarn.Render(g.Warn+" refresh failed: ")+m.ag.standupErr.Error())
	}
	if s.Narrative != "" {
		out = append(out, "")
		out = append(out, narrativeLines(g, s.Narrative, w)...)
	}

	out = append(out, "", section("Yesterday", fmt.Sprintf("%s %s %s", fmtDay(s.Yesterday), g.Sep, countWord(len(s.Done), "change")), w, g))
	if len(s.Done) == 0 {
		out = append(out, labelPad+stFaint.Render("nothing recorded"))
	}
	for _, e := range s.Done {
		mark := g.Done
		if e.Kind == "mr.merged" || e.Kind == "issue.closed" || e.Kind == "mr.approved" {
			mark = g.OK
		}
		when := stFaint.Render(e.At.Local().Format("15:04") + " " + sourceWord(e.Source))
		out = append(out, rightAligned(m.g, labelPad+mark+" "+e.Text, when, w))
	}

	out = append(out, "", section("Today", countWord(len(s.Today), "item")+" in your court", w, g))
	if len(s.Today) == 0 {
		out = append(out, labelPad+stFaint.Render("nothing in your court"))
	}
	for _, f := range s.Today {
		out = append(out, m.focusLines(f, w)...)
	}

	guide = "none"
	if n := len(s.Blockers); n > 0 {
		guide = countWord(n, "blocker")
	}
	out = append(out, "", section("Blockers", guide, w, g))
	if len(s.Blockers) == 0 {
		out = append(out, labelPad+stFaint.Render("nothing in your way"))
	}
	for _, b := range s.Blockers {
		lead := labelPad + stWarn.Bold(true).Render(g.Warn) + " "
		head := lead + stBold.Render(refShort(b.Ref)) + " " + b.Title
		if tail := " " + g.Sep + " " + b.Reason; lipgloss.Width(head)+lipgloss.Width(tail) <= w-2 {
			out = append(out, pad(m.g, head+stFaint.Render(tail), w))
			continue
		}
		out = append(out, pad(m.g, head, w))
		for _, l := range wrap(b.Reason, w-lipgloss.Width(lead)-2) {
			out = append(out, strings.Repeat(" ", lipgloss.Width(lead))+stFaint.Render(l))
		}
	}
	out = append(out, warningLines(g, s.Warnings, w)...)
	return out
}

// warningLines lists unreadable sources under a blank line, each wrapped at
// word boundaries behind the warn glyph.
func warningLines(g glyphs, warns []string, w int) []string {
	if len(warns) == 0 {
		return nil
	}
	out := []string{""}
	for _, warn := range warns {
		for i, l := range wrap(warn, w-5) {
			lead := "   "
			if i == 0 {
				lead = " " + stWarn.Render(g.Warn) + " "
			}
			out = append(out, lead+stFaint.Render(l))
		}
	}
	return out
}

func sourceWord(src string) string {
	if src == agile.SourceEvents {
		return "sync"
	}
	return src
}

func countWord(n int, noun string) string { return fmt.Sprintf("%d %s", n, plural(n, noun)) }

// narrativeLines sets the AI narrative behind a faint rail, one blank rail
// line between its paragraphs.
func narrativeLines(g glyphs, text string, w int) []string {
	rail := labelPad + stRule.Render(g.Vertical) + " "
	var out []string
	for _, para := range strings.Split(text, "\n") {
		if strings.TrimSpace(para) == "" {
			continue
		}
		if len(out) > 0 {
			out = append(out, strings.TrimRight(rail, " "))
		}
		for _, l := range wrap(para, w-5) {
			out = append(out, rail+l)
		}
	}
	return out
}

// focusLines is one thing on today's plate: the item's station line when it is
// a cached work item, else a plain mark (a review request). The reason follows
// the title when it fits whole; otherwise it moves to a faint second line
// under the title rather than being cut.
func (m model) focusLines(f agile.Focus, w int) []string {
	g := m.g
	var lead, right string
	if it := m.findItem(f.Ref); it != nil {
		lead, right = labelPad+track(g, it, 1)+"  ", weightText(g, it.Weight)
	} else {
		lead = labelPad + stFaint.Render(g.Ahead) + " "
	}
	head := lead + stBold.Render(refShort(f.Ref)) + " " + f.Title
	reason := courtTextPlain(g, f)
	tail := " " + g.Sep + " " + reason
	if lipgloss.Width(head)+lipgloss.Width(tail) <= w-2-lipgloss.Width(right) {
		return []string{rightAligned(m.g, head+stFaint.Render(tail), right, w)}
	}
	out := []string{rightAligned(m.g, head, right, w)}
	indent := strings.Repeat(" ", lipgloss.Width(lead))
	for _, l := range wrap(reason, w-lipgloss.Width(lead)-2) {
		out = append(out, indent+stFaint.Render(l))
	}
	return out
}

// courtTextPlain is what follows a Today title: every item there is in your
// court, so only the reason is worth the room.
func courtTextPlain(_ glyphs, f agile.Focus) string {
	if f.Reason == "" {
		return f.Court
	}
	return f.Reason
}

func (m model) findItem(ref string) *domain.WorkItem {
	for i := range m.results {
		for j := range m.results[i].Items {
			if m.results[i].Items[j].Ref.String() == ref {
				return &m.results[i].Items[j]
			}
		}
	}
	return nil
}

// wrap breaks prose at word boundaries into lines of at most w cells.
func wrap(s string, w int) []string {
	w = max(w, 10)
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case lipgloss.Width(line)+1+lipgloss.Width(word) <= w:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return out
}

// --- sprint review ---------------------------------------------------------------

func (m model) reviewLines(w int) []string {
	g := m.g
	r := m.ag.review
	if r == nil {
		if m.ag.reviewErr != nil {
			return loadError(g, m.ag.reviewErr, w)
		}
		return []string{notice(g, m.g.T("reading the sprint from the forge…"))}
	}
	line := ink(r.Project)
	guide := fmtSpan(m.g, r.Sprint.Start.String(), r.Sprint.Due.String()) + " " + g.Sep + " by " + r.Basis
	if r.Mine {
		guide += " " + g.Sep + " mine"
	}
	if m.ag.loading {
		guide = g.Sync + " refreshing " + g.Sep + " " + guide
	}
	out := []string{section("Sprint review "+g.Sep+" "+r.Sprint.Title, guide, w, g)}
	if m.ag.reviewErr != nil {
		out = append(out, " "+stWarn.Render(g.Warn+" refresh failed: ")+m.ag.reviewErr.Error())
	}
	if r.Narrative != "" {
		out = append(out, "")
		out = append(out, narrativeLines(g, r.Narrative, w)...)
	}
	out = append(out, "")
	kv := func(k, v string) string { return labelPad + stFaint.Render(pad(m.g, k, keyColW)) + v }
	out = append(out,
		kv("Committed", g.T(agile.FormatTotals(r.Committed, r.Basis))),
		kv("Added", g.T(agile.FormatTotals(r.Added, r.Basis))+stFaint.Render(" after the first day")),
		kv("Done", stBold.Render(g.T(agile.FormatTotals(r.Done, r.Basis)))),
		kv("Carried", g.T(agile.FormatTotals(r.Carried, r.Basis))),
	)
	total := metric(r.Committed, r.Basis) + metric(r.Added, r.Basis)
	out = append(out, labelPad+stFaint.Render(pad(m.g, "", keyColW))+bar(g, line, metric(r.Done, r.Basis), total)+"  "+scopeWords(r, total))

	out = append(out, "", section("Velocity", velocityGuide(g, r.Velocity), w, g))
	out = append(out, m.velocityLines(r.Velocity, line)...)

	if len(r.Burndown) > 0 {
		out = append(out, "", section("Burn-down", "remaining "+r.Basis+" at the end of each working day", w, g))
		peak := 1
		for _, p := range r.Burndown {
			peak = max(peak, p.Remaining, int(p.Ideal+0.5))
		}
		for _, p := range r.Burndown {
			out = append(out, pad(m.g, labelPad+stFaint.Render(pad(m.g, fmtDay(p.Date), keyColW))+bar(g, line, p.Remaining, peak)+
				fmt.Sprintf(" %3d", p.Remaining)+stFaint.Render(fmt.Sprintf("  ideal %.1f", p.Ideal)), w))
		}
	}

	if len(r.Epics) > 0 {
		out = append(out, "", section("By epic", countWord(len(r.Epics), "group"), w, g))
		for _, e := range r.Epics {
			name := e.Epic
			if name == "" {
				name = "no epic"
			}
			out = append(out, pad(m.g, labelPad+stBold.Render(name)+stFaint.Render(" "+g.Sep+" ")+agile.EpicSummary(e, r.Basis), w))
		}
	}

	out = append(out, "", section("Items", fmt.Sprintf("%d done %s %d carried", r.Done.Count, g.Sep, r.Carried.Count), w, g))
	statusW := 0
	for _, it := range r.Items {
		statusW = max(statusW, lipgloss.Width(itemStatus(it)))
	}
	for _, it := range r.Items {
		out = append(out, m.reviewItemLines(it, line, statusW, w)...)
	}
	out = append(out, warningLines(g, r.Warnings, w)...)
	return out
}

// metric is a totals' figure in the review's basis.
func metric(t agile.Totals, basis string) int {
	if basis == agile.BasisWeight {
		return t.Weight
	}
	return t.Count
}

func scopeWords(r *agile.Review, total int) string {
	if r.Basis == agile.BasisWeight {
		return fmt.Sprintf("%dw of %dw done", metric(r.Done, r.Basis), total)
	}
	return fmt.Sprintf("%d of %d items done", metric(r.Done, r.Basis), total)
}

// bar is a station line in the project's ink: travelled for the part done,
// to-come for the rest.
func bar(g glyphs, line lipgloss.Style, part, whole int) string {
	if whole <= 0 {
		return stFaint.Render(strings.Repeat(g.ToCome, barW))
	}
	n := min(barW, (part*barW+whole/2)/whole)
	if part > 0 && n == 0 {
		n = 1
	}
	return line.Render(strings.Repeat(g.Travelled, n)) + stFaint.Render(strings.Repeat(g.ToCome, barW-n))
}

func velocityGuide(g glyphs, v agile.Velocity) string {
	if len(v.Sprints) == 0 {
		return "no closed sprints"
	}
	if v.Average == nil {
		return "by " + v.Basis + " " + g.Sep + " " + countWord(len(v.Sprints), "closed sprint")
	}
	unit := " items"
	if v.Basis == agile.BasisWeight {
		unit = "w"
	}
	return fmt.Sprintf("by %s %s last %s %s average %.1f%s per sprint", v.Basis, g.Sep, countWord(len(v.Sprints), "sprint"), g.Sep, *v.Average, unit)
}

func (m model) velocityLines(v agile.Velocity, line lipgloss.Style) []string {
	g := m.g
	if len(v.Sprints) == 0 {
		return []string{labelPad + stFaint.Render("no closed sprints to compare")}
	}
	var out []string
	for _, s := range v.Sprints {
		head := labelPad + stFaint.Render(pad(m.g, s.Title, keyColW))
		switch {
		case v.Basis == agile.BasisWeight && s.Velocity == nil:
			out = append(out, head+stFaint.Render(g.Ahead+" no weighted work "+g.Sep+" not in the average"))
		case v.Basis == agile.BasisWeight:
			tail := fmt.Sprintf("  %dw of %dw", *s.Velocity, s.Scope.Weight)
			if s.Scope.Unweighted > 0 {
				tail += stFaint.Render(fmt.Sprintf(" %s %d unweighted not counted", g.Sep, s.Scope.Unweighted))
			}
			out = append(out, head+bar(g, line, s.Done.Weight, s.Scope.Weight)+tail)
		default:
			out = append(out, head+bar(g, line, s.Done.Count, s.Scope.Count)+fmt.Sprintf("  %d of %s done", s.Done.Count, countWord(s.Scope.Count, "item")))
		}
	}
	return out
}

func itemStatus(it agile.ReviewItem) string {
	switch {
	case it.Done && it.KeptOpen:
		return "done, kept open"
	case it.Done:
		return "done"
	}
	return "carried"
}

// reviewItemLines is one sprint item: mark, status word (the column is as wide
// as the widest word present), ref, title and weight. What else is known of it
// follows the title when it fits whole, else sits on a faint second line.
func (m model) reviewItemLines(it agile.ReviewItem, line lipgloss.Style, statusW, w int) []string {
	g := m.g
	mark, word := stFaint.Render(g.Ahead), stFaint.Render(itemStatus(it))
	if it.Done {
		mark, word = line.Render(g.Done), itemStatus(it)
	} else if sel, ok := m.selectedCarried(); ok && sel.Ref == it.Ref {
		mark = stBold.Render(g.Selected) // the carried item `>` moves
	}
	weight := stFaint.Render(m.g.T("–"))
	if it.Weight != nil {
		weight = fmt.Sprintf("%dw", *it.Weight)
	}
	lead := labelPad + mark + " " + pad(m.g, word, statusW) + " "
	left := lead + stBold.Render(refShort(it.Ref.String())) + " " + it.Title
	var meta []string
	if it.Added {
		meta = append(meta, "added mid-sprint")
	}
	if len(it.MRs) > 0 {
		meta = append(meta, "merged "+strings.Join(it.MRs, ", "))
	}
	if len(meta) == 0 {
		return []string{rightAligned(m.g, left, weight, w)}
	}
	tail := " " + g.Sep + " " + strings.Join(meta, " "+g.Sep+" ")
	if lipgloss.Width(left)+lipgloss.Width(tail) <= w-2-lipgloss.Width(weight) {
		return []string{rightAligned(m.g, left+stFaint.Render(tail), weight, w)}
	}
	indent := strings.Repeat(" ", lipgloss.Width(lead))
	out := []string{rightAligned(m.g, left, weight, w)}
	for _, l := range wrap(strings.Join(meta, " "+g.Sep+" "), w-lipgloss.Width(lead)-2) {
		out = append(out, indent+stFaint.Render(l))
	}
	return out
}
