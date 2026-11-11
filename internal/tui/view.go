package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/events"
)

const (
	minW, minH  = 60, 16
	twoPaneMinW = 100
	drawerW     = 60
	wideMinW    = 140 // three regions: list | preview | activity and services
	railW       = 28
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
	if m.nc.open {
		dw := min(drawerW, w-4)
		if narrow {
			dw = w
		}
		d := m.nc.drawer(m.g, dw, h-2)
		layers = append(layers, lipgloss.NewLayer(fit(d, dw, h-2)).X(w-dw).Y(1).Z(1))
	}
	mrDialog := m.mr != nil && (m.mr.modal != nil || m.help)
	if ts := m.nc.toastStack(m.g, toastWidth(w)); ts != "" && !m.nc.open && !m.help && m.agt == nil && m.sw == nil && m.cl == nil && m.nt == nil && m.wt == nil && m.hs.pal == nil && !mrDialog { // a dialog owns the screen: toasts must not cover its keys
		layers = append(layers, lipgloss.NewLayer(ts).X(w-lipgloss.Width(ts)-1).Y(max(1, h-1-lipgloss.Height(ts))).Z(3))
	}
	switch {
	case m.help && m.mr != nil:
		layers = append(layers, blankFieldLayer(mrHelpView(min(w, 100), h, m.g, m.aiOn()), w, h))
	case m.help:
		hw := min(76, w-4)
		if narrow {
			hw = w
		}
		d := helpView(hw, h, m.g)
		top := max(0, (h-2-lipgloss.Height(d))/2)
		layers = append(layers, blankFieldLayer(strings.Repeat("\n", top)+d, w, h))
	}
	if l := blankFieldLayer(m.mrOverlay(w, h), w, h); l != nil {
		layers = append(layers, l)
	}
	if l := m.swOverlayLayer(w, h); l != nil {
		layers = append(layers, l)
	}
	if l := m.classifyOverlayLayer(w, h); l != nil {
		layers = append(layers, l)
	}
	if l := m.notesOverlayLayer(w, h); l != nil {
		layers = append(layers, l)
	}
	if l := m.weightOverlayLayer(w, h); l != nil {
		layers = append(layers, l)
	}
	if l := m.paletteOverlayLayer(w, h); l != nil {
		layers = append(layers, l)
	}
	if l := m.agentOverlayLayer(w, h); l != nil {
		layers = append(layers, l)
	}
	return lipgloss.NewCompositor(layers...).Render()
}

// --- header / footer -----------------------------------------------------------

func (m model) header(w int) string {
	// The header carries scope as a count and which page is showing; the body
	// names every project.
	repos := 0
	for _, p := range m.projects {
		repos += len(p.Repos)
	}
	left := stBold.Render("ADE")
	if m.page == pageHome {
		left += "  " + stBold.Render("Home") + stFaint.Render(" · worktrees")
	} else {
		left += "  " + stFaint.Render("home · ") + stBold.Render("Worktrees")
	}
	if m.mr != nil {
		left = stBold.Render("ADE") + "  " + stFaint.Render("← Home "+m.g.Sep+" ") + stBold.Render(m.mrCrumb())
	}
	switch m.page {
	case pageBudget:
		left = m.budgetHeaderLeft()
	case pageLog:
		left = m.logHeaderLeft()
	}
	if m.isAgilePage() && m.mr == nil {
		left = m.agileHeaderLeft()
	}
	if len(m.projects) > 0 {
		left += stFaint.Render(fmt.Sprintf("  %s %d %s %s %d %s", m.g.Sep, len(m.projects), plural(len(m.projects), "project"), m.g.Sep, repos, plural(repos, "repo")))
	}
	mid := m.activity()
	right := stFaint.Render("no unread")
	if m.nc.unread > 0 {
		right = stBold.Render(fmt.Sprintf("%s %d unread", m.g.Unread, m.nc.unread))
	}
	if chip := m.chip.render(m.g); chip != "" {
		right = chip + "  " + right
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(mid) - lipgloss.Width(right) - 2
	if gap < 4 {
		return " " + left + strings.Repeat(" ", max(1, w-lipgloss.Width(left)-lipgloss.Width(right)-2)) + right
	}
	return " " + left + strings.Repeat(" ", gap/2) + mid + strings.Repeat(" ", gap-gap/2) + right
}

// activity summarises the git update and forge sync loops in one phrase.
func (m model) activity() string {
	var parts []string
	switch {
	case m.updating && m.progress.Total > 0:
		parts = append(parts, fmt.Sprintf("updating %d/%d", m.progress.Done, m.progress.Total))
	case m.updating:
		parts = append(parts, "updating")
	}
	if m.syncing {
		parts = append(parts, "syncing")
	}
	if len(parts) > 0 {
		return m.g.Sync + " " + strings.Join(parts, " "+m.g.Sep+" ")
	}
	switch {
	case m.syncErr != nil:
		return stBad.Render(m.g.Bad + " sync failed " + m.g.Sep + " i for details")
	case m.lastReport != nil && m.lastReport.Severity() >= events.Warn:
		return sevGlyph(m.g, m.lastReport.Severity()) + " " + sevStyle(m.lastReport.Severity()).Render(m.lastReport.Title()) + stFaint.Render(" "+m.g.Sep+" "+ago(m.lastUpdate))
	case !m.syncedAt().IsZero():
		return stFaint.Render("synced " + ago(m.syncedAt()))
	case !m.lastUpdate.IsZero():
		return stFaint.Render("updated " + ago(m.lastUpdate))
	}
	return ""
}

func (m model) footer(w int) string {
	narrow := w < twoPaneMinW
	switch {
	case m.hs.pal != nil:
		return " " + keys("up/down", "choose", "enter", "run", "esc", "close")
	case m.agt != nil:
		return " " + keys("esc", "back/close", "ctrl+c", "quit") + stFaint.Render("   the agent dialog has its own keys")
	case m.nc.open:
		return " " + keys("j/k", "move", "enter", "go to", "f", "filter: "+m.nc.filter.String(), "r", "read", "R", "all read", "esc", "close")
	case m.mr != nil:
		return m.mrFooter(w)
	case m.help:
		return " " + keys("esc", "close")
	case m.isAgilePage():
		return m.agileFooter(w)
	case m.page == pageBudget:
		return m.budgetFooter(w)
	case m.page == pageLog:
		return m.logFooter(w)
	case m.page == pageHome && m.hs.search.typing:
		return " " + keys("enter", "keep filter", "up/down", "move", "esc", "clear search")
	case m.page == pageHome && narrow:
		return " " + keys("j/k", "move", "enter", "open", "/", "search", ":", "palette", "f", m.homeFilterName(), "tab", "details", "?", "keys")
	case m.page == pageHome:
		return " " + keys("j/k", "move", "enter", "open", "/", "search", ":", "palette", "f", "filter: "+m.homeFilterName(), "o", "browser", "w", "workspace", "U", "refresh", "?", "keys", "q", "quit")
	case narrow:
		return " " + keys("j/k", "move", "tab", "details", "U", "update", "t", "home", "i", "inbox", "?", "keys")
	}
	return " " + keys("j/k", "move", "w", "workspace", "U", "update now", "y", "copy path", "t", "home", "i", "notifications", "?", "keys", "q", "quit")
}

// syncedAt is when Home's data last came from the forge: this process's last
// sync, else the newest cached project.
func (m model) syncedAt() time.Time {
	t := m.lastSync
	for i := range m.results {
		if r := m.results[i].SyncedAt; r.After(t) {
			t = r
		}
	}
	return t
}

// toastWidth keeps pop-ups readable but out of the way: a third of a wide
// screen, the full width (minus a margin) on a narrow one.
func toastWidth(w int) int {
	if w < twoPaneMinW {
		return w - 2
	}
	return max(44, min(64, w/3))
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
	if m.mr != nil {
		return m.mrBody(w, h)
	}
	if m.isAgilePage() {
		return m.agileBody(w, h)
	}
	if m.page == pageBudget {
		return m.budgetBody(w, h)
	}
	if m.page == pageLog {
		return m.logBody(w, h)
	}
	if m.page == pageHome {
		return m.homeBody(w, h)
	}
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

func (m model) homeBody(w, h int) string {
	if !m.homeLoaded {
		return fit(" "+stFaint.Render("reading work items…"), w, h)
	}
	if len(m.results) == 0 {
		if m.syncing {
			return fit(" "+stFaint.Render(m.g.Sync+" first sync with your forges…"), w, h)
		}
		if len(m.app.Config.Projects) == 0 {
			return m.empty(w, h)
		}
		return fit(" "+stFaint.Render("No work items yet. U syncs now."), w, h)
	}
	if w < twoPaneMinW {
		if m.showDetail {
			return fit(m.homeDetail(w-2), w, h)
		}
		return m.homeLines(w, h)
	}
	sep := stRule.Render(" │ ")
	if m.threeRegion() {
		// list | preview | activity and services
		rest := w - railW - 2*3
		prevW := max(34, rest*30/100)
		listW := rest - prevW
		return columns(sep, h, m.homeLines(listW, h),
			fit(m.homeDetail(prevW-1), prevW, h), m.railView(railW, h))
	}
	listW := w * 66 / 100
	right := fit(m.homeDetail(w-listW-4), w-listW-3, h)
	return columns(sep, h, m.homeLines(listW, h), right)
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
	marks := m.nc.marks()
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
				out = append(out, m.treeLine(m.rows[idx], idx == m.sel, first, last, w, rowUnread(marks, m.rows[idx])))
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

// rowUnread counts unread notifications about a tree or its repo.
func rowUnread(marks map[string]int, r row) int {
	n := marks[markKey(r.project, r.repo.Name+"/"+r.tree.Name)]
	if r.base {
		n += marks[markKey(r.project, r.repo.Name)]
	}
	return n
}

func (m model) treeLine(r row, selected, first, last bool, w, unread int) string {
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
	if unread > 0 {
		left += " " + newMark(unread)
	}
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
			if p[0] == "path" {
				l = append(l, stFaint.Render(pad(p[0], 10))+truncLeft(p[1], w-10))
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
	l = append(l, "", stBold.Render("Recent")+stFaint.Render(fmt.Sprintf(" %s %d unread %s i opens notifications", m.g.Sep, m.nc.unread, m.g.Sep)))
	if len(m.nc.items) == 0 {
		l = append(l, lipgloss.Wrap(stFaint.Render("Nothing yet. Updates, CI results, review activity, task and agent results land here."), w-2, " "))
	}
	for _, it := range m.nc.items[:min(len(m.nc.items), 8)] {
		l = append(l, inboxLine(m.g, it, w, false))
	}
	return strings.Join(l, "\n")
}

// truncLeft shortens a path from the left so its tail (repo and tree) stays
// visible: "…/demo-gl/api/main". The cut snaps to a separator when one is
// left in the tail.
func truncLeft(s string, w int) string {
	if lipgloss.Width(s) <= w || w < 2 {
		return s
	}
	tail := ansi.TruncateLeft(s, lipgloss.Width(s)-(w-1), "")
	if i := strings.Index(tail, "/"); i > 0 {
		tail = tail[i:]
	}
	return "…" + tail
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

// helpGroup is one titled block of the Home help dialog.
type helpGroup struct {
	title string
	rows  [][2]string
}

// helpGroups lists every Home and Worktrees key. full gives each key its own
// row; the tight form pairs related keys so the dialog still fits a short
// terminal.
func helpGroups(full bool) []helpGroup {
	if !full {
		return []helpGroup{
			{"Move", [][2]string{
				{"j k g G", "move; top or bottom"},
				{"tab t p e", "details (narrow); Home or Worktrees; project; Activity"},
				{"/ : f", "search; palette; filter (triage, blocked, type, app...)"},
			}},
			{"Selected item", [][2]string{
				{"enter", "open: the item's MR page"},
				{"s M", "start work; create the MR for started work"},
				{"c", "classify: suggest missing type/app labels"},
				{"a A", "agent runs for the item; all runs"},
				{"w v", "open workspace (zellij); VS Code workspace of all repos"},
				{"y o", "copy link or path; browser"},
				{"n = r", "notes; set weight; restart blocked services"},
			}},
			{"Pages", [][2]string{
				{"S V", "standup; sprint review"},
				{"b L", "budget; audit log"},
				{"i U", "notifications; refresh now"},
			}},
			{"Leave", [][2]string{
				{"esc ? q", "dismiss; close this; quit"},
			}},
		}
	}
	return []helpGroup{
		{"Move", [][2]string{
			{"j / k", "move"},
			{"g / G", "jump to the top or bottom"},
			{"t", "switch Home (work items) and Worktrees"},
			{"/", "search: fuzzy over items and MRs, and the forge's open MRs; esc clears"},
			{":", "command palette: every action by name, with its key and CLI command"},
			{"f", "filter: waiting on you, triage, blocked, drift, or a type, app or workflow"},
			{"p", "project filter: cycles through the projects"},
			{"e", "show or hide the Activity section (a side rail at 140+ columns)"},
			{"tab", "details for the selection (narrow terminals)"},
		}},
		{"Selected item", [][2]string{
			{"enter", "open the item's MR page (the MR itself for a review request)"},
			{"s", "start work: child issue, branch, worktree, draft MR"},
			{"M", "create the MR for work already started"},
			{"c", "classify: suggest missing type/app labels (added on confirm)"},
			{"a", "agents: plan or address review for this item"},
			{"A", "agents: every run"},
			{"w", "open the workspace (needs the ade zellij session)"},
			{"v", "VS Code workspace: every repo of the project in one window, the item's worktrees first; written to the item's records dir, opened when code is on PATH"},
			{"W", "open it with the other main pane: omp (ADE implement skill) or the editor"},
			{"y", "copy the item's link (Home) or the tree's path (Worktrees)"},
			{"o", "open the item in the browser (plain host: shows the URL)"},
			{"n", "notes (yours; fed to agents and AI)"},
			{"=", "set weight (shown before it is written; audited)"},
			{"r", "restart blocked services of the item; otherwise refresh"},
		}},
		{"Pages", [][2]string{
			{"S", "standup: yesterday, today, blockers"},
			{"V", "sprint review: committed, done, carried, velocity"},
			{"b", "budget: caps, spend by day, feature and model"},
			{"L", "log: the audit trail"},
			{"i", "notifications; enter goes to the subject, r/R mark read"},
			{"U", "refresh now: git update and forge sync"},
		}},
		{"Leave", [][2]string{
			{"esc", "close an overlay or dismiss failure pop-ups"},
			{"?", "close this"},
			{"q", "quit (the zellij session keeps running)"},
		}},
	}
}

func helpView(w, h int, g glyphs) string {
	render := func(full bool) []string {
		inner := w - 4
		l := []string{stBold.Render("Keys")}
		for _, grp := range helpGroups(full) {
			l = append(l, section(grp.title, "", inner, g))
			for _, r := range grp.rows {
				desc := ansi.Wrap(r[1], max(10, inner-9), " ")
				for i, ln := range strings.Split(desc, "\n") {
					key := ""
					if i == 0 {
						key = r[0]
					}
					l = append(l, " "+stBold.Render(pad(key, 8))+ln)
				}
			}
		}
		return l
	}
	l := render(true)
	if len(l)+2 > h-2 { // the frame is two rows, and the footer and header stay visible
		l = render(false)
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
