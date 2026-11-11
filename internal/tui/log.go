package tui

import (
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/audit"
)

// The Log page (docs/PLAN.md 4.1): the audit log, newest first, filterable;
// enter jumps to the entry's target. The History tab on the MR page reads the
// same log through loadHistory and historyView (logview.go).

// logLimit bounds how many entries a load keeps (the newest ones).
const logLimit = 2000

var errNoAudit = errors.New("no audit log")

// logSince is how far back the page reads.
type logSince int

const (
	sinceHour logSince = iota
	sinceDay
	sinceWeek
	sinceAll
)

func (s logSince) String() string { return [...]string{"1h", "1d", "7d", "all"}[s] }

// cutoff is the oldest time the window includes; zero means no limit.
func (s logSince) cutoff(now time.Time) time.Time {
	switch s {
	case sinceHour:
		return now.Add(-time.Hour)
	case sinceDay:
		return now.Add(-24 * time.Hour)
	case sinceWeek:
		return now.Add(-7 * 24 * time.Hour)
	}
	return time.Time{}
}

// logGroup narrows the list to one family of actions.
type logGroup int

const (
	groupAll logGroup = iota
	groupForge
	groupAI
	groupReview
	groupBudget
	groupLocal
	groupErrors
	groupCount
)

func (g logGroup) String() string {
	return [...]string{"all", "forge.*", "ai.*", "review.*", "budget.*", "git.* project.*", "errors only"}[g]
}

func (g logGroup) keep(e audit.Entry) bool {
	switch g {
	case groupForge:
		return audit.GlobMatch("forge.*", e.Action)
	case groupAI:
		return audit.GlobMatch("ai.*", e.Action)
	case groupReview:
		return audit.GlobMatch("review.*", e.Action)
	case groupBudget:
		return audit.GlobMatch("budget.*", e.Action)
	case groupLocal:
		return audit.GlobMatch("git.*", e.Action) || audit.GlobMatch("project.*", e.Action)
	case groupErrors:
		return e.Outcome == "error" || e.Error != ""
	}
	return true
}

// matchText is the free-text filter: a case-insensitive substring over the
// action, target, item and error. q is already lower case.
func matchText(e audit.Entry, q string) bool {
	for _, s := range []string{e.Action, e.Target, e.Item, e.Error} {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

// logState is the page's state; it lives in model.lg.
type logState struct {
	back page // page to return to on L / esc

	log     *audit.Log    // nil: the app's
	entries []audit.Entry // newest first, for the current window
	full    bool          // the load hit logLimit: older entries exist
	err     error
	loaded  bool
	loading bool
	gen     int // bumped per load: a slow answer for an older request is dropped
	inited  bool

	since logSince
	group logGroup
	text  string // free-text filter
	// editing is the inline filter prompt; prev is restored by esc.
	editing bool
	prev    string

	vis     []int // indexes into entries that pass the filters
	sel     int   // index into vis
	dscroll int   // detail pane scroll
}

// logMsg carries a finished load to Update.
type logMsg struct {
	gen     int
	entries []audit.Entry
	full    bool
	err     error
}

// newestFirst orders entries by time, newest first; equal times keep the
// later-appended entry first.
func newestFirst(es []audit.Entry) {
	slices.Reverse(es)
	slices.SortStableFunc(es, func(a, b audit.Entry) int { return b.TS.Compare(a.TS) })
}

// loadLog reads the newest entries since the cutoff off the UI loop.
func loadLog(log *audit.Log, since time.Time, gen int) tea.Cmd {
	return func() tea.Msg {
		if log == nil {
			return logMsg{gen: gen, err: errNoAudit}
		}
		es, err := log.Query(audit.Filter{Since: since, Limit: logLimit})
		newestFirst(es)
		return logMsg{gen: gen, entries: es, full: len(es) >= logLimit, err: err}
	}
}

func (m model) auditLog() *audit.Log {
	if m.lg.log != nil {
		return m.lg.log
	}
	if m.app != nil {
		return m.app.Audit
	}
	return nil
}

// openLog shows the Log page and loads it.
func (m model) openLog() (model, tea.Cmd) {
	if m.mr != nil {
		return m, nil // the MR page owns the screen
	}
	m.enterToolPage(pageLog)
	if !m.lg.inited {
		m.lg.inited, m.lg.since = true, sinceDay
	}
	return m.logReload()
}

// logReload reads the window again. The generation is bumped on the copy of
// the model Update returns, so callers keep the result.
func (m model) logReload() (model, tea.Cmd) {
	m.lg.gen++
	m.lg.loading = true
	now := time.Now()
	return m, loadLog(m.auditLog(), m.lg.since.cutoff(now), m.lg.gen)
}

func (m model) onLog(msg logMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.lg.gen {
		return m, nil
	}
	m.lg.loading, m.lg.err, m.lg.loaded = false, msg.err, true
	if msg.err != nil {
		return m, nil
	}
	keep := m.lg.selectedID()
	m.lg.entries, m.lg.full = msg.entries, msg.full
	m.lg.refilter(keep)
	return m, nil
}

// selectedID is the ID of the selected entry, "" when there is none.
func (l *logState) selectedID() string {
	if e, ok := l.selected(); ok {
		return e.ID
	}
	return ""
}

func (l *logState) selected() (audit.Entry, bool) {
	if l.sel < 0 || l.sel >= len(l.vis) {
		return audit.Entry{}, false
	}
	return l.entries[l.vis[l.sel]], true
}

// refilter recomputes the visible entries and keeps the selection on the
// entry with ID keep when it is still listed.
func (l *logState) refilter(keep string) {
	q := strings.ToLower(strings.TrimSpace(l.text))
	l.vis = l.vis[:0]
	for i, e := range l.entries {
		if l.group.keep(e) && (q == "" || matchText(e, q)) {
			l.vis = append(l.vis, i)
		}
	}
	l.dscroll = 0
	for i, idx := range l.vis {
		if keep != "" && l.entries[idx].ID == keep {
			l.sel = i
			return
		}
	}
	l.sel = clamp(l.sel, 0, len(l.vis)-1)
}

// filtered reports whether any filter narrows the list.
func (l *logState) filtered() bool {
	return l.group != groupAll || strings.TrimSpace(l.text) != ""
}

// logKey handles a key while the Log page is showing.
func (m model) logKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	l := &m.lg
	ks := k.String()
	if l.editing {
		if ks == "ctrl+c" {
			return m, nil, false
		}
		keep := l.selectedID()
		switch {
		case ks == "enter":
			l.editing = false
		case ks == "esc":
			l.editing, l.text = false, l.prev
			l.refilter(keep)
		case ks == "backspace", ks == "ctrl+h":
			if _, n := utf8.DecodeLastRuneInString(l.text); n > 0 {
				l.text = l.text[:len(l.text)-n]
				l.refilter(keep)
			}
		case ks == "ctrl+u":
			l.text = ""
			l.refilter(keep)
		case k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0:
			l.text += k.Text
			l.refilter(keep)
		}
		return m, nil, true
	}
	if next, cmd, ok := m.toolKey(k); ok {
		return next, cmd, true
	}
	n := len(l.vis)
	switch ks {
	case "j", "down":
		l.sel, l.dscroll = clamp(l.sel+1, 0, n-1), 0
	case "k", "up":
		l.sel, l.dscroll = clamp(l.sel-1, 0, n-1), 0
	case "g", "home":
		l.sel, l.dscroll = 0, 0
	case "G", "end":
		l.sel, l.dscroll = max(n-1, 0), 0
	case "pgdown", "space":
		l.sel, l.dscroll = clamp(l.sel+max(1, m.h-8), 0, n-1), 0
	case "pgup":
		l.sel, l.dscroll = clamp(l.sel-max(1, m.h-8), 0, n-1), 0
	case "J":
		l.dscroll = clamp(l.dscroll+1, 0, m.maxDetailScroll(len(m.logDetail(m.toolDetailWidth()))))
	case "K":
		l.dscroll = max(0, l.dscroll-1)
	case "/":
		l.editing, l.prev = true, l.text
	case "a":
		keep := l.selectedID()
		l.group = (l.group + 1) % groupCount
		l.refilter(keep)
	case "s":
		l.since = (l.since + 1) % (sinceAll + 1)
		next, cmd := m.logReload()
		return next, cmd, true
	case "c":
		keep := l.selectedID()
		l.group, l.text = groupAll, ""
		l.refilter(keep)
	case "r":
		next, cmd := m.logReload()
		return next, cmd, true
	case "enter":
		e, ok := l.selected()
		if !ok {
			return m, nil, true
		}
		if ref, ok := m.entryRef(e); ok {
			cmd, _ := m.jumpToRef(ref)
			return m, cmd, true
		}
		m.showDetail = true // narrow: the entry in full
	default:
		return m, nil, false
	}
	return m, nil, true
}

// --- jumping -----------------------------------------------------------------------

// homeTarget is where a ref lives on Home.
type homeTarget struct {
	row    int
	openMR bool // the ref is an MR: open its page rather than just select the row
	all    bool // found only with the Home filter cleared
}

// homeTargetFor finds the Home row a ref belongs to: the work item itself,
// one of its child issues or MRs, or a review-requested MR. Audit refs may
// carry the plan's "work:" prefix.
func (m model) homeTargetFor(ref string) (homeTarget, bool) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "work:")
	if ref == "" {
		return homeTarget{}, false
	}
	find := func(rows []homeRow) (homeTarget, bool) {
		for i, r := range rows {
			if r.ref() == ref {
				return homeTarget{row: i, openMR: r.mr != nil}, true
			}
		}
		for i, r := range rows {
			if r.item == nil {
				continue
			}
			for _, mr := range r.item.MRs {
				if mr.Ref.String() == ref {
					return homeTarget{row: i, openMR: true}, true
				}
			}
			for _, c := range r.item.Children {
				if c.Ref.String() == ref {
					return homeTarget{row: i}, true
				}
			}
		}
		return homeTarget{}, false
	}
	if t, ok := find(m.homeRows); ok {
		return t, true
	}
	if m.homeFilter != hfAll {
		if t, ok := find(buildHomeRows(m.results, hfAll)); ok {
			t.all = true
			return t, true
		}
	}
	return homeTarget{}, false
}

// jumpToRef goes to a ref on Home: a work item or child issue selects its
// row; an MR opens its page. A Home filter that hides the row is cleared.
// ok is false when the ref is not on Home (the caller does nothing).
func (m *model) jumpToRef(ref string) (tea.Cmd, bool) {
	t, ok := m.homeTargetFor(ref)
	if !ok {
		return nil, false
	}
	if t.all {
		m.homeFilter = hfAll
		m.rebuildHome("")
		if t, ok = m.homeTargetFor(ref); !ok || t.all {
			return nil, false
		}
	}
	r := m.homeRows[t.row]
	m.selectSubject(r.project, r.ref())
	if !t.openMR {
		return nil, true
	}
	m.showDetail = false
	cmd := m.openHomeRow()
	if m.mr != nil {
		ref = strings.TrimPrefix(ref, "work:")
		for i, mr := range m.mr.mrs {
			if i > 0 && mr.Ref.String() == ref {
				return tea.Batch(cmd, m.mrSelect(i)), true
			}
		}
	}
	return cmd, true
}

// entryRef is the ref enter jumps to for an audit entry: its target, else
// its item, whichever is on Home.
func (m model) entryRef(e audit.Entry) (string, bool) {
	for _, ref := range []string{e.Target, e.Item} {
		if _, ok := m.homeTargetFor(ref); ok {
			return strings.TrimPrefix(strings.TrimSpace(ref), "work:"), true
		}
	}
	return "", false
}
