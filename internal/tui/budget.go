package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/budget"
	"github.com/coredev-uk/ade/internal/ledger"
)

// The Budget page (docs/PLAN.md 6.5, 6.9): caps, profiles with their
// provider-side figures, spend by day, feature and model, the costliest
// items, recent budget stops and how agent review drafts were triaged.
//
// It paints from the ledger and cached billing snapshots at once (an offline
// build), then asks the providers in the background; the render path never
// calls a provider.

const (
	budgetWindow = 30 * 24 * time.Hour // by-feature, by-model, top items, stops, review rates
	budgetDays   = 7                   // the by-day series
	// nearCap is the share of a cap where the page starts saying "near cap".
	nearCap = 0.8
)

// budgetPhase says how a report was built.
type budgetPhase int

const (
	budgetOffline budgetPhase = iota // cached snapshots only
	budgetOnline                     // providers read when their snapshot is due
	budgetForced                     // providers read now (r)
)

// budgetState is the page's state; it lives in model.bud.
type budgetState struct {
	back page // page to return to on b / esc

	rep *budget.Report
	err error // the last build that failed; the previous report stays on screen
	// loading is an offline read in flight; refreshing a provider read.
	loading, refreshing bool
	// live is true once rep includes a provider read, false while it is
	// only cached snapshots.
	live bool
	gen  int // bumped per build: a slow answer for an older request is dropped

	sel     int // selected row (index into budRows)
	dscroll int // detail pane scroll

	// deps builds the data sources. Nil: budget.ForApp(app).
	deps func() (budget.Deps, error)
}

// budgetMsg carries a finished build to Update.
type budgetMsg struct {
	gen   int
	phase budgetPhase
	rep   budget.Report
	err   error
}

var errNoApp = errors.New("no app")

func (m model) budgetDeps() (budget.Deps, error) {
	if m.bud.deps != nil {
		return m.bud.deps()
	}
	if m.app == nil {
		return budget.Deps{}, errNoApp
	}
	return budget.ForApp(m.app)
}

// budgetBuild reads the report off the UI loop. The generation is bumped on
// the copy of the model Update returns, so callers keep the result.
func (m model) budgetBuild(phase budgetPhase) (model, tea.Cmd) {
	m.bud.gen++
	switch phase {
	case budgetOffline:
		m.bud.loading = true
	default:
		m.bud.refreshing = true
	}
	gen, ctx, deps := m.bud.gen, m.ctx, m.budgetDeps
	if ctx == nil {
		ctx = context.Background()
	}
	return m, func() tea.Msg {
		d, err := deps()
		if err != nil {
			return budgetMsg{gen: gen, phase: phase, err: err}
		}
		rep, err := budget.Build(ctx, d, budget.Options{
			Refresh: phase == budgetForced,
			Offline: phase == budgetOffline,
			Days:    budgetDays,
			Window:  budgetWindow,
		})
		return budgetMsg{gen: gen, phase: phase, rep: rep, err: err}
	}
}

// onBudget applies a finished build. An offline report is followed by the
// provider read, so the page is never blocked on the network.
func (m model) onBudget(msg budgetMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.bud.gen {
		return m, nil
	}
	m.bud.loading, m.bud.refreshing = false, false
	if msg.err != nil {
		m.bud.err = msg.err
		return m, nil
	}
	m.bud.err = nil
	rep := msg.rep
	// Biggest spender first; the ledger returns buckets in key order.
	byCost := func(a, b ledger.Bucket) int {
		switch {
		case a.CostUSD > b.CostUSD:
			return -1
		case a.CostUSD < b.CostUSD:
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	}
	rep.ByFeature = slices.SortedFunc(slices.Values(rep.ByFeature), byCost)
	rep.ByModel = slices.SortedFunc(slices.Values(rep.ByModel), byCost)
	m.bud.rep = &rep
	m.bud.live = msg.phase != budgetOffline
	m.bud.sel = clamp(m.bud.sel, 0, len(budRows(&rep))-1)
	if msg.phase == budgetOffline && m.page == pageBudget {
		next, cmd := m.budgetBuild(budgetOnline)
		return next, cmd
	}
	return m, nil
}

// isToolPage reports whether the Budget or Log page owns the screen (and no
// agile page or MR page is on top of it).
func (m model) isToolPage() bool {
	return (m.page == pageBudget || m.page == pageLog) && m.mr == nil && !m.isAgilePage()
}

// openBudget shows the Budget page and loads it.
func (m model) openBudget() (model, tea.Cmd) {
	if m.mr != nil {
		return m, nil // the MR page owns the screen
	}
	m.enterToolPage(pageBudget)
	return m.budgetBuild(budgetOffline)
}

// enterToolPage switches to the Budget or Log page, remembering the
// dashboard page to return to.
func (m *model) enterToolPage(p page) {
	back := m.page
	switch back {
	case pageBudget:
		back = m.bud.back
	case pageLog:
		back = m.lg.back
	}
	m.bud.back, m.lg.back = back, back
	m.page, m.showDetail, m.help = p, false, false
	m.ag.kind = agileNone
}

// leaveToolPage returns to the dashboard page the tool page was opened from.
func (m *model) leaveToolPage() {
	switch m.page {
	case pageBudget:
		m.page = m.bud.back
	case pageLog:
		m.page = m.lg.back
	}
	m.showDetail = false
}

// toolKey handles keys common to the Budget and Log pages: leaving, page
// switching, details. handled is false for the keys the global handler owns
// (quit, help, inbox, refresh).
func (m model) toolKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch k.String() {
	case "esc":
		if m.help {
			return m, nil, false
		}
		if m.nc.dismissToasts() {
			return m, nil, true
		}
		if m.showDetail {
			m.showDetail = false
			return m, nil, true
		}
		m.leaveToolPage()
		return m, nil, true
	case "t":
		// t keeps its meaning: flip Home and Worktrees, from the page this
		// one was opened over.
		m.leaveToolPage()
		m.page = 1 - m.page
		return m, nil, true
	case "tab":
		m.showDetail = !m.showDetail
		return m, nil, true
	case "b":
		if m.page == pageBudget {
			m.leaveToolPage()
			return m, nil, true
		}
		next, cmd := m.openBudget()
		return next, cmd, true
	case "L":
		if m.page == pageLog {
			m.leaveToolPage()
			return m, nil, true
		}
		next, cmd := m.openLog()
		return next, cmd, true
	}
	return m, nil, false
}

// budgetKey handles a key while the Budget page is showing.
func (m model) budgetKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if next, cmd, ok := m.toolKey(k); ok {
		return next, cmd, true
	}
	n := 0
	if m.bud.rep != nil {
		n = len(budRows(m.bud.rep))
	}
	prev := m.bud.sel
	switch k.String() {
	case "j", "down":
		m.bud.sel = clamp(m.bud.sel+1, 0, n-1)
	case "k", "up":
		m.bud.sel = clamp(m.bud.sel-1, 0, n-1)
	case "g", "home":
		m.bud.sel = 0
	case "G", "end":
		m.bud.sel = max(n-1, 0)
	case "pgdown", "space":
		m.bud.sel = clamp(m.bud.sel+max(1, m.h-8), 0, n-1)
	case "pgup":
		m.bud.sel = clamp(m.bud.sel-max(1, m.h-8), 0, n-1)
	case "J":
		if m.bud.rep != nil {
			m.bud.dscroll = clamp(m.bud.dscroll+1, 0, m.maxDetailScroll(len(m.budgetDetail(m.toolDetailWidth()))))
		}
	case "K":
		m.bud.dscroll = max(0, m.bud.dscroll-1)
	case "r":
		if m.bud.refreshing {
			return m, nil, true
		}
		next, cmd := m.budgetBuild(budgetForced)
		return next, cmd, true
	case "enter":
		if m.bud.rep == nil || n == 0 {
			return m, nil, true
		}
		r := budRows(m.bud.rep)[m.bud.sel]
		if r.kind == bkItem {
			cmd, _ := m.jumpToRef(m.bud.rep.TopItems[r.idx].Key)
			return m, cmd, true
		}
		m.showDetail = true // narrow: the row's details
	default:
		return m, nil, false
	}
	if m.bud.sel != prev {
		m.bud.dscroll = 0
	}
	return m, nil, true
}
