package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The action dialog is Home's one confirm-then-write dialog for actions that
// make a single change the user should see first: moving a workflow label
// (`m`), cleaning up merged work (`x`), pushing a branch (`P`). It reads what
// it needs in a command (the list never waits), lists the exact change, and
// does it on enter. Every action behind it is the same code as its CLI command
// (`ade issue stage`, `ade cleanup`, `ade push`), so it is audited the same.

type adStage int

const (
	adResolving adStage = iota // reading what the action needs
	adConfirm                  // the change is listed
	adApplying
	adDone
	adFailed
)

// actionPlan is what an action offers once it has read its inputs.
type actionPlan struct {
	// lines are the change, one per row.
	lines []string
	// note is a faint line under them.
	note string
	// nothing says there is nothing to do: the lines say why, and only esc
	// closes the dialog.
	nothing bool
	// verb names enter in the key row ("move", "clean up", "push").
	verb string
	// apply makes the change; its lines are the result rows. An error is shown
	// as the failure, with whatever lines say what was written before it.
	apply func(ctx context.Context) ([]string, error)
	// after runs once the change was made, to re-read what it moved (the item,
	// the trees); it may return nil.
	after func(m model) tea.Cmd
}

// actionSpec is what an action asks for: the dialog's title and the read.
type actionSpec struct {
	title   string
	heading string // the item, in faint text under the title
	reading string // shown while it reads: "reading the issue…"
	// retry is the key that asks again after a failure ("m").
	retry   string
	resolve func(ctx context.Context) (actionPlan, error)
}

type actionDialog struct {
	spec  actionSpec
	stage adStage
	plan  actionPlan
	lines []string // the result rows
	err   error
}

type (
	adResolvedMsg struct {
		d    *actionDialog
		plan actionPlan
		err  error
	}
	adAppliedMsg struct {
		d     *actionDialog
		lines []string
		err   error
	}
)

// anyHomeDialogOpen is true while a Home dialog other than the action dialog is
// open: the action keys then do nothing.
func (m model) anyHomeDialogOpen() bool {
	return m.sw != nil || m.cl != nil || m.agt != nil || m.nt != nil || m.wt != nil || m.sm != nil ||
		m.ait != nil || m.cm != nil || m.ne != nil || m.act != nil || m.hs.pal != nil
}

// openAction starts the dialog for spec and reads its inputs in a command. It
// does nothing off Home and the MR page, or while another dialog is open.
func (m model) openAction(spec actionSpec) (model, tea.Cmd) {
	onMR := m.mr != nil && m.mr.modal == nil && !m.help
	if (!m.onHomePage() && !onMR) || m.anyHomeDialogOpen() {
		return m, nil
	}
	d := &actionDialog{spec: spec, stage: adResolving}
	m.act = d
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return m, func() tea.Msg {
		plan, err := spec.resolve(ctx)
		return adResolvedMsg{d: d, plan: plan, err: err}
	}
}

// actUpdate routes the dialog's messages and, while it is open, every key
// press but ctrl+c.
func (m model) actUpdate(msg tea.Msg) (model, tea.Cmd, bool) {
	d := m.act
	switch msg := msg.(type) {
	case adResolvedMsg:
		if d == nil || msg.d != d {
			return m, nil, true
		}
		if msg.err != nil {
			d.stage, d.err = adFailed, msg.err
			return m, nil, true
		}
		d.plan, d.stage = msg.plan, adConfirm
		return m, nil, true
	case adAppliedMsg:
		if d == nil || msg.d != d {
			// The dialog was closed while it wrote: the change is made all
			// the same, so read again what it moved.
			if msg.d.plan.after != nil {
				return m, msg.d.plan.after(m), true
			}
			return m, nil, true
		}
		d.lines = msg.lines
		var cmd tea.Cmd
		if msg.err != nil {
			d.stage, d.err = adFailed, msg.err
		} else {
			d.stage = adDone
		}
		// A partly made change shows what was made, too.
		if d.plan.after != nil {
			cmd = d.plan.after(m)
		}
		return m, cmd, true
	case tea.KeyPressMsg:
		if d == nil || msg.String() == "ctrl+c" {
			return m, nil, false
		}
		next, cmd := m.actKey(msg.String())
		return next, cmd, true
	}
	return m, nil, false
}

func (m model) actKey(ks string) (model, tea.Cmd) {
	d := m.act
	switch d.stage {
	case adConfirm:
		switch ks {
		case "esc", "q":
			m.act = nil
		case "enter":
			if d.plan.nothing || d.plan.apply == nil {
				return m, nil
			}
			d.stage = adApplying
			ctx := m.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			apply := d.plan.apply
			return m, func() tea.Msg {
				lines, err := apply(ctx)
				return adAppliedMsg{d: d, lines: lines, err: err}
			}
		}
	case adDone, adFailed:
		if ks == "esc" || ks == "enter" || ks == "q" {
			m.act = nil
		}
	default:
		if ks == "esc" {
			m.act = nil
		}
	}
	return m, nil
}

// actionOverlay draws the dialog on the blank field every Home dialog uses.
func (m model) actionOverlay(w int) string {
	d := m.act
	if d == nil {
		return ""
	}
	dw := min(w, 100)
	inner := dw - 4
	g := m.g
	l := []string{}
	if d.spec.heading != "" {
		l = append(l, stFaint.Render(ansi.Truncate(d.spec.heading, inner, g.Ell)), "")
	}
	var hint string
	switch d.stage {
	case adResolving:
		l = append(l, stFaint.Render(g.Sync+" "+g.T(d.spec.reading)))
		hint = keys("esc", "cancel")
	case adConfirm:
		l = append(l, wrapRows(d.plan.lines, inner)...)
		if d.plan.note != "" {
			l = append(l, "", stFaint.Render(ansi.Wrap(g.Info+" "+d.plan.note, inner, "")))
		}
		hint = keys("enter", d.plan.verb, "esc", "cancel")
		if d.plan.nothing || d.plan.apply == nil {
			hint = keys("esc", "close")
		}
	case adApplying:
		l = append(l, stFaint.Render(g.Sync+" writing…"))
		hint = keys("esc", "hide")
	case adDone:
		for _, r := range wrapRows(d.lines, inner-2) {
			l = append(l, stOK.Render(g.OK)+" "+r)
		}
		hint = keys("enter", "close")
	case adFailed:
		l = append(l, stBad.Render(ansi.Wrap(g.Bad+" "+popupText(d.err), inner, "")))
		if d.spec.retry != "" {
			l = append(l, stFaint.Render(ansi.Wrap("  press "+d.spec.retry+" to try again; the cause is in the audit log (L)", inner, "")))
		}
		for _, r := range wrapRows(d.lines, inner-2) {
			l = append(l, stOK.Render(g.OK)+" "+r)
		}
		hint = keys("esc", "close")
	}
	l = append(l, "", hint)
	return m.dialog(g.T(d.spec.title), l, dw)
}

// wrapRows wraps each row to w cells.
func wrapRows(rows []string, w int) []string {
	var out []string
	for _, r := range rows {
		out = append(out, strings.Split(ansi.Wrap(r, max(w, 10), ""), "\n")...)
	}
	return out
}

func (m model) actionOverlayLayer(w, h int) *lipgloss.Layer {
	return blankFieldLayer(m.g, m.actionOverlay(w), w, h)
}
