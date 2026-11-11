package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/agent"
)

// agStateMark is the glyph and word of a run state: always both, so a
// NO_COLOR or ascii terminal reads the same.
func (m model) agStateMark(s agent.State) string {
	g := m.g
	switch s {
	case agent.StatePlanning, agent.StateRunning:
		return stBold.Render(g.Sync + " " + string(s))
	case agent.StateAwaiting:
		return stWarn.Render(g.Warn + " needs approval")
	case agent.StateReview:
		return stWarn.Render(g.Train + " review")
	case agent.StatePushed:
		return stOK.Render(g.OK + " pushed")
	case agent.StateFailed:
		return stBad.Render(g.Bad + " failed")
	case agent.StateCancelled:
		return stFaint.Render(g.Sep + " cancelled")
	}
	return stFaint.Render(g.Ahead + " " + string(s))
}

// agentOverlay draws the dialog, or "" when closed.
func (m model) agentOverlay(w, h int) string {
	d := m.agt
	if d == nil {
		return ""
	}
	dw := min(w, 100) // a reading measure; the block is padded to the screen in agentOverlayLayer
	inner := dw - 4
	title := "Agent"
	if d.item != nil {
		title += " · " + d.item.Ref.String()
	} else {
		title += " · all runs"
	}
	var l []string
	if d.item != nil && d.stage == agList {
		l = append(l, stFaint.Render(ansi.Truncate(d.item.Issue.Title, inner, "…")), "")
	}
	if d.err != nil {
		l = append(l, stBad.Render(ansi.Wrap(m.g.Bad+" "+d.err.Error(), inner, "")), "", keys("esc", "close"))
		return m.dialog(title, l, dw)
	}
	var hint string
	switch d.stage {
	case agList:
		var hh string
		l, hh = m.agListView(d, inner, h)
		if d.item != nil {
			l = append([]string{stFaint.Render(ansi.Truncate(d.item.Issue.Title, inner, "…")), ""}, l...)
		}
		hint = hh
	case agPlan:
		l, hint = m.agPlanView(d, inner, h)
	case agDetail:
		l, hint = m.agDetailView(d, inner, h)
	case agConfirm:
		l, hint = m.agConfirmView(d, inner)
	}
	if d.busy != "" {
		l = append(l, "", stFaint.Render(m.g.Sync+" "+d.busy+"…"))
	}
	if d.warn != "" {
		l = append(l, "", stWarn.Render(ansi.Wrap(m.g.Warn+" "+d.warn, inner, "")))
	}
	l = append(l, "", hint)
	return m.dialog(title, l, dw)
}

func (m model) agListView(d *agentDialog, inner, h int) ([]string, string) {
	g := m.g
	var l []string
	hint := keys("enter", "open", "x", "cancel run", "esc", "close")
	if d.item != nil {
		l = append(l, section("Start", "work in the item's worktree", inner, g))
		l = append(l, ansi.Truncate(stBold.Render("p")+" plan                "+stFaint.Render("read-only: issue, comments, AGENTS.md"), inner, "…"))
		switch {
		case d.mr != nil:
			l = append(l, ansi.Truncate(stBold.Render("c")+" address comments    "+stFaint.Render(fmt.Sprintf("%s · %d unresolved, replies drafted", d.mr.Ref, d.mr.Unresolved)), inner, "…"))
		default:
			l = append(l, stFaint.Render("c address comments    no open MR with unresolved threads"))
		}
		l = append(l, "")
		hint = keys("p", "plan", "c", "address", "enter", "open", "x", "cancel", "esc", "close")
	}
	guide := countWord(len(d.runs), "run")
	if d.item == nil {
		guide += " · every item"
	}
	l = append(l, section("Runs", guide, inner, g))
	if len(d.runs) == 0 {
		l = append(l, stFaint.Render("no runs yet"))
		return l, hint
	}
	room := max(4, h-18)
	top := clamp(d.sel-room+1, 0, max(len(d.runs)-room, 0))
	now := time.Now()
	for i := top; i < min(len(d.runs), top+room); i++ {
		l = append(l, m.agRunLine(d.runs[i], i == d.sel, d.item == nil, inner, now))
	}
	if len(d.runs) > top+room {
		l = append(l, stFaint.Render(fmt.Sprintf("… %d more", len(d.runs)-top-room)))
	}
	return l, hint
}

// agRunLine is one run: cursor, state, id, kind, spend against the cap,
// elapsed. Every-item lists lead with the repo's ink and the item.
func (m model) agRunLine(r agent.Run, sel, withItem bool, inner int, now time.Time) string {
	cur := " "
	if sel {
		cur = m.g.Selected
	}
	id, idW := r.ID, 19
	if inner < 70 || withItem {
		_, id, _ = strings.Cut(id, "-") // the date is in the full id on show
		idW = 10
	}
	cost := fmt.Sprintf("$%.4f/$%.2f", r.CostUSD, r.CapUSD)
	el := fmt.Sprintf("%-6s", r.Elapsed(now).Round(time.Second).String())
	head := cur + " " + pad(m.agStateMark(r.State), 17) + " " + pad(id, idW) + " " + pad(string(r.Kind), 7)
	tail := stFaint.Render(cost + " " + m.g.Sep + " " + el)
	mid := ""
	if withItem {
		mid = " " + ink(r.Repo).Render(refShortOf(r.Item())) + " "
	}
	line := head + mid
	gap := inner - lipgloss.Width(line) - lipgloss.Width(tail)
	if gap < 1 {
		return ansi.Truncate(line+" "+tail, inner, "…")
	}
	return line + strings.Repeat(" ", gap) + tail
}

func refShortOf(ref string) string { return refShort(ref) }

func (m model) agPlanView(d *agentDialog, inner, h int) ([]string, string) {
	g := m.g
	r := d.cur
	guide := fmt.Sprintf("%s %s $%.4f", r.ID, g.Sep, r.CostUSD)
	if r.Approved {
		guide += " " + g.Sep + " approved"
	} else {
		guide += " " + g.Sep + " awaiting your approval"
	}
	l := []string{section("Plan", guide, inner, g), ""}
	lines := wrap(d.plan, inner-2)
	room := max(5, h-16)
	d.scroll = clamp(d.scroll, 0, max(len(lines)-room, 0))
	for _, ln := range lines[d.scroll:min(len(lines), d.scroll+room)] {
		l = append(l, stFaint.Render(g.Vertical)+" "+ln)
	}
	if more := len(lines) - d.scroll - room; more > 0 {
		l = append(l, stFaint.Render(g.Vertical+fmt.Sprintf(" … %d more lines (j scrolls)", more)))
	}
	l = append(l, "", stFaint.Render(fmt.Sprintf("worktree %s · %s · cap $%.2f", "…/"+filepath.Base(r.Worktree), r.Driver, r.CapUSD)))
	enter := "approve"
	if r.Approved {
		enter = "run it in the worktree"
	}
	return l, keys("enter", enter, "e", "edit plan", "j/k", "scroll", "x", "discard", "esc", "back")
}

func (m model) agDetailView(d *agentDialog, inner, h int) ([]string, string) {
	g, r := m.g, d.cur
	l := []string{section("Run "+r.ID, string(r.Kind)+" "+g.Sep+" "+string(r.State), inner, g)}
	row := func(k, v string) { l = append(l, ansi.Truncate(stFaint.Render(pad(k, 10))+v, inner, "…")) }
	row("state", m.agStateMark(r.State))
	row("item", r.Item()+" "+stFaint.Render("in "+r.Repo+" on "+r.Branch))
	row("driver", r.Driver+" "+stFaint.Render(r.Model))
	row("cost", fmt.Sprintf("$%.4f", r.CostUSD)+stFaint.Render(fmt.Sprintf(" of $%.2f cap", r.CapUSD)))
	row("elapsed", r.Elapsed(time.Now()).Round(time.Second).String())
	if r.MR != nil {
		row("MR", r.MR.String())
	}
	if len(r.Commits) > 0 {
		row("commits", strings.Join(r.Commits, ", "))
	}
	if r.Drafts > 0 {
		row("replies", fmt.Sprintf("%d drafted, pending your triage in the MR's Review tab", r.Drafts))
	}
	for _, c := range r.Calls {
		if c.StoppedBy != "" && r.Error == "" {
			row("call", stBad.Render(g.Bad+" stopped at the cost cap")+fmt.Sprintf(" %s %s %s $%.4f %s %d turns", g.Sep, c.Kind, g.Sep, c.CostUSD, g.Sep, c.Turns))
			continue
		}
		row("call", fmt.Sprintf("%-7s exit %d %s $%.4f %s %d turns", c.Kind, c.Exit, g.Sep, c.CostUSD, g.Sep, c.Turns))
	}
	if r.Error != "" {
		l = append(l, stBad.Render(ansi.Wrap(g.Bad+" "+r.Error, inner, "")))
	}
	if ds := d.diff; ds != nil {
		l = append(l, "", section("Diff", fmt.Sprintf("%s · +%d -%d", countWord(len(ds.Files), "file"), ds.Added(), ds.Removed()), inner, g))
		room := max(3, h-24)
		for i, f := range ds.Files {
			if i == room {
				l = append(l, stFaint.Render(fmt.Sprintf("… %d more files (ade agent diff %s)", len(ds.Files)-room, r.ID)))
				break
			}
			l = append(l, ansi.Truncate(stBold.Render(f.Status)+" "+pad(f.Path, max(10, inner-20))+" "+agCount("+", f.Added, stOK)+" "+agCount("-", f.Removed, stBad), inner, "…"))
		}
		if len(ds.Files) == 0 {
			l = append(l, stFaint.Render("no changes"))
		}
	}
	hint := keys("l", "lazygit", "esc", "back")
	if r.State == agent.StateReview {
		hint = keys("l", "lazygit", "P", "push", "x", "discard", "esc", "back")
	}
	if d.plan != "" && r.Kind == agent.KindPlan {
		hint += "   " + keys("p", "plan")
	}
	return l, hint
}

func (m model) agConfirmView(d *agentDialog, inner int) ([]string, string) {
	g, r := m.g, d.cur
	l := []string{section("Push", "your approval of the diff", inner, g), ""}
	if ds := d.diff; ds != nil {
		l = append(l, fmt.Sprintf("%s · +%d -%d", countWord(len(ds.Files), "file"), ds.Added(), ds.Removed()))
	}
	l = append(l, "")
	for _, ln := range wrap("Commits everything in the worktree as the configured git user, marked (ade\x00agent), then pushes "+stBold.Render(r.Branch)+". The merge request updates by itself.", inner) {
		l = append(l, strings.ReplaceAll(ln, "\x00", " "))
	}
	return l, keys("y", "commit and push", "n", "back")
}

// agentOverlayLayer places the dialog at the top, centred in a blank field
// the size of the body, so no fragment of the list or preview beneath shows
// beside or below it.
func (m model) agentOverlayLayer(w, h int) *lipgloss.Layer {
	return blankFieldLayer(m.agentOverlay(w, h), w, h)
}

// blankFieldLayer is the one placement every dialog shares: the block sits at
// the top, centred, in a blank field the size of the body. "" yields nil.
func blankFieldLayer(d string, w, h int) *lipgloss.Layer {
	if d == "" {
		return nil
	}
	dw := lipgloss.Width(d)
	left := (w - dw) / 2
	lines := strings.Split(d, "\n")
	rows := max(h-2, len(lines)+1)
	out := make([]string, rows)
	for i := range out {
		ln := ""
		if i < len(lines) {
			ln = lines[i]
		}
		out[i] = strings.Repeat(" ", left) + pad(ln, dw) + strings.Repeat(" ", w-left-dw)
	}
	return lipgloss.NewLayer(strings.Join(out, "\n")).X(0).Y(1).Z(2)
}

// agCount is a diff count: coloured when it says something, faint at zero.
func agCount(sign string, n int, st lipgloss.Style) string {
	t := fmt.Sprintf("%s%d", sign, n)
	if n == 0 {
		return stFaint.Render(t)
	}
	return st.Render(t)
}
