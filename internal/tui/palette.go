package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// `:` opens the command palette: every action Home has, by name, with its key
// and its CLI equivalent. `f` opens the same list restricted to filters. One
// component, two entry lists.

// palEntry is one named action.
type palEntry struct {
	name string // what you type to find it
	key  string // its key, shown faint
	cli  string // the CLI command that does the same, shown faint
	// needHome entries only apply on Home, to the selected row.
	needHome bool
	current  bool // the active choice of a picker: marked
	run      func(m model) (model, tea.Cmd)
}

// palette is the open list.
type palette struct {
	title  string // dialog title
	prompt string // ":" for the palette, "" for a picker
	all    []palEntry
	input  string
	sel    int
}

// visible lists the entries matching the typed text, best match first.
func (p *palette) visible() []palEntry {
	q := strings.TrimSpace(p.input)
	if q == "" {
		return p.all
	}
	type scored struct {
		e     palEntry
		score int
	}
	var hits []scored
	for _, e := range p.all {
		if !fuzzyFields(q, []string{e.name, e.cli}) {
			continue
		}
		if s, ok := fuzzyScore(q, e.name+" "+e.cli); ok {
			hits = append(hits, scored{e, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]palEntry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out
}

// press runs a key as if typed, for actions that are their key. It goes
// through Update so keys owned by overlays (agent menu `P`) work too.
func (m model) press(s string) (model, tea.Cmd) {
	next, cmd := m.Update(pressMsg(s))
	return next.(model), cmd
}

// pressMsg is the key press for a key name as help spells it.
func pressMsg(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func pressEntry(name, key, cli string, needHome bool) palEntry {
	return palEntry{name: name, key: key, cli: cli, needHome: needHome, run: func(m model) (model, tea.Cmd) { return m.press(key) }}
}

// paletteEntries is every action reachable by name. Pages and the actions
// with a CLI equivalent say which command it is.
func (m model) paletteEntries() []palEntry {
	page := func(name, cli string, open func(model) (model, tea.Cmd)) palEntry {
		return palEntry{name: name, cli: cli, run: open}
	}
	onHome := m.page == pageHome && m.mr == nil && !m.isAgilePage()
	es := []palEntry{
		{name: "Go to Home", run: func(m model) (model, tea.Cmd) {
			m.page, m.showDetail, m.ag.kind, m.mr = pageHome, false, agileNone, nil
			return m, nil
		}},
		{name: "Go to Worktrees", cli: "ade project status", run: func(m model) (model, tea.Cmd) {
			m.page, m.showDetail, m.ag.kind, m.mr = pageTrees, false, agileNone, nil
			return m, nil
		}},
		page("Standup", "ade standup", func(m model) (model, tea.Cmd) { return m.openStandup() }),
		page("Sprint review", "ade sprint review", func(m model) (model, tea.Cmd) { return m.openReview() }),
		page("Budget", "ade budget", func(m model) (model, tea.Cmd) { return m.openBudget() }),
		page("Audit log", "ade audit", func(m model) (model, tea.Cmd) { return m.openLog() }),
		{name: "Notifications", key: "i", cli: "ade inbox", run: func(m model) (model, tea.Cmd) {
			m.nc.open, m.nc.sel = true, 0
			return m, m.pollInbox()
		}},
		{name: "Refresh: git update and forge sync", key: "U", cli: "ade update && ade sync", run: func(m model) (model, tea.Cmd) {
			return m, m.refresh()
		}},
		{name: "Help: keys", key: "?", run: func(m model) (model, tea.Cmd) { m.help = true; return m, nil }},
		{name: "Quit", key: "q", run: func(m model) (model, tea.Cmd) { return m, tea.Quit }},
	}
	if m.mr != nil {
		return append(m.mrEntries(), es...)
	}
	if m.page == pageTrees && !m.isAgilePage() {
		trees := []palEntry{
			pressEntry("Open workspace", "w", "ade ws up", false),
			pressEntry("Open workspace with the other main pane (omp or editor)", "W", "ade ws up --layout agent", false),
			{name: "VS Code workspace", key: "v", cli: "ade vscode workspace <ref> --open", run: func(m model) (model, tea.Cmd) { return m.press("v") }},
			pressEntry("Copy the tree's path", "y", "", false),
		}
		return append(trees, es...)
	}
	if !onHome {
		return es
	}
	home := []palEntry{
		{name: "Search work items and open MRs", key: "/", needHome: true, run: func(m model) (model, tea.Cmd) { return m.openSearch() }},
		{name: "Clear search and filters", needHome: true, run: func(m model) (model, tea.Cmd) { return m.clearFilters(), nil }},
		{name: m.g.T("Filter…"), key: "f", needHome: true, run: func(m model) (model, tea.Cmd) { return m.openFilterPicker(), nil }},
		{name: "Cycle project filter", key: "p", needHome: true, run: func(m model) (model, tea.Cmd) { return m.cycleProject() }},
		{name: "Show or hide Activity", key: "e", needHome: true, run: func(m model) (model, tea.Cmd) { return m.toggleActivity(), nil }},
		{name: "Open the item's MR page", key: "enter", needHome: true, run: func(m model) (model, tea.Cmd) {
			cmd := m.openHomeRow()
			return m, cmd
		}},
		pressEntry("Start work", "s", "ade start", true),
		pressEntry("Create the MR for started work", "M", "ade mr create", true),
		pressEntry("Classify: suggest type and app labels", "c", "ade ai classify", true),
		pressEntry("Agents for this item", "a", "ade agent", true),
		pressEntry("Agents: every run", "A", "ade agent list", true),
		pressEntry("Open workspace", "w", "ade ws up", true),
		{name: "VS Code workspace for " + m.paletteSubject(), key: "v", cli: "ade vscode workspace <ref> --open", needHome: true, run: func(m model) (model, tea.Cmd) { return m.press("v") }},
		pressEntry("Open workspace with the other main pane (omp or editor)", "W", "ade ws up --layout agent", true),
		pressEntry("Restart blocked services", "r", "ade ws restart", true),
		pressEntry("Copy link", "y", "", true),
		pressEntry("Open in browser", "o", "", true),
		pressEntry("Notes", "n", "ade item <ref> --edit", true),
		pressEntry("Add a note", "N", "ade note <ref> <text> [--blocked]", true),
		pressEntry("Set weight", "=", "ade issue weight <ref> <n>", true),
		pressEntry("Move to the next sprint", ">", "ade issue move <ref>", true),
	}
	if m.aiOn() {
		home = append(home, pressEntry("Research the issue in the background", "R", "ade ai research <ref> --detach", true))
	}
	es = append(home, es...)
	// Named filters and project choices, directly.
	es = append(es, m.filterEntries()...)
	for _, p := range m.projectNames() {
		es = append(es, palEntry{name: "Project: " + p, needHome: true, run: func(m model) (model, tea.Cmd) { return m.setProject(p), nil }})
	}
	return es
}

// filterEntries lists the attention filters and every facet value on Home.
func (m model) filterEntries() []palEntry {
	var es []palEntry
	for f := hfAll; f <= hfDrift; f++ {
		es = append(es, palEntry{
			name: "Filter: " + f.String(), needHome: true, current: m.hs.facet == "" && m.homeFilter == f,
			run: func(m model) (model, tea.Cmd) { return m.setFilter(f, "", ""), nil },
		})
	}
	for _, facet := range []string{"type", "app", "workflow"} {
		for _, v := range m.facetChoices(facet) {
			es = append(es, palEntry{
				name: "Filter: " + facet + " " + v, needHome: true, current: m.hs.facet == facet && m.hs.facetVal == v,
				run: func(m model) (model, tea.Cmd) { return m.setFilter(hfAll, facet, v), nil },
			})
		}
	}
	return es
}

// facetChoices lists the values of a facet present on Home's items, sorted.
func (m model) facetChoices(facet string) []string {
	seen := map[string]bool{}
	for _, res := range m.results {
		for i := range res.Items {
			for _, v := range facetValues(&res.Items[i], facet) {
				seen[v] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func (m model) projectNames() []string {
	var out []string
	for _, r := range m.results {
		out = append(out, r.Project)
	}
	return out
}

// --- state changes -------------------------------------------------------------

func (m model) setFilter(f homeFilter, facet, value string) model {
	m.homeFilter, m.hs.facet, m.hs.facetVal = f, facet, value
	m.homeSel = 0
	m.rebuildHome("")
	return m
}

func (m model) setProject(p string) model {
	m.hs.project = p
	m.hs.search.fetched, m.hs.search.pool = false, nil // the pool depends on the projects searched
	m.homeSel = 0
	m.rebuildHome("")
	return m
}

func (m model) clearFilters() model {
	m = m.clearSearch()
	m.hs.project = ""
	return m.setFilter(hfAll, "", "")
}

// openFilterPicker is `f`: a small list of the attention filters and of every
// type, app and workflow value on Home.
func (m model) openFilterPicker() model {
	m.hs.pal = &palette{title: "Filter Home", all: m.filterEntries()}
	for i, e := range m.hs.pal.all {
		if e.current {
			m.hs.pal.sel = i
		}
	}
	return m
}

// openPalette is `:`.
func (m model) openPalette() model {
	m.hs.pal = &palette{title: "Command palette", prompt: ":", all: m.paletteEntries()}
	return m
}

// --- keys ----------------------------------------------------------------------

func (m model) paletteKey(k tea.KeyPressMsg) (model, tea.Cmd) {
	p := m.hs.pal
	vis := p.visible()
	switch ks := k.String(); ks {
	case "esc":
		m.hs.pal = nil
	case "enter":
		if p.sel >= len(vis) {
			return m, nil
		}
		e := vis[p.sel]
		m.hs.pal = nil
		return e.run(m)
	case "down", "ctrl+n", "tab":
		p.sel = clamp(p.sel+1, 0, len(vis)-1)
	case "up", "ctrl+p", "shift+tab":
		p.sel = clamp(p.sel-1, 0, len(vis)-1)
	case "backspace", "ctrl+h":
		if r := []rune(p.input); len(r) > 0 {
			p.input, p.sel = string(r[:len(r)-1]), 0
		}
	case "ctrl+u":
		p.input, p.sel = "", 0
	default:
		if k.Text != "" && !strings.ContainsAny(k.Text, "\r\n\t") {
			p.input += k.Text
			p.sel = 0
		}
	}
	return m, nil
}

// --- drawing -------------------------------------------------------------------

const paletteRows = 10

func (m model) paletteOverlay(w int) string {
	p := m.hs.pal
	if p == nil {
		return ""
	}
	dw := min(w, 100)
	inner := dw - 4
	vis := p.visible()
	var l []string
	prompt := p.prompt
	if prompt == "" {
		prompt = m.g.Prompt
	}
	l = append(l, stBold.Render(prompt)+" "+p.input+stBold.Render(m.g.Cursor), "")
	top := 0
	if p.sel >= paletteRows {
		top = p.sel - paletteRows + 1
	}
	nameW := 0
	for _, e := range vis {
		nameW = max(nameW, lipgloss.Width(e.name))
	}
	nameW = min(nameW, inner-4-16)
	for i := top; i < min(len(vis), top+paletteRows); i++ {
		e := vis[i]
		mark := "  "
		name := pad(m.g, e.name, nameW)
		if i == p.sel {
			mark, name = stBold.Render(m.g.Selected)+" ", stBold.Render(name)
		}
		line := mark + name
		if e.current {
			line += " " + stOK.Render(m.g.OK)
		}
		rest := inner - lipgloss.Width(line) - 1
		if hint := strings.TrimSpace(strings.Join([]string{e.key, e.cli}, "  ")); hint != "" && rest > 4 {
			line += "  " + stFaint.Render(ansi.Truncate(hint, rest-2, m.g.Ell))
		}
		l = append(l, line)
	}
	if len(vis) == 0 {
		l = append(l, stFaint.Render("Nothing is named like that."))
	}
	row := keys("up/down", "choose", "enter", "run", "esc", "close")
	if len(vis) > paletteRows {
		row += stFaint.Render(fmt.Sprintf("   %d of %d", p.sel+1, len(vis)))
	}
	l = append(l, "", row)
	return m.dialog(p.title, l, dw)
}

func (m model) paletteOverlayLayer(w, h int) *lipgloss.Layer {
	return blankFieldLayer(m.g, m.paletteOverlay(w), w, h)
}
