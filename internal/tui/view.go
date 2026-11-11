package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/notify"
)

const (
	minW, minH  = 60, 16
	twoPaneMinW = 100
	drawerW     = 60
)

func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "ADE"
	return v
}

func (m model) render() string {
	w, h := max(m.w, minW), max(m.h, minH)
	if m.w == 0 {
		w, h = 120, 36 // before the first WindowSizeMsg
	}
	body := m.body(w, h-3)
	screen := m.header(w) + "\n\n" + body + "\n" + m.footer(w)
	layers := []*lipgloss.Layer{lipgloss.NewLayer(fit(screen, w, h))}
	// On narrow terminals overlays take the full width: a partly covered list
	// would show cut-off fragments of the rows beneath.
	narrow := w < twoPaneMinW
	if m.drawer {
		dw := min(drawerW, w-4)
		if narrow {
			dw = w
		}
		d := m.drawerView(dw, h-2)
		layers = append(layers, lipgloss.NewLayer(fit(d, dw, h-2)).X(w-dw).Y(1).Z(1))
	}
	if m.help {
		hw := min(76, w-4)
		if narrow {
			hw = w
		}
		d := helpView(hw, m.g)
		layers = append(layers, lipgloss.NewLayer(d).X((w-lipgloss.Width(d))/2).Y(max(1, (h-lipgloss.Height(d))/2)).Z(2))
	}
	return lipgloss.NewCompositor(layers...).Render()
}

// --- header / footer -----------------------------------------------------------

func (m model) header(w int) string {
	// No project tabs: nothing is filtered in Phase 0, and the body already
	// names every project. The header carries scope as a count.
	repos := 0
	for _, p := range m.projects {
		repos += len(p.Repos)
	}
	left := stBold.Render("ADE")
	if len(m.projects) > 0 {
		left += stFaint.Render(fmt.Sprintf("  %s %d %s %s %d %s", m.g.Sep, len(m.projects), plural(len(m.projects), "project"), m.g.Sep, repos, plural(repos, "repo")))
	}
	var mid string
	switch {
	case m.updating && m.progress.Total > 0:
		mid = m.g.Sync + fmt.Sprintf(" updating %d/%d", m.progress.Done, m.progress.Total)
	case m.updating:
		mid = m.g.Sync + " updating"
	case !m.lastUpdate.IsZero():
		mid = stFaint.Render(m.g.Sync + " updated " + ago(m.lastUpdate))
		if m.lastReport != nil && m.lastReport.Severity() >= events.Warn {
			mid = sevStyle(m.lastReport.Severity()).Render(m.g.Sync+" "+m.lastReport.Title()) + stFaint.Render(" · "+ago(m.lastUpdate))
		}
	}
	right := stFaint.Render("no unread")
	if m.unread > 0 {
		right = stBold.Render(fmt.Sprintf("%s %d unread", m.g.Unread, m.unread))
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(mid) - lipgloss.Width(right) - 2
	if gap < 4 {
		return " " + left + strings.Repeat(" ", max(1, w-lipgloss.Width(left)-lipgloss.Width(right)-2)) + right
	}
	return " " + left + strings.Repeat(" ", gap/2) + mid + strings.Repeat(" ", gap-gap/2) + right
}

func (m model) footer(w int) string {
	if n := len(m.toasts); n > 0 {
		t := m.toasts[n-1]
		line := " " + sevGlyph(m.g, t.ev.Severity) + " " + t.ev.Title
		if t.expires.IsZero() {
			line += stFaint.Render("   esc dismiss")
		}
		if n > 1 {
			line += stFaint.Render(fmt.Sprintf("   +%d earlier", n-1))
		}
		return ansi.Truncate(line, w, "…")
	}
	if w < twoPaneMinW {
		return " " + keys("j/k", "move", "tab", "details", "U", "update", "i", "inbox", "?", "keys", "q", "quit")
	}
	return " " + keys("j/k", "move", "U", "update now", "y", "copy path", "i", "inbox", "?", "keys", "q", "quit")
}

func keys(kv ...string) string {
	parts := make([]string, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, stBold.Render(kv[i])+" "+stFaint.Render(kv[i+1]))
	}
	return strings.Join(parts, "   ")
}

// --- body ------------------------------------------------------------------------

func (m model) body(w, h int) string {
	if m.loadErr != nil {
		return fit(" "+stBad.Render(m.g.Bad+" could not read projects: ")+m.loadErr.Error(), w, h)
	}
	if !m.loaded {
		return fit(" "+stFaint.Render("reading projects…"), w, h)
	}
	if len(m.projects) == 0 {
		return m.empty(w, h)
	}
	if w < twoPaneMinW {
		// Single pane: the list, or (tab) the selected tree's details.
		if m.showDetail {
			return fit(m.detail(w-2), w, h)
		}
		return m.lines(w, h)
	}
	listW := w * 64 / 100
	right := fit(m.detail(w-listW-4), w-listW-3, h) // one cell of right margin
	return columns(stRule.Render(" │ "), h, m.lines(listW, h), right)
}

func (m model) empty(w, h int) string {
	msg := []string{
		stBold.Render("No projects on this machine yet."),
		"",
		"Add one from a manifest file, URL, or git source:",
		"  " + stBold.Render("ade project add <manifest>"),
		"",
		stFaint.Render("Config: " + m.app.Config.File),
	}
	block := strings.Join(msg, "\n")
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, block)
}

const repoCol = 10

// lines renders every project as a section of repo lines (each repo a metro
// line whose stations are its worktrees, baseline first), scrolled so the
// selected tree stays in view.
func (m model) lines(w, h int) string {
	selLine := 0
	var out []string
	idx := 0
	for _, p := range m.projects {
		trees := 0
		for _, r := range p.Repos {
			if r.Baseline != nil {
				trees++
			}
			trees += len(r.Worktrees)
		}
		guide := fmt.Sprintf("%d repos %s %d trees", len(p.Repos), m.g.Sep, trees)
		if !p.LastUpdate.IsZero() {
			guide += " " + m.g.Sep + " updated " + ago(p.LastUpdate)
		}
		out = append(out, section(p.Name, guide, w, m.g))
		for _, r := range p.Repos {
			first := true
			for idx < len(m.rows) && m.rows[idx].project == p.Name && m.rows[idx].repo.Name == r.Name {
				if idx == m.sel {
					selLine = len(out)
				}
				last := idx+1 >= len(m.rows) || m.rows[idx+1].project != p.Name || m.rows[idx+1].repo.Name != r.Name
				out = append(out, m.treeLine(m.rows[idx], idx == m.sel, first, last, w))
				first = false
				idx++
			}
		}
		out = append(out, "")
	}
	top := 0
	if selLine >= h-1 {
		top = selLine - h + 2
	}
	return fit(strings.Join(out[min(top, len(out)):], "\n"), w, h)
}

func section(title, guide string, w int, g glyphs) string {
	t := " " + stBold.Render(title) + " "
	gd := " " + stFaint.Render(guide)
	fill := max(1, w-lipgloss.Width(t)-lipgloss.Width(gd)-1)
	return t + stRule.Render(strings.Repeat(g.Rule, fill)) + gd
}

func (m model) treeLine(r row, selected, first, last bool, w int) string {
	mark := "   "
	if selected {
		mark = " " + stBold.Render(m.g.Selected) + " "
	}
	name := strings.Repeat(" ", repoCol)
	if first {
		name = pad(r.repo.Name, repoCol)
		if !r.repo.InManifest {
			name = stFaint.Render(name)
		}
	}
	line := ink(r.repo.Name)
	var station, label string
	switch {
	case r.repo.Error != "":
		station, label = stBad.Render(m.g.Bad), stBad.Render(r.repo.Error)
	case !r.repo.Present:
		station, label = stFaint.Render(m.g.Ahead), stFaint.Render("not cloned "+m.g.Sep+" U to update")
	case r.base:
		station, label = line.Render(m.g.Done), r.tree.Name
	default:
		conn := m.g.Branch
		if last {
			conn = m.g.BranchEnd
		}
		station, label = line.Render(conn)+line.Bold(true).Render(m.g.Train), r.tree.Name
	}
	if selected {
		label = stBold.Render(label)
	}
	left := mark + name + station + " " + label
	status := m.treeStatus(r)
	if !r.repo.InManifest && first {
		status = stFaint.Render("dropped from manifest") + "  " + status
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(status) - 1
	if gap < 2 {
		return ansi.Truncate(left, w-lipgloss.Width(status)-3, "…") + "  " + status
	}
	return left + strings.Repeat(" ", gap) + status
}

func (m model) treeStatus(r row) string {
	if !r.repo.Present || r.repo.Error != "" {
		return ""
	}
	t := r.tree
	var parts []string
	switch {
	case !t.HasUpstream:
		parts = append(parts, stFaint.Render("no upstream"))
	case t.Ahead > 0 && t.Behind > 0:
		parts = append(parts, stWarn.Render(fmt.Sprintf("%s%d %s%d diverged", m.g.Up, t.Ahead, m.g.Down, t.Behind)))
	case t.Behind > 0:
		parts = append(parts, stWarn.Render(fmt.Sprintf("%s%d behind", m.g.Down, t.Behind)))
	case t.Ahead > 0:
		parts = append(parts, fmt.Sprintf("%s%d to push", m.g.Up, t.Ahead))
	default:
		parts = append(parts, stFaint.Render("up to date"))
	}
	if t.Dirty {
		parts = append(parts, stWarn.Render(m.g.Dirty+" uncommitted"))
	}
	return strings.Join(parts, stFaint.Render(" "+m.g.Sep+" "))
}

// detail is the right-hand preview: the selected tree, then recent activity.
func (m model) detail(w int) string {
	var l []string
	if m.sel < len(m.rows) {
		r := m.rows[m.sel]
		l = append(l, stBold.Render(r.project+" / "+r.repo.Name+" / "+r.tree.Name))
		kind := "work branch"
		if r.base {
			kind = "baseline · kept current by update"
		}
		l = append(l, stFaint.Render(kind), "")
		kv := [][2]string{
			{"path", r.tree.Path},
			{"branch", r.tree.Branch},
			{"upstream", map[bool]string{true: "origin/" + r.tree.Branch, false: "none"}[r.tree.HasUpstream]},
			{"status", m.treeStatus(r)},
		}
		for _, p := range kv {
			if p[1] == "" {
				continue
			}
			l = append(l, stFaint.Render(pad(p[0], 10))+ansi.Truncate(p[1], w-10, "…"))
		}
		if f := m.failuresFor(r); len(f) > 0 {
			l = append(l, "", stBold.Render("Last update"))
			for _, x := range f {
				l = append(l, " "+stBad.Render(m.g.Bad)+" "+ansi.Truncate(x, w-3, "…"))
			}
		}
	}
	l = append(l, "", stBold.Render("Recent")+stFaint.Render(fmt.Sprintf(" %s %d unread %s i opens the inbox", m.g.Sep, m.unread, m.g.Sep)))
	if len(m.recent) == 0 {
		l = append(l, lipgloss.Wrap(stFaint.Render("Nothing yet. Update results land here, then task, CI and review events in later phases."), w-2, " "))
	}
	for _, it := range m.recent[:min(len(m.recent), 8)] {
		l = append(l, m.inboxLine(it, w, false))
	}
	return strings.Join(l, "\n")
}

func (m model) failuresFor(r row) []string {
	if m.lastReport == nil {
		return nil
	}
	var out []string
	for _, res := range m.lastReport.Results {
		if res.Project != r.project || res.Repo != r.repo.Name {
			continue
		}
		for _, f := range res.Failures {
			out = append(out, f.Op+": "+f.Reason)
		}
		for _, s := range res.Skipped {
			out = append(out, "skipped "+s.Tree+": "+s.Reason)
		}
	}
	return out
}

func (m model) inboxLine(it notify.Item, w int, selected bool) string {
	title := it.Title
	if !it.Read {
		title = stBold.Render(title)
	}
	when := stFaint.Render(ago(it.Time))
	mark := "  "
	if selected {
		mark = stBold.Render(m.g.Selected) + " "
	}
	left := mark + sevGlyph(m.g, it.Severity) + " "
	room := w - lipgloss.Width(left) - lipgloss.Width(when) - 2
	return left + pad(ansi.Truncate(title, room, "…"), room) + "  " + when
}

func (m model) drawerView(w, h int) string {
	var l []string
	l = append(l, stBold.Render("Inbox")+stFaint.Render(fmt.Sprintf(" %s %d unread", m.g.Sep, m.unread)), "")
	if len(m.recent) == 0 {
		l = append(l, lipgloss.Wrap(stFaint.Render("Nothing yet. Update results, failed fetches and skipped pulls land here; task, CI and review events join them in later phases."), w-4, " "))
	}
	for i, it := range m.recent {
		if len(l) > h-6 {
			break
		}
		l = append(l, m.inboxLine(it, w-4, i == m.drawSel), "    "+stFaint.Render(it.Kind))
		if i == m.drawSel && it.Detail != "" {
			for _, d := range strings.Split(strings.TrimSpace(it.Detail), "\n")[:min(4, strings.Count(strings.TrimSpace(it.Detail), "\n")+1)] {
				l = append(l, "    "+ansi.Truncate(d, w-8, "…"))
			}
		}
	}
	l = append(l, "", keys("r", "mark read", "R", "all read", "esc", "close"))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#666666")).
		Padding(0, 1).Width(w).Height(h).Render(strings.Join(l, "\n"))
}

func helpView(w int, g glyphs) string {
	rows := [][2]string{
		{"j / k", "move between trees"},
		{"tab", "details for the selected tree (narrow terminals)"},
		{"U", "update now: manifests, fetch, fast-forward clean trees"},
		{"y", "copy the selected tree's path"},
		{"i", "inbox; r marks read, R marks all read"},
		{"esc", "close an overlay or dismiss failure toasts"},
		{"q", "quit (the zellij session keeps running)"},
	}
	l := []string{stBold.Render("Keys"), ""}
	for _, r := range rows {
		l = append(l, stRule.Render(g.Vertical)+" "+stBold.Render(pad(r[0], 7))+r[1])
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#666666")).
		Padding(0, 1).Width(w).Render(strings.Join(l, "\n"))
}

// --- helpers ---------------------------------------------------------------------

func sevStyle(s events.Severity) lipgloss.Style {
	switch s {
	case events.Success:
		return stOK
	case events.Warn:
		return stWarn
	case events.Error:
		return stBad
	default:
		return stFaint
	}
}

func sevGlyph(g glyphs, s events.Severity) string {
	switch s {
	case events.Success:
		return stOK.Render(g.OK)
	case events.Warn:
		return stWarn.Render(g.Warn)
	case events.Error:
		return stBad.Render(g.Bad)
	default:
		return stFaint.Render(g.Info)
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("2 Jan")
	}
}

// pad right-pads or truncates s to exactly n cells.
func pad(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) > n {
		return ansi.Truncate(s, n, "…")
	}
	return s + strings.Repeat(" ", n-lipgloss.Width(s))
}

// fit forces a block to exactly w×h cells.
func fit(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	out := make([]string, h)
	for i := range out {
		if i < len(lines) {
			out[i] = pad(lines[i], w)
		} else {
			out[i] = strings.Repeat(" ", w)
		}
	}
	return strings.Join(out, "\n")
}

// columns joins equal-height blocks side by side with a separator.
func columns(sep string, h int, blocks ...string) string {
	split := make([][]string, len(blocks))
	for i, b := range blocks {
		split[i] = strings.Split(b, "\n")
	}
	var sb strings.Builder
	for y := range h {
		for i := range blocks {
			if i > 0 {
				sb.WriteString(sep)
			}
			if y < len(split[i]) {
				sb.WriteString(split[i][y])
			}
		}
		if y < h-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
