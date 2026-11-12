package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/agile"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/sprintmove"
	"github.com/coredev-uk/ade/internal/startwork"
)

// The sprint-move dialog (PLAN 3, "Moving to the next sprint"). `>` on a Home
// item, or on the selected carried item of the Sprint review page (`M` there
// takes every unfinished item of yours), moves tracker issues to the next
// sprint. ADE never moves silently: the dialog lists the exact writes (the
// iteration, the milestone, or the sprint label swap) and the child issues
// that follow, and enter makes them, audited as forge.issue.sprint. ADE only
// moves issues; the project lead closes them.

type smStage int

const (
	smResolving smStage = iota // reading the issues, their children and the sprints
	smConfirm                  // the writes are listed
	smApplying
	smDone
	smFailed
)

// smEntry is one tracker issue of the dialog: its target, where "next" is
// for it, and the plans (or the reason it cannot move).
type smEntry struct {
	tg    *sprintmove.Target
	plans []sprintmove.Plan
	err   error
}

type sprintDialog struct {
	refs    []forge.Ref
	heading string // what the dialog is about: the item's title, or the set
	stage   smStage

	entries []smEntry
	// children says the children that carry the issue's sprint follow it.
	children bool

	results  []sprintmove.Result
	warn     string
	err      error
	failedAt smStage
}

type (
	smResolvedMsg struct {
		d       *sprintDialog
		targets []*sprintmove.Target
		err     error
	}
	smAppliedMsg struct {
		d       *sprintDialog
		results []sprintmove.Result
		err     error
	}
)

// rebuild plans every entry again; it reads nothing.
func (d *sprintDialog) rebuild() {
	ch := sprintmove.ChildrenNone
	if d.children {
		ch = sprintmove.ChildrenSame
	}
	for i := range d.entries {
		e := &d.entries[i]
		e.plans, e.err = nil, nil
		next, err := e.tg.Next()
		if err != nil {
			e.err = err
			continue
		}
		e.plans, e.err = e.tg.Plans(next, ch)
	}
}

func (d *sprintDialog) hasChildren() bool {
	for _, e := range d.entries {
		if len(e.tg.Children) > 0 {
			return true
		}
	}
	return false
}

func (d *sprintDialog) anyWrite() bool {
	for _, e := range d.entries {
		if e.err == nil && sprintmove.AnyWrite(e.plans) {
			return true
		}
	}
	return false
}

// openSprintMove is Home's `>`: the selected item moves to the next sprint.
func (m model) openSprintMove() (model, tea.Cmd) {
	if m.page != pageHome || m.mr != nil || m.isAgilePage() || m.homeSel >= len(m.homeRows) || m.homeRows[m.homeSel].item == nil {
		return m, nil
	}
	it := m.homeRows[m.homeSel].item
	return m.openSprintMoveFor([]forge.Ref{it.Ref}, it.Issue.Title)
}

// openSprintMoveFor opens the dialog for tracker issues; nothing while
// another dialog is open.
func (m model) openSprintMoveFor(refs []forge.Ref, heading string) (model, tea.Cmd) {
	if len(refs) == 0 || m.sw != nil || m.cl != nil || m.agt != nil || m.nt != nil || m.wt != nil || m.sm != nil {
		return m, nil
	}
	d := &sprintDialog{refs: refs, heading: heading, stage: smResolving, children: true}
	m.sm = d
	ctx, a, open, now := m.ctx, m.app, m.writerOpen(), time.Now()
	if m.sync != nil && m.sync.Now != nil {
		now = m.sync.Now()
	}
	args := make([]string, len(refs))
	for i, r := range refs {
		args[i] = r.String()
	}
	return m, func() tea.Msg {
		tgs, err := sprintmove.ResolveAll(ctx, a, open, args, now)
		return smResolvedMsg{d: d, targets: tgs, err: err}
	}
}

// writerOpen opens forges for writing: through the sync service's opener when
// there is one (tests, fakes), else the configured drivers.
func (m model) writerOpen() startwork.OpenFunc {
	sync := m.sync
	return func(ctx context.Context, name string, f config.Forge) (startwork.Forge, error) {
		if sync == nil {
			return startwork.OpenForge(ctx, name, f)
		}
		fg, err := sync.Open(ctx, name, f)
		if err != nil {
			return nil, err
		}
		w, ok := fg.(startwork.Forge)
		if !ok {
			return nil, fmt.Errorf("forge %s cannot write issues", name)
		}
		return w, nil
	}
}

// smUpdate routes the dialog's messages and, while it is open, every key press
// but ctrl+c.
func (m model) smUpdate(msg tea.Msg) (model, tea.Cmd, bool) {
	d := m.sm
	switch msg := msg.(type) {
	case smResolvedMsg:
		if d == nil || msg.d != d {
			return m, nil, true
		}
		if msg.err != nil {
			d.stage, d.err, d.failedAt = smFailed, msg.err, smResolving
			m.auditSprintFailure(d.refs, msg.err)
			return m, nil, true
		}
		for _, t := range msg.targets {
			d.entries = append(d.entries, smEntry{tg: t})
		}
		d.rebuild()
		d.stage = smConfirm
		return m, nil, true
	case smAppliedMsg:
		if d == nil || msg.d != d {
			return m, nil, true
		}
		d.results = msg.results
		// Home shows each moved issue after one read of it, not a sync of every
		// project; a partly written move shows what was written.
		var cmds []tea.Cmd
		for _, r := range msg.results {
			if !r.Applied || r.Plan.Child {
				continue
			}
			for _, e := range d.entries {
				if e.tg.Issue.Ref == r.Plan.Ref {
					cmds = append(cmds, m.refreshItem(e.tg.Manifest, e.tg.Forge, r.Plan.Ref))
				}
			}
		}
		if msg.err != nil {
			d.stage, d.err, d.failedAt = smFailed, msg.err, smApplying
			return m, tea.Batch(cmds...), true
		}
		d.stage = smDone
		for _, r := range msg.results {
			if r.Warning != "" {
				d.warn = r.Warning
			}
		}
		// The review page loses the moved items.
		if m.ag.kind == agileReview {
			next, cmd := m.reload()
			m = next
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...), true
	case tea.KeyPressMsg:
		if d == nil || msg.String() == "ctrl+c" {
			return m, nil, false
		}
		next, cmd := m.smKey(msg.String())
		return next, cmd, true
	}
	return m, nil, false
}

func (m model) smKey(ks string) (model, tea.Cmd) {
	d := m.sm
	switch d.stage {
	case smConfirm:
		d.warn = ""
		switch ks {
		case "esc", "q":
			m.sm = nil
		case "c":
			if !d.hasChildren() {
				d.warn = "no item here has a child issue"
				break
			}
			d.children = !d.children
			d.rebuild()
		case "enter":
			return m.smApply(d)
		}
	case smDone, smFailed:
		if ks == "esc" || ks == "enter" || ks == "q" {
			m.sm = nil
		}
	default:
		if ks == "esc" {
			m.sm = nil
		}
	}
	return m, nil
}

func (m model) smApply(d *sprintDialog) (model, tea.Cmd) {
	if !d.anyWrite() {
		d.warn = "nothing to move: see above"
		return m, nil
	}
	d.stage = smApplying
	ctx, a := m.ctx, m.app
	entries := append([]smEntry(nil), d.entries...)
	return m, func() tea.Msg {
		var all []sprintmove.Result
		for _, e := range entries {
			if e.err != nil || !sprintmove.AnyWrite(e.plans) {
				continue
			}
			res, err := sprintmove.Apply(ctx, e.tg.Forge, a, e.tg.Project, "", e.plans)
			all = append(all, res...)
			if err != nil {
				return smAppliedMsg{d: d, results: all, err: err}
			}
		}
		return smAppliedMsg{d: d, results: all}
	}
}

// auditSprintFailure records a move that failed before any write, so its raw
// error lands in the log rather than on screen.
func (m model) auditSprintFailure(refs []forge.Ref, err error) {
	if m.app == nil || m.app.Audit == nil {
		return
	}
	for _, r := range refs {
		_ = m.app.Audit.Append(audit.Entry{
			Action: sprintmove.Action, Target: r.String(), Item: r.String(), Outcome: "error", Error: err.Error(),
			Params: map[string]any{"stage": "read"},
		})
	}
}

// --- Sprint review page ------------------------------------------------------

// carriedItems are the review's unfinished items, in page order.
func (m model) carriedItems() []agile.ReviewItem {
	if m.ag.review == nil {
		return nil
	}
	var out []agile.ReviewItem
	for _, it := range m.ag.review.Items {
		if !it.Done {
			out = append(out, it)
		}
	}
	return out
}

// selectedCarried is the carried item `>` moves: the one picked with J/K, else
// the first.
func (m model) selectedCarried() (agile.ReviewItem, bool) {
	items := m.carriedItems()
	if len(items) == 0 {
		return agile.ReviewItem{}, false
	}
	for _, it := range items {
		if it.Ref.String() == m.ag.selRef {
			return it, true
		}
	}
	return items[0], true
}

// stepCarried moves the selection among the carried items and scrolls to it.
func (m *model) stepCarried(delta int) {
	items := m.carriedItems()
	if len(items) == 0 {
		return
	}
	cur, _ := m.selectedCarried()
	i := 0
	for j, it := range items {
		if it.Ref == cur.Ref {
			i = j
		}
	}
	i = max(0, min(len(items)-1, i+delta))
	m.ag.selRef = items[i].Ref.String()
	short := refShort(m.ag.selRef)
	for at, ln := range m.reviewLines(m.w) {
		if strings.Contains(ln, short) && strings.Contains(ansi.Strip(ln), "carried") {
			bodyH := max(1, m.h-4)
			switch {
			case at < m.ag.scroll:
				m.ag.scroll = at
			case at >= m.ag.scroll+bodyH:
				m.ag.scroll = at - bodyH + 1
			}
			return
		}
	}
}

// openReviewMove is the review page's `>` (the selected carried item) and `M`
// (every unfinished item assigned to you).
func (m model) openReviewMove(all bool) (model, tea.Cmd) {
	if m.ag.review == nil {
		return m, nil
	}
	if !all {
		it, ok := m.selectedCarried()
		if !ok {
			return m, nil
		}
		return m.openSprintMoveFor([]forge.Ref{it.Ref}, it.Title)
	}
	var refs []forge.Ref
	for _, it := range m.carriedItems() {
		if it.Assigned {
			refs = append(refs, it.Ref)
		}
	}
	return m.openSprintMoveFor(refs, countWord(len(refs), "unfinished item")+" of yours in "+m.ag.review.Sprint.Title)
}

// --- view --------------------------------------------------------------------

func (m model) sprintOverlay(w, h int) string {
	d := m.sm
	if d == nil {
		return ""
	}
	dw := min(w, 100) // the shared dialog measure; blankFieldLayer centres it
	inner := dw - 4
	g := m.g
	title := "Move to the next sprint"
	if len(d.refs) == 1 {
		title += m.g.T(" · ") + d.refs[0].String()
	}
	l := []string{stFaint.Render(ansi.Truncate(d.heading, inner, m.g.Ell)), ""}
	var hint string
	switch d.stage {
	case smResolving:
		l = append(l, stFaint.Render(g.Sync+" reading "+countWord(len(d.refs), "item")+m.g.T(" and the sprints…")))
		hint = keys("esc", "cancel")
	case smConfirm:
		l = append(l, m.sprintConfirmView(d, inner, h)...)
		hint = keys("enter", "move", "esc", "cancel")
		switch {
		case !d.anyWrite():
			hint = keys("esc", "close")
		case d.hasChildren():
			hint = keys("enter", "move", "c", "children", "esc", "cancel")
		}
	case smApplying:
		l = append(l, stFaint.Render(g.Sync+m.g.T(" writing the sprint…")))
		hint = keys("esc", "hide")
	case smDone:
		for _, r := range d.results {
			l = append(l, m.sprintResultLine(r, inner)...)
		}
		l = append(l, "", stFaint.Render(ansi.Wrap(g.Info+" ADE moves issues only: the project lead closes them.", inner, "")))
		hint = keys("enter", "close")
	case smFailed:
		cause, fix, _ := strings.Cut(sprintFailure(d), m.g.T(" · "))
		cause, fix = m.g.T(cause), m.g.T(fix)
		l = append(l, stBad.Render(ansi.Wrap(g.Bad+" "+cause, inner, "")))
		if fix != "" {
			l = append(l, stFaint.Render(ansi.Wrap("  "+fix, inner, "")))
		}
		for _, r := range d.results {
			l = append(l, m.sprintResultLine(r, inner)...)
		}
		hint = keys("esc", "close")
	}
	if d.warn != "" {
		l = append(l, "", stWarn.Render(ansi.Wrap(g.Warn+" "+d.warn, inner, "")))
	}
	l = append(l, "", hint)
	return m.dialog(title, l, dw)
}

// sprintConfirmView lists the writes: per issue the move, then the children
// that follow or stay behind. A long list is cut to what fits the screen.
func (m model) sprintConfirmView(d *sprintDialog, inner, h int) []string {
	g := m.g
	dest := ""
	for _, e := range d.entries {
		if e.err == nil && len(e.plans) > 0 {
			dest = e.plans[0].To.Title + " " + g.Sep + " " + e.plans[0].Mode
			break
		}
	}
	l := []string{section("Moves", dest, inner, g)}
	var body []string
	for _, e := range d.entries {
		if e.err != nil {
			for i, ln := range wrap(e.tg.Issue.Ref.String()+": "+e.err.Error(), inner-4) {
				pre := stBad.Render(g.Bad) + " "
				if i > 0 {
					pre = "  "
				}
				body = append(body, "  "+pre+ln)
			}
			continue
		}
		for _, p := range e.plans {
			for _, ln := range wrap(m.g.T(moveLine(p)), inner-2) {
				body = append(body, "  "+ln)
			}
			for _, n := range p.Notes {
				body = append(body, stFaint.Render(ansi.Wrap("    note: "+n, inner, "")))
			}
		}
	}
	if room := max(6, h-14); len(body) > room {
		more := len(body) - (room - 1)
		body = append(body[:room-1], stFaint.Render(fmt.Sprintf(m.g.T("  … and %d more lines"), more)))
	}
	l = append(l, body...)
	if d.hasChildren() && d.anyWrite() {
		line := m.g.T("child issues that carry the same sprint follow · c leaves them alone")
		if !d.children {
			line = m.g.T("child issues are left alone · c lets them follow")
		}
		l = append(l, stFaint.Render(ansi.Truncate("  "+line, inner, m.g.Ell)))
	}
	return l
}

// moveLine is a plan as one confirm row. The rule above the rows names the
// destination and how it is stored, so the row does not repeat the storage
// mode (a label move still says which labels change).
func moveLine(p sprintmove.Plan) string {
	if p.Unchanged || p.Mode == sprintmove.ModeLabel {
		return p.Describe()
	}
	who := p.Ref.String()
	if p.Child {
		who += " (child)"
	}
	return fmt.Sprintf("%s: %s → %s", who, sprintmove.SprintName(p.From), p.To.Title)
}

func (m model) sprintResultLine(r sprintmove.Result, inner int) []string {
	g := m.g
	mark, st := g.OK+" done", stOK
	switch {
	case r.Plan.Unchanged:
		mark, st = g.Info+" unchanged", stFaint
	case !r.Applied:
		mark, st = g.Bad+" failed", stBad
	}
	var out []string
	for i, ln := range wrap(m.g.T(r.Plan.Describe()), inner-2-lipgloss.Width(mark)) {
		if i == 0 {
			out = append(out, st.Render(mark)+" "+ln)
		} else {
			out = append(out, strings.Repeat(" ", lipgloss.Width(mark)+1)+ln)
		}
	}
	return out
}

// sprintFailure words a failed dialog as "cause · what to do". The raw error
// is in the audit log, never on screen.
func sprintFailure(d *sprintDialog) string {
	if d.failedAt == smResolving {
		return "could not read the issues or sprints from the forge · check the connection and press > again"
	}
	return "the move was not fully written · what was written is listed below; press > to try again, or move it on the forge"
}

func (m model) sprintOverlayLayer(w, h int) *lipgloss.Layer {
	return blankFieldLayer(m.g, m.sprintOverlay(w, h), w, h)
}
