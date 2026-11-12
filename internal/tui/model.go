package tui

import (
	"context"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/review"
	"github.com/coredev-uk/ade/internal/store"
)

type page int

const (
	pageHome   page = iota // work items from the forge
	pageTrees              // projects → repos → worktrees
	pageBudget             // AI spend, caps and profiles (budget.go)
	pageLog                // the audit log (log.go)
)

// row is one selectable line on the Worktrees page.
type row struct {
	project string
	repo    project.RepoStatus
	tree    project.TreeStatus
	base    bool // the repo's baseline (default-branch) tree
}

type model struct {
	ctx   context.Context
	app   *app.App
	svc   *project.Service
	sync  *forgesync.Service
	cache *store.Store
	g     glyphs
	w, h  int
	page  page

	// Home
	results    []forgesync.Result
	homeRows   []homeRow
	homeSel    int
	homeFilter homeFilter
	hs         homeState // search, palette, facet and project filters, Activity, load average (homeactions.go)
	homeLoaded bool      // cache read: before it, "nothing assigned" would be a lie
	syncing    bool
	syncQueued bool // a sync waits for the running update to finish (PLAN 2.3: git first, forge second)
	lastSync   time.Time
	syncErr    error

	// Worktrees
	projects []project.ProjectStatus
	rows     []row
	sel      int
	loadErr  error
	loaded   bool

	// showDetail swaps the list for the selection's details on narrow
	// terminals, where the two-pane layout does not fit.
	showDetail bool

	updating   bool
	progress   project.Progress
	lastUpdate time.Time
	lastReport *project.Report

	nc   *notifCenter
	help bool

	// MR page (mrpage*.go): mr is the open page, nil on the dashboard;
	// mrCache keeps each MR's data for the session; reviews stores drafts.
	mr      *mrPage
	mrCache map[string]*mrData
	reviews *review.Store
	be      *mrBackends
	dial    func(ctx context.Context, ref forge.Ref, forgeKey string) (review.Backend, error) // nil: the configured forge

	// ag is the Standup / Sprint review pages' state (agile.go).
	ag agileUI

	// bud and lg are the Budget and Log pages' state (budget.go, log.go).
	bud budgetState
	lg  logState

	// sw is the open Start work dialog, nil when closed (startwork.go).
	sw *startDialog
	// cl is the open Classify dialog (classify.go); chip is the header's AI
	// spend figure (aichip.go).
	cl   *classifyDialog
	chip aiChip

	// nt and wt are the open Notes and Weight dialogs, nil when closed
	// (notes.go, weight.go).
	nt *notesDialog
	wt *weightDialog
	// sm is the open sprint-move dialog, nil when closed (sprintmove.go).
	sm *sprintDialog

	// agt is the open agent dialog, nil when closed (agent.go).
	agt *agentDialog

	// ws is the running-instances view behind Home's Services strip (workspace.go).
	ws wsState

	// fw signals changes of the cache and the audit log (watch.go); nil: none.
	fw *fileWatch
}

// --- messages ----------------------------------------------------------------

type (
	statusMsg struct {
		projects []project.ProjectStatus
		err      error
	}
	resultsMsg struct {
		results []forgesync.Result
		err     error
		synced  bool // from a sync run, not the cache
	}
	progressMsg   project.Progress
	updateDoneMsg struct {
		report project.Report
		err    error
	}
	eventMsg       events.Event
	tickMsg        time.Time
	updateTickMsg  time.Time
	startUpdateMsg struct{}
)

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.loadStatus(), m.loadResults(), m.nc.poll(), tick(), m.loadAIChip(), m.watchStart()}
	if m.app.Config.UpdateOnStart() {
		cmds = append(cmds, func() tea.Msg { return startUpdateMsg{} })
	}
	if iv := m.app.Config.Update.Interval.Duration; iv > 0 {
		cmds = append(cmds, tea.Tick(iv, func(t time.Time) tea.Msg { return updateTickMsg(t) }))
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) loadStatus() tea.Cmd {
	return func() tea.Msg {
		ps, err := m.svc.Status(m.ctx, "")
		return statusMsg{ps, err}
	}
}

// loadResults reads the last sync from the cache: Home renders from it at
// once, before any network call.
func (m model) loadResults() tea.Cmd {
	return func() tea.Msg {
		rs, err := forgesync.Load(m.cache)
		return resultsMsg{results: rs, err: err}
	}
}

func (m model) runSync() tea.Cmd {
	return func() tea.Msg {
		rs, err := m.sync.Sync(m.ctx)
		return resultsMsg{results: rs, err: err, synced: true}
	}
}

func (m model) runUpdate() tea.Cmd {
	return func() tea.Msg {
		r, err := m.svc.Update(m.ctx)
		return updateDoneMsg{r, err}
	}
}

// refresh runs the update (git) and then the forge sync, in that order (PLAN
// 2.3 step 6): the sync starts when the update reports done, so what it
// writes (item records, branches seen) is read against fetched git state. A
// loop already running is not started twice; a sync requested while an update
// runs is queued behind it, and one requested while only a sync runs starts
// its update alongside.
func (m *model) refresh() tea.Cmd {
	var cmds []tea.Cmd
	if !m.updating {
		m.updating, m.progress = true, project.Progress{}
		cmds = append(cmds, m.runUpdate())
	}
	if m.updating && !m.syncing && !m.syncQueued && m.sync != nil {
		m.syncQueued = true
	}
	return tea.Batch(cmds...)
}

// startQueuedSync is called when an update finishes: it starts the sync the
// update was holding back.
func (m *model) startQueuedSync() tea.Cmd {
	if !m.syncQueued {
		return nil
	}
	m.syncQueued = false
	if m.syncing || m.sync == nil {
		return nil
	}
	m.syncing = true
	return m.runSync()
}

// pollInbox re-reads the inbox: rows from other ade processes pop up.
func (m model) pollInbox() tea.Cmd { return m.nc.poll() }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if next, cmd, ok := m.startUpdate(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.classifyUpdate(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.notesUpdate(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.weightUpdate(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.smUpdate(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.agentUpdate(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.homeUpdate(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.watchUpdate(msg); ok {
		return next, cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case statusMsg:
		m.projects, m.loadErr, m.loaded = msg.projects, msg.err, true
		m.rows = flatten(msg.projects)
		m.sel = min(m.sel, max(len(m.rows)-1, 0))
	case resultsMsg:
		if msg.synced {
			m.syncing, m.syncErr = false, msg.err
			m.lastSync = time.Now()
		}
		if msg.err == nil || len(msg.results) > 0 {
			m.setResults(msg.results)
		}
		m.homeLoaded = true
		vel := m.loadVelocity(!msg.synced)
		return m, tea.Batch(m.pollInbox(), vel)
	case startUpdateMsg:
		return m, m.refresh()
	case updateTickMsg:
		next := tea.Tick(m.app.Config.Update.Interval.Duration, func(t time.Time) tea.Msg { return updateTickMsg(t) })
		return m, tea.Batch(m.refresh(), next)
	case progressMsg:
		m.progress = project.Progress(msg)
	case updateDoneMsg:
		m.updating, m.lastUpdate = false, time.Now()
		if msg.err == nil {
			r := msg.report
			m.lastReport = &r
		}
		sync := m.startQueuedSync() // before m is returned: it sets m.syncing
		return m, tea.Batch(m.loadStatus(), m.pollInbox(), sync)
	case eventMsg:
		m.nc.onEvent(events.Event(msg))
		return m, m.pollInbox()
	case inboxMsg:
		m.nc.onInbox(msg)
	case mrResultMsg:
		return m, m.onMRResult(msg)
	case historyMsg:
		m.onHistory(msg)
	case mrSpinMsg:
		return m, m.onSpin()
	case tickMsg:
		m.nc.tick(time.Time(msg))
		cmds := []tea.Cmd{tick()} // toast expiry, ages; the cache and the AI figure come from watch.go
		if time.Time(msg).Second()%wsPollEach == 0 {
			cmds = append(cmds, m.wsPoll())
		}
		return m, tea.Batch(cmds...)
	case wsOpenedMsg:
		return m, m.onWsOpened(msg)
	case codeWsMsg:
		m.onCodeWs(msg)
		return m, nil
	case wsChipsMsg:
		m.ws.chips = msg.chips
	case aiChipMsg:
		m.chip = aiChip(msg)
	case agileMsg:
		return m.agileUpdate(msg)
	case budgetMsg:
		return m.onBudget(msg)
	case logMsg:
		return m.onLog(msg)
	case tea.PasteMsg:
		m.paste(msg)
		return m, nil
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

// setResults replaces Home's data and keeps the selection on the same row
// (by ref) when it still exists.
func (m *model) setResults(rs []forgesync.Result) {
	keep := ""
	if m.homeSel < len(m.homeRows) {
		keep = m.homeRows[m.homeSel].ref()
	}
	// Projects with work come first; one that failed and has nothing cached
	// sinks to the bottom so its error doesn't push work off screen.
	m.results = append([]forgesync.Result(nil), rs...)
	sort.SliceStable(m.results, func(i, j int) bool {
		empty := func(r forgesync.Result) bool { return r.Err != "" && len(r.Items) == 0 }
		return !empty(m.results[i]) && empty(m.results[j])
	})
	m.rebuildHome(keep)
}

func (m *model) rebuildHome(keep string) {
	m.homeRows = buildRows(m.results, m.homeQuery())
	m.homeSel = min(m.homeSel, max(len(m.homeRows)-1, 0))
	for i, r := range m.homeRows {
		if r.ref() == keep {
			m.homeSel = i
			break
		}
	}
}

// selectSubject moves the selection to the row an inbox item is about, on
// whichever page shows it.
func (m *model) selectSubject(project, subject string) {
	for i, r := range m.homeRows {
		if r.project == project && r.ref() == subject {
			m.page, m.homeSel, m.showDetail = pageHome, i, true
			return
		}
	}
	for i, r := range m.rows {
		if r.project == project && (subject == r.repo.Name || subject == r.repo.Name+"/"+r.tree.Name) {
			m.page, m.sel, m.showDetail = pageTrees, i, true
			return
		}
	}
}

func (m model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	ks := k.String()
	if m.nc.open && ks != "ctrl+c" {
		cmd, jump := m.nc.key(ks)
		if jump != nil {
			m.selectSubject(jump.Project, jump.Subject)
			m.mr = nil // the jump leaves the MR page
			m.ag.kind = agileNone
		}
		return m, cmd
	}
	if m.mr != nil {
		if cmd, handled := m.mrKey(k); handled {
			return m, cmd
		}
	}
	if m.isAgilePage() {
		if next, cmd, handled := m.agileKey(k); handled {
			return next, cmd
		}
	}
	if m.isToolPage() {
		var next tea.Model
		var cmd tea.Cmd
		var handled bool
		if m.page == pageBudget {
			next, cmd, handled = m.budgetKey(k)
		} else {
			next, cmd, handled = m.logKey(k)
		}
		if handled {
			return next, cmd
		}
	}
	switch ks {
	case "ctrl+c", "q":
		if m.help {
			m.help = false
			return m, nil
		}
		return m, tea.Quit
	case "esc":
		switch {
		case m.help:
			m.help = false
		case m.nc.dismissToasts():
		case m.onHomePage() && m.searching():
			return m.clearSearch(), nil
		case m.showDetail:
			m.showDetail = false
		}
	case "tab":
		m.showDetail = !m.showDetail
	case "?":
		m.help = !m.help
	case "i":
		m.nc.open, m.nc.sel = true, 0
		return m, m.pollInbox()
	case "S":
		next, cmd := m.openStandup()
		return next, cmd
	case "V":
		next, cmd := m.openReview()
		return next, cmd
	case "b":
		next, cmd := m.openBudget()
		return next, cmd
	case "L":
		next, cmd := m.openLog()
		return next, cmd
	case "s":
		next, cmd := m.openStart(false)
		return next, cmd
	case "w":
		next, cmd := m.openWorkspace(false)
		return next, cmd
	case "v":
		next, cmd := m.openCodeWorkspace()
		return next, cmd
	case "W":
		next, cmd := m.openWorkspace(true)
		return next, cmd
	case "M":
		next, cmd := m.openStart(true)
		return next, cmd
	case "c":
		next, cmd := m.openClassify()
		return next, cmd
	case "a", "A":
		if m.page == pageHome && m.mr == nil && !m.isAgilePage() {
			var it *domain.WorkItem
			if m.homeSel < len(m.homeRows) {
				it = m.homeRows[m.homeSel].item
			}
			if ks == "a" && it == nil {
				return m, nil // `a` needs an item; `A` lists every run
			}
			next, cmd := m.openAgent(it, ks == "A")
			return next, cmd
		}
	case "enter":
		if m.page == pageHome {
			return m, m.openHomeRow()
		}
	case "t":
		m.page = 1 - m.page
	case "U":
		return m, m.refresh()
	case "f":
		if m.onHomePage() {
			return m.openFilterPicker(), nil
		}
	case "/":
		next, cmd := m.openSearch()
		return next, cmd
	case ":":
		return m.openPalette(), nil
	case "r":
		next, cmd := m.restartOrRefresh()
		return next, cmd
	case "n":
		next, cmd := m.openNotes()
		return next, cmd
	case "=":
		next, cmd := m.openWeight()
		return next, cmd
	case ">":
		next, cmd := m.openSprintMove()
		return next, cmd
	case "o":
		next, cmd := m.openBrowser()
		return next, cmd
	case "p":
		if m.onHomePage() {
			next, cmd := m.cycleProject()
			return next, cmd
		}
	case "e":
		if m.onHomePage() {
			return m.toggleActivity(), nil
		}
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "g", "home":
		m.move(-1 << 30)
	case "G", "end":
		m.move(1 << 30)
	case "y":
		return m, m.copySelection()
	}
	return m, nil
}

func (m *model) move(d int) {
	if m.page == pageHome {
		m.homeSel = clamp(m.homeSel+d, 0, len(m.homeRows)-1)
		return
	}
	m.sel = clamp(m.sel+d, 0, len(m.rows)-1)
}

func clamp(v, lo, hi int) int { return max(lo, min(v, max(hi, lo))) }

// copySelection copies the selected item's web URL (Home) or tree path
// (Worktrees) with OSC 52, which works in VS Code and browser terminals.
func (m *model) copySelection() tea.Cmd {
	var what, text string
	switch {
	case m.page == pageHome && m.homeSel < len(m.homeRows):
		r := m.homeRows[m.homeSel]
		what = r.ref()
		if r.item != nil {
			text = r.item.Issue.WebURL
		} else {
			text = r.mr.WebURL
		}
	case m.page == pageTrees && m.sel < len(m.rows):
		what, text = "path", m.rows[m.sel].tree.Path
	}
	if text == "" {
		return nil
	}
	e := events.New("ui.copied", events.Info, "copied "+what+": "+text)
	e.Transient = true
	m.nc.push(e)
	return tea.SetClipboard(text)
}

func flatten(ps []project.ProjectStatus) []row {
	var rows []row
	for _, p := range ps {
		for _, r := range p.Repos {
			if r.Baseline != nil {
				rows = append(rows, row{project: p.Name, repo: r, tree: *r.Baseline, base: true})
			}
			for _, t := range r.Worktrees {
				rows = append(rows, row{project: p.Name, repo: r, tree: t})
			}
			if r.Baseline == nil && len(r.Worktrees) == 0 {
				rows = append(rows, row{project: p.Name, repo: r})
			}
		}
	}
	return rows
}
