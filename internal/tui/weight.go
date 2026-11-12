package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/weight"
)

// The weight dialog (PLAN 3, "Weights"). `=` on a Home item sets or clears the
// issue's weight. ADE never re-weights silently: the dialog shows the exact
// writes (the weight field on GitLab Premium, else the weight label swapped
// with the old one removed) before enter makes them, audited as
// forge.issue.weight. The suggested value is the median of similar closed
// issues (the same one classify shows); it only fills the field.

type wtStage int

const (
	wtResolving wtStage = iota // reading the issue, its capabilities and its children
	wtInput                    // choosing the value; the writes are previewed
	wtApplying
	wtDone
	wtFailed
)

type weightDialog struct {
	ref   forge.Ref
	title string
	stage wtStage

	tg *weight.Target
	// input is the digits typed; none says "clear it" instead.
	input string
	none  bool
	// children says the child issues follow (when there are any).
	children bool
	suggest  *int
	why      string

	results  []weight.Result
	warn     string
	err      error
	failedAt wtStage
}

type (
	wtResolvedMsg struct {
		d       *weightDialog
		tg      *weight.Target
		suggest *int
		why     string
		err     error
	}
	wtAppliedMsg struct {
		d       *weightDialog
		results []weight.Result
		err     error
	}
)

// openWeight is Home's `=`: the selected item's weight. It does nothing
// without an item row, or while another dialog is open.
func (m model) openWeight() (model, tea.Cmd) {
	if m.page != pageHome || m.mr != nil || m.isAgilePage() || m.homeSel >= len(m.homeRows) || m.homeRows[m.homeSel].item == nil {
		return m, nil
	}
	return m.openWeightFor(m.homeRows[m.homeSel].item.Ref, m.homeRows[m.homeSel].item.Issue.Title, nil, "")
}

// openWeightFor opens the dialog for one tracker issue (also from classify's
// `=`, which has just shown the suggestion: shown and why carry it over, for
// the issue is not labelled yet and the dialog could not derive it alone).
func (m model) openWeightFor(ref forge.Ref, title string, shown *int, shownWhy string) (model, tea.Cmd) {
	if m.sw != nil || m.cl != nil || m.agt != nil || m.nt != nil || m.wt != nil {
		return m, nil
	}
	d := &weightDialog{ref: ref, title: title, stage: wtResolving, children: true}
	m.wt = d
	ctx, a, sync := m.ctx, m.app, m.sync
	var items []domain.WorkItem
	for _, r := range m.results {
		items = append(items, r.Items...)
	}
	samples := ai.WeightSamples(items)
	return m, func() tea.Msg {
		open := func(ctx context.Context, name string, f config.Forge) (startwork.Forge, error) {
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
		tg, err := weight.Resolve(ctx, a, open, ref.String())
		if err != nil {
			return wtResolvedMsg{d: d, err: err}
		}
		// The synced items cover the sprint window; the history beyond it
		// comes from the cache, never from a wait on the forge.
		samples = ai.MergeSamples(samples, weight.HistorySamples(ctx, a, tg.Forge, tg.Manifest.Forge, tg.Manifest))
		w, why := ai.SuggestWeight(tg.Issue, tg.Manifest.Labels, tg.Manifest.Tracker.Weight, ai.Suggestion{}, samples)
		if w == nil && shown != nil {
			w, why = shown, shownWhy
		}
		return wtResolvedMsg{d: d, tg: tg, suggest: w, why: why}
	}
}

// weightUpdate routes the dialog's messages and, while it is open, every key
// press but ctrl+c.
func (m model) weightUpdate(msg tea.Msg) (model, tea.Cmd, bool) {
	d := m.wt
	switch msg := msg.(type) {
	case wtResolvedMsg:
		if d == nil || msg.d != d {
			return m, nil, true
		}
		if msg.err != nil {
			d.stage, d.err, d.failedAt = wtFailed, msg.err, wtResolving
			m.auditWeightFailure(d.ref, msg.err)
			return m, nil, true
		}
		d.tg, d.suggest, d.why, d.stage = msg.tg, msg.suggest, msg.why, wtInput
		d.children = len(d.tg.Children) > 0
		if d.suggest != nil {
			d.input = strconv.Itoa(*d.suggest)
		}
		return m, nil, true
	case wtAppliedMsg:
		if d == nil || msg.d != d {
			return m, nil, true
		}
		d.results = msg.results
		if msg.err != nil {
			d.stage, d.err, d.failedAt = wtFailed, msg.err, wtApplying
			return m, nil, true
		}
		d.stage = wtDone
		// Home's row shows the new weight after reading this one item again
		// (and its children, whose weights changed with it).
		return m, m.refreshItem(d.tg.Manifest, d.tg.Forge, d.ref), true
	case tea.KeyPressMsg:
		if d == nil || msg.String() == "ctrl+c" {
			return m, nil, false
		}
		next, cmd := m.weightKey(msg.String())
		return next, cmd, true
	}
	return m, nil, false
}

func (m model) weightKey(ks string) (model, tea.Cmd) {
	d := m.wt
	switch d.stage {
	case wtInput:
		d.warn = ""
		switch ks {
		case "esc", "q":
			m.wt = nil
		case "backspace", "ctrl+h":
			d.input, d.none = d.input[:max(len(d.input)-1, 0)], false
		case "n", "x":
			d.input, d.none = "", true
		case "s":
			if d.suggest == nil {
				d.warn = "nothing to suggest: no closed issue of this type and app shares a title word"
				break
			}
			d.input, d.none = strconv.Itoa(*d.suggest), false
		case "up", "+", "=":
			d.step(1)
		case "down", "-":
			d.step(-1)
		case "c":
			if len(d.tg.Children) == 0 {
				d.warn = "this item has no child issue"
				break
			}
			d.children = !d.children
		case "enter":
			return m.weightApply(d)
		default:
			if len(ks) == 1 && ks[0] >= '0' && ks[0] <= '9' && len(d.input) < 3 {
				d.input, d.none = d.input+ks, false
			}
		}
	case wtDone, wtFailed:
		if ks == "esc" || ks == "enter" || ks == "q" {
			m.wt = nil
		}
	default:
		if ks == "esc" {
			m.wt = nil
		}
	}
	return m, nil
}

// step moves the typed value by delta, starting from the current weight; it
// never goes below 1 (use n to clear).
func (d *weightDialog) step(delta int) {
	n, err := strconv.Atoi(d.input)
	if err != nil {
		n = 0
		if cur := domain.ResolveWeight(d.tg.Issue, d.tg.Manifest.Tracker.Weight); cur != nil && !d.none {
			n = *cur
		}
		if delta > 0 {
			n = max(n, 0)
		}
	}
	d.input, d.none = strconv.Itoa(max(n+delta, 1)), false
}

// value is the weight the dialog would write: nil clears it.
func (d *weightDialog) value() (*int, error) {
	if d.none {
		return nil, nil
	}
	n, err := strconv.Atoi(d.input)
	if err != nil || n < 1 {
		return nil, fmt.Errorf("type a weight of 1 or more, or n to clear it")
	}
	return &n, nil
}

func (d *weightDialog) plans() ([]weight.Plan, error) {
	to, err := d.value()
	if err != nil {
		return nil, err
	}
	return d.tg.Plans(to, d.children)
}

func (m model) weightApply(d *weightDialog) (model, tea.Cmd) {
	plans, err := d.plans()
	if err != nil {
		d.warn = err.Error()
		return m, nil
	}
	if !slicesAnyWrite(plans) {
		d.warn = "already " + weight.Value(plans[0].To) + ": nothing to write"
		return m, nil
	}
	d.stage = wtApplying
	ctx, a, tg := m.ctx, m.app, d.tg
	return m, func() tea.Msg {
		res, err := weight.Apply(ctx, tg.Forge, a, tg.Project, "", plans)
		return wtAppliedMsg{d: d, results: res, err: err}
	}
}

func slicesAnyWrite(plans []weight.Plan) bool {
	for _, p := range plans {
		if !p.Unchanged {
			return true
		}
	}
	return false
}

// auditWeightFailure records a weight attempt that failed before any write,
// so its raw error lands in the log rather than on screen.
func (m model) auditWeightFailure(ref forge.Ref, err error) {
	if m.app == nil || m.app.Audit == nil {
		return
	}
	_ = m.app.Audit.Append(audit.Entry{
		Action: "forge.issue.weight", Target: ref.String(), Item: ref.String(), Outcome: "error", Error: err.Error(),
		Params: map[string]any{"stage": "read"},
	})
}

// --- view --------------------------------------------------------------------

func (m model) weightOverlay(w, h int) string {
	d := m.wt
	if d == nil {
		return ""
	}
	dw := min(w, 100) // the shared dialog measure; blankFieldLayer centres it
	inner := dw - 4
	g := m.g
	l := []string{stFaint.Render(ansi.Truncate(d.title, inner, m.g.Ell)), ""}
	var hint string
	switch d.stage {
	case wtResolving:
		l = append(l, stFaint.Render(g.Sync+" reading "+d.ref.String()+m.g.Ell))
		hint = keys("esc", "cancel")
	case wtInput:
		l = append(l, m.weightInputView(d, inner)...)
		hint = keys("0-9", "type", "n", "none", "+/-", "adjust", "enter", "set", "esc", "close")
		if d.suggest != nil {
			hint = keys("0-9", "type", "s", "suggested", "n", "none", "enter", "set", "esc", "close")
		}
	case wtApplying:
		l = append(l, stFaint.Render(g.Sync+m.g.T(" writing the weight…")))
		hint = keys("esc", "hide")
	case wtDone:
		for _, r := range d.results {
			l = append(l, m.weightResultLine(r, inner)...)
		}
		hint = keys("enter", "close")
	case wtFailed:
		cause, fix, _ := strings.Cut(weightFailure(d), m.g.T(" · "))
		cause, fix = m.g.T(cause), m.g.T(fix)
		l = append(l, stBad.Render(ansi.Wrap(g.Bad+" "+cause, inner, "")))
		if fix != "" {
			l = append(l, stFaint.Render(ansi.Wrap("  "+fix, inner, "")))
		}
		for _, r := range d.results {
			l = append(l, m.weightResultLine(r, inner)...)
		}
		hint = keys("esc", "close")
	}
	if d.warn != "" {
		l = append(l, "", stWarn.Render(ansi.Wrap(g.Warn+" "+d.warn, inner, "")))
	}
	l = append(l, "", hint)
	return m.dialog(m.g.T("Weight · ")+d.ref.String(), l, dw)
}

func (m model) weightResultLine(r weight.Result, inner int) []string {
	g := m.g
	mark, st := g.OK+" done", stOK
	switch {
	case r.Plan.Unchanged:
		mark, st = g.Info+" unchanged", stFaint
	case !r.Applied:
		mark, st = g.Bad+" failed", stBad
	}
	out := []string{}
	for i, ln := range wrap(m.g.T(r.Plan.Describe()), inner-2-lipgloss.Width(mark)) {
		if i == 0 {
			out = append(out, st.Render(mark)+" "+ln)
		} else {
			out = append(out, strings.Repeat(" ", lipgloss.Width(mark)+1)+ln)
		}
	}
	return out
}

func (m model) weightInputView(d *weightDialog, inner int) []string {
	g := m.g
	cur := domain.ResolveWeight(d.tg.Issue, d.tg.Manifest.Tracker.Weight)
	now := "unweighted"
	if cur != nil {
		now = fmt.Sprintf("%dw", *cur)
	}
	l := []string{section("Weight", "now "+now, inner, g)}
	field := d.input + "_"
	if d.none {
		field = "none"
	}
	l = append(l, "  new weight  "+stBold.Render("[ "+field+" ]"))
	switch {
	case d.suggest != nil:
		l = append(l, "", stFaint.Render(ansi.Wrap(fmt.Sprintf(m.g.T("%s suggested %d · %s"), g.Info, *d.suggest, d.why), inner, "")))
	case d.why != "":
		l = append(l, "", stFaint.Render(ansi.Wrap(g.Info+" "+d.why, inner, "")))
	}
	plans, err := d.plans()
	mode := ""
	if err == nil {
		mode = "weight field"
		if plans[0].Mode == weight.ModeLabel {
			mode = "label " + d.tg.Manifest.Tracker.Weight.Pattern
			if d.tg.Manifest.Tracker.Weight.Pattern == "" {
				mode = "label weight::{value}"
			}
		}
	}
	l = append(l, "", section("Writes", mode, inner, g))
	if err != nil {
		l = append(l, stFaint.Render(ansi.Wrap("  "+err.Error(), inner, "")))
		return l
	}
	for _, p := range plans {
		for _, ln := range wrap(m.g.T(p.Describe()), inner-2) {
			l = append(l, "  "+ln)
		}
		for _, n := range p.Notes {
			l = append(l, stFaint.Render(ansi.Wrap("    note: "+n, inner, "")))
		}
	}
	if len(d.tg.Children) > 0 {
		names := make([]string, len(d.tg.Children))
		for i, c := range d.tg.Children {
			names[i] = c.Issue.Ref.String()
		}
		line := fmt.Sprintf(m.g.T("child %s follows the weight · c leaves it alone"), strings.Join(names, ", "))
		if !d.children {
			line = fmt.Sprintf(m.g.T("child %s is left alone · c lets it follow"), strings.Join(names, ", "))
		}
		l = append(l, stFaint.Render(ansi.Truncate("  "+line, inner, m.g.Ell)))
	}
	return l
}

// weightFailure words a failed dialog as "cause · what to do". A failed read
// shows its error, for the fix depends on it.
func weightFailure(d *weightDialog) string {
	if d.failedAt == wtResolving {
		return "could not read the issue: " + oneLine(d.err.Error()) + " · check the connection and press = again"
	}
	return "the weight was not fully written · what was written is listed below; press = to try again, or set it on the forge"
}

func (m model) weightOverlayLayer(w, h int) *lipgloss.Layer {
	return blankFieldLayer(m.g, m.weightOverlay(w, h), w, h)
}
