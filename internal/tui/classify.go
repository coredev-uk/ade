package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/startwork"
)

// The classify dialog (PLAN 6.1/§3). `c` on a Home item suggests the type and
// app labels the issue lacks: from keywords when AI is off (no cost), from the
// model otherwise. The suggestion is only a proposal: labels are added when
// you confirm, and only the ones you leave ticked. It never removes a label.

type clStage int

const (
	clResolving clStage = iota // reading the issue and its manifest
	clThinking                 // suggesting
	clAsk                      // the AI call needs a yes (pre-flight)
	clSuggest                  // proposal on screen
	clApplying
	clDone
	clFailed
)

type clLabel struct {
	name string
	on   bool
}

type classifyDialog struct {
	ref   forge.Ref
	stage clStage

	res *startwork.Resolved
	in  ai.ClassifyInput
	cr  ai.ClassifyResult
	pf  *ai.Preflight // what the AI call would cost, for clAsk

	labels []clLabel
	cur    int
	added  []string
	warn   string
	err    error
	// failedAt is the stage that failed: it words the cause, since the raw
	// error is for the audit log, not for the dialog.
	failedAt clStage
}

type (
	clResolvedMsg struct {
		d   *classifyDialog
		res *startwork.Resolved
		in  ai.ClassifyInput
		err error
	}
	clSuggestedMsg struct {
		d   *classifyDialog
		cr  ai.ClassifyResult
		pf  *ai.Preflight // set with a needs-confirmation answer
		err error
	}
	clAppliedMsg struct {
		d   *classifyDialog
		err error
	}
)

// openClassify is Home's `c`.
func (m model) openClassify() (model, tea.Cmd) {
	if m.page != pageHome || m.mr != nil || m.isAgilePage() || m.sw != nil || m.homeSel >= len(m.homeRows) || m.homeRows[m.homeSel].item == nil {
		return m, nil
	}
	it := m.homeRows[m.homeSel].item
	d := &classifyDialog{ref: it.Ref, stage: clResolving}
	m.cl = d
	ctx, a, sync, results := m.ctx, m.app, m.sync, m.results
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
		res, err := startwork.Resolve(ctx, a, open, d.ref.String())
		if err != nil {
			return clResolvedMsg{d: d, err: err}
		}
		in := ai.ClassifyInput{Issue: res.Item.Issue, Labels: res.Manifest.Labels, Weight: res.Manifest.Tracker.Weight}
		for _, r := range res.Manifest.Repos {
			in.KnownApps = append(in.KnownApps, r.App...)
		}
		seen := map[string]bool{}
		var items []domain.WorkItem
		for _, r := range results {
			items = append(items, r.Items...)
			for _, wi := range r.Items {
				if wi.Type != "" && !seen[wi.Type] {
					seen[wi.Type] = true
					in.KnownTypes = append(in.KnownTypes, wi.Type)
				}
			}
		}
		in.Samples = ai.WeightSamples(items)
		if hist, err := ai.ClosedSamples(ctx, res.Forge, res.Manifest.Tracker.Project, res.Manifest.Labels, res.Manifest.Tracker.Weight, time.Now()); err == nil {
			in.Samples = ai.MergeSamples(in.Samples, hist)
		}
		return clResolvedMsg{d: d, res: res, in: in}
	}
}

// classifyCmd runs the suggestion off the UI loop. assume answers a pending
// pre-flight prompt.
func (m model) classifyCmd(d *classifyDialog, assume bool) tea.Cmd {
	ctx, a := m.ctx, m.app
	forgeName := d.res.Manifest.Forge
	in := d.in
	return func() tea.Msg {
		svc, err := ai.NewService(a)
		if err != nil {
			return clSuggestedMsg{d: d, err: err}
		}
		cr, err := svc.Classify(ctx, forgeName, in, ai.ClassifyOpts{Assume: assume})
		var nc *ai.ErrNeedsConfirm
		if errors.As(err, &nc) {
			return clSuggestedMsg{d: d, pf: &nc.Preflight}
		}
		return clSuggestedMsg{d: d, cr: cr, err: err}
	}
}

// classifyUpdate routes the dialog's messages and, while it is open, every
// key press but ctrl+c.
func (m model) classifyUpdate(msg tea.Msg) (model, tea.Cmd, bool) {
	d := m.cl
	switch msg := msg.(type) {
	case clResolvedMsg:
		if d == nil || msg.d != d {
			return m, nil, true
		}
		if msg.err != nil {
			d.stage, d.err, d.failedAt = clFailed, msg.err, clResolving
			m.auditClassifyFailure(d.ref, msg.err)
			return m, nil, true
		}
		d.res, d.in, d.stage = msg.res, msg.in, clThinking
		return m, m.classifyCmd(d, false), true
	case clSuggestedMsg:
		if d == nil || msg.d != d {
			return m, nil, true
		}
		switch {
		case msg.pf != nil:
			d.stage, d.pf = clAsk, msg.pf
		case msg.err != nil:
			d.stage, d.err, d.failedAt = clFailed, msg.err, clThinking
		default:
			d.cr, d.stage = msg.cr, clSuggest
			for _, l := range msg.cr.Suggestion.Labels {
				d.labels = append(d.labels, clLabel{name: l, on: true})
			}
		}
		return m, nil, true
	case clAppliedMsg:
		if d == nil || msg.d != d {
			return m, nil, true
		}
		if msg.err != nil {
			d.stage, d.err, d.failedAt = clFailed, msg.err, clApplying
			return m, nil, true
		}
		d.stage = clDone
		// Home's row: the issue left "needs triage". Only the forge is read
		// again; the git update (clones, pulls) has nothing to do with labels.
		var cmd tea.Cmd
		if m.sync != nil && !m.syncing {
			m.syncing = true
			cmd = m.runSync()
		}
		return m, cmd, true
	case tea.KeyPressMsg:
		if d == nil || msg.String() == "ctrl+c" {
			return m, nil, false
		}
		next, cmd := m.classifyKey(msg.String())
		return next, cmd, true
	}
	return m, nil, false
}

func (m model) classifyKey(ks string) (model, tea.Cmd) {
	d := m.cl
	switch d.stage {
	case clSuggest:
		switch ks {
		case "esc", "q":
			m.cl = nil
		case "j", "down":
			d.cur = min(d.cur+1, max(len(d.labels)-1, 0))
		case "k", "up":
			d.cur = max(d.cur-1, 0)
		case "space", " ", "x":
			if d.cur >= 0 && d.cur < len(d.labels) {
				d.labels[d.cur].on = !d.labels[d.cur].on
			}
		case "=":
			if d.cr.Suggestion.Weight == nil {
				d.warn = "no weight to suggest: press = on Home to set one by hand"
				return m, nil
			}
			ref, title := d.ref, d.res.Item.Issue.Title
			m.cl = nil
			return m.openWeightFor(ref, title)
		case "enter", "y":
			var add []string
			for _, l := range d.labels {
				if l.on {
					add = append(add, l.name)
				}
			}
			if len(add) == 0 {
				d.warn = "nothing ticked: space ticks a label, esc closes"
				if len(d.labels) == 0 {
					d.warn = "no label to add · esc closes"
				}
				return m, nil
			}
			d.warn, d.added, d.stage = "", add, clApplying
			return m, m.classifyApply(d, add)
		}
	case clAsk:
		switch ks {
		case "y", "enter":
			d.stage, d.pf = clThinking, nil
			return m, m.classifyCmd(d, true)
		case "esc", "q", "n":
			m.cl = nil
		}
	case clDone, clFailed:
		if ks == "esc" || ks == "enter" || ks == "q" {
			m.cl = nil
		}
	default:
		if ks == "esc" {
			m.cl = nil
		}
	}
	return m, nil
}

func (m model) classifyApply(d *classifyDialog, add []string) tea.Cmd {
	ctx, a := m.ctx, m.app
	w, ref, source := d.res.Forge, d.ref, d.cr.Suggestion.Source
	return func() tea.Msg {
		return clAppliedMsg{d: d, err: ai.ApplyLabels(ctx, w, a.Audit, ref, add, source)}
	}
}

// --- view --------------------------------------------------------------------

func (m model) classifyOverlay(w, h int) string {
	d := m.cl
	if d == nil {
		return ""
	}
	dw := min(w, 100) // the shared dialog measure; blankFieldLayer centres it
	inner := dw - 4
	g := m.g
	l := []string{}
	if d.res != nil {
		l = append(l, stFaint.Render(ansi.Truncate(d.res.Item.Issue.Title, inner, "…")), "")
	}
	var hint string
	switch d.stage {
	case clResolving:
		l = append(l, stFaint.Render(g.Sync+" reading "+d.ref.String()+"…"))
		hint = keys("esc", "cancel")
	case clThinking:
		l = append(l, stFaint.Render(g.Sync+" "+m.classifyWhat()+"…"))
		hint = keys("esc", "cancel")
	case clAsk:
		l = append(l, stWarn.Render(g.Warn+" this asks the model first"))
		for _, ln := range packFields(strings.Split(d.pf.Describe(), " · "), g.sep(), "  ", inner) {
			l = append(l, stFaint.Render(ln))
		}
		for _, c := range d.pf.Confirm {
			for i, ln := range wrap(c, inner-2) {
				if i == 0 {
					ln = g.Warn + " " + ln
				} else {
					ln = "  " + ln
				}
				l = append(l, stWarn.Render(ln))
			}
		}
		hint = keys("y", "send", "esc", "cancel")
	case clSuggest:
		l = append(l, m.classifySuggestView(d, inner)...)
		hint = keys("space", "tick", "enter", "add labels", "esc", "close")
		if len(d.labels) == 0 {
			hint = keys("esc", "close")
		}
		if d.cr.Suggestion.Weight != nil {
			hint += "   " + keys("=", "set weight")
		}
	case clApplying:
		l = append(l, stFaint.Render(g.Sync+" adding "+strings.Join(d.added, ", ")+"…"))
		hint = keys("esc", "hide")
	case clDone:
		l = append(l, stOK.Render(g.OK+" added ")+strings.Join(d.added, ", "))
		hint = keys("enter", "close")
	case clFailed:
		if errors.Is(d.err, ai.ErrNothingToClassify) {
			l = append(l, stOK.Render(g.OK+" "+d.err.Error()), stFaint.Render("nothing to suggest, and no model call was made"))
		} else {
			cause, fix, _ := strings.Cut(classifyFailure(d), " · ")
			l = append(l, stBad.Render(ansi.Wrap(g.Bad+" "+cause, inner, "")))
			if fix != "" {
				l = append(l, stFaint.Render(ansi.Wrap("  "+fix, inner, "")))
			}
		}
		hint = keys("esc", "close")
	}
	if d.warn != "" {
		l = append(l, "", stWarn.Render(ansi.Wrap(g.Warn+" "+d.warn, inner, "")))
	}
	l = append(l, "", hint)
	return m.dialog("Classify · "+d.ref.String(), l, dw)
}

// classifyFailure words a failed dialog as "cause · what to do". The raw
// error is in the audit log (the AI call and the label write audit their
// own failures; auditClassifyFailure covers the read), never on screen.
func classifyFailure(d *classifyDialog) string {
	switch d.failedAt {
	case clResolving:
		return "could not read the issue from the forge · check the connection and press c again"
	case clApplying:
		return "the labels were not added · try again, or add them on the forge"
	}
	return ai.Human(d.err)
}

// auditClassifyFailure records a classify attempt that failed before any AI
// call, so its raw error lands in the log.
func (m model) auditClassifyFailure(ref forge.Ref, err error) {
	if m.app == nil || m.app.Audit == nil {
		return
	}
	_ = m.app.Audit.Append(audit.Entry{
		Action: "ai.classify", Target: ref.String(), Item: ref.String(), Outcome: "error", Error: err.Error(),
	})
}

// packFields lays fields out joined by sep, filling each line to w cells;
// every line starts with indent and none starts with a bare separator.
func packFields(fields []string, sep, indent string, w int) []string {
	var out []string
	line := ""
	for _, f := range fields {
		switch {
		case line == "":
			line = indent + f
		case lipgloss.Width(line)+lipgloss.Width(sep)+lipgloss.Width(f) <= w:
			line += sep + f
		default:
			out = append(out, line)
			line = indent + f
		}
	}
	if line != "" {
		out = append(out, line)
	}
	for i, ln := range out {
		out[i] = ansi.Truncate(ln, w, "…")
	}
	return out
}

// classifyWhat says how the suggestion is being made.
func (m model) classifyWhat() string {
	if m.cl != nil && m.cl.res != nil && m.app != nil && m.app.Config.AI.Mode != "off" && m.app.Config.AI.Profile != "" {
		return "asking the model"
	}
	return "matching keywords"
}

func (m model) classifySuggestView(d *classifyDialog, inner int) []string {
	g := m.g
	s := d.cr.Suggestion
	how := "keywords · AI is off or not configured · no cost"
	if s.Source == ai.SourceAI {
		how = "AI"
		if r := d.cr.Run; r != nil {
			how += g.sep() + r.Model
			if r.CacheHit {
				how += g.sep() + "cached, $0"
			} else {
				how += g.sep() + fmt.Sprintf("$%.4f", r.CostUSD)
			}
		}
	}
	miss := d.in.Missing()
	l := []string{}
	if len(miss) > 0 {
		l = append(l, section("Suggested labels", how, inner, g))
		if len(d.labels) == 0 {
			l = append(l, stFaint.Render("no label fits "+strings.Join(miss, " and ")+" confidently; add one by hand"))
		}
	}
	for i, lb := range d.labels {
		cur, box := " ", g.Ahead
		if i == d.cur {
			cur = g.Selected
		}
		name := lb.name
		if lb.on {
			box, name = g.Done, stBold.Render(lb.name)
		}
		l = append(l, ansi.Truncate(cur+" "+box+" "+name, inner, "…"))
	}
	if s.Reason != "" {
		l = append(l, "", stFaint.Render(ansi.Wrap("why: "+s.Reason, inner, "")))
	}
	for _, dr := range s.Dropped {
		l = append(l, stFaint.Render(ansi.Truncate("ignored "+dr, inner, "…")))
	}
	var open []string // facets the issue lacks and no suggestion covers
	for _, f := range miss {
		if (f == "type" && s.Type == "") || (f == "app" && len(s.Apps) == 0) {
			open = append(open, f)
		}
	}
	if len(miss) > 0 {
		note := "labels are only added, never removed"
		if len(open) > 0 {
			note = "no " + strings.Join(open, " or ") + " label suggested · " + note
		}
		l = append(l, stFaint.Render(ansi.Wrap(note, inner, "")))
	}
	if len(l) > 0 && (s.Weight != nil || s.WeightReason != "") {
		l = append(l, "")
	}
	switch {
	case s.Weight != nil:
		l = append(l, section("Suggested weight", "from similar closed issues · no model", inner, g),
			"  "+stBold.Render(fmt.Sprintf("%dw", *s.Weight)),
			stFaint.Render(ansi.Wrap(s.WeightReason, inner, "")),
			stFaint.Render("= sets it (shown first, then confirmed)"))
	case s.WeightReason != "":
		l = append(l, stFaint.Render(ansi.Wrap(s.WeightReason, inner, "")))
	}
	return l
}

// classifyOverlayLayer places the dialog in the shared blank-field layer.
func (m model) classifyOverlayLayer(w, h int) *lipgloss.Layer {
	return blankFieldLayer(m.classifyOverlay(w, h), w, h)
}
