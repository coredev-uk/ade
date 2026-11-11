package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/agent"
	"github.com/coredev-uk/ade/internal/domain"
	hostedit "github.com/coredev-uk/ade/internal/editor"
	"github.com/coredev-uk/ade/internal/forge"
)

// The agent surfaces (PLAN 6.6): `a` on a Home item (`P` on the MR page) opens
// the agent menu for that item: start a plan, address the MR's comments, and
// the item's runs. Home's `A` lists every run. Runs show state, spend and
// elapsed time; a plan awaiting approval opens the plan review dialog, a run
// in review shows its diff summary with lazygit and push.
//
// All agent, forge and git work runs in commands; the dialog polls the run
// store once a second while open, so a run started here or by `ade agent` in
// another pane shows up and moves on its own.

type agStage int

const (
	agList    agStage = iota // start choices and runs
	agPlan                   // a plan awaiting approval, read here
	agDetail                 // a run's state, spend, diff
	agConfirm                // push, asked
)

// agentDialog is the dialog's state. The model holds a pointer (nil when
// closed), so copies of the model share one dialog.
type agentDialog struct {
	stage agStage
	mgr   *agent.Manager
	// item is the work item the menu is for; nil lists every run.
	item *domain.WorkItem
	// mr is the item's MR with unresolved threads, when "address comments" applies.
	mr *forge.MR

	runs []agent.Run
	sel  int
	cur  agent.Run
	// plan is plan.md of cur, scrolled by scroll.
	plan   string
	scroll int
	diff   *agent.DiffSummary

	busy string // what a command is doing, in words
	warn string
	err  error

	// notes is the item's notes (empty: none); noNotes leaves them out of
	// the runs started from this dialog (n toggles).
	notes   string
	noNotes bool
}

// --- messages -----------------------------------------------------------------

type (
	agRunsMsg struct {
		d    *agentDialog
		runs []agent.Run
		err  error
	}
	agStartedMsg struct {
		d    *agentDialog
		run  agent.Run
		kind agent.Kind
		err  error
	}
	// agStepMsg is the end of a long step (plan, run, address, push).
	agStepMsg struct {
		d   *agentDialog
		run agent.Run
		err error
	}
	agOpenedMsg struct {
		d    *agentDialog
		run  agent.Run
		plan string
		diff *agent.DiffSummary
		err  error
	}
	agTickMsg  struct{ d *agentDialog }
	agEditMsg  struct{ d *agentDialog }
	agLazyDone struct{ err error }
)

// Tests replace these: no real agent, editor or lazygit is ever started.
var (
	agentOpen = agent.ForApp
	agEditor  = func(ed hostedit.Editor, path string) *exec.Cmd { return ed.Command(path, 0, true) }
	agLazygit = func(worktree string) tea.Cmd {
		cmd := agent.LazygitCmd(worktree, os.Getenv("ZELLIJ") != "")
		if os.Getenv("ZELLIJ") != "" {
			// A floating pane: nothing to hand the terminal over to.
			return func() tea.Msg { return agLazyDone{err: cmd.Run()} }
		}
		return tea.ExecProcess(cmd, func(err error) tea.Msg { return agLazyDone{err: err} })
	}
)

// --- opening ------------------------------------------------------------------

// openAgent is Home's `a` (the selected item) and `A` (all runs), and the MR
// page's `A` (the page's item).
func (m model) openAgent(item *domain.WorkItem, all bool) (model, tea.Cmd) {
	if m.agt != nil {
		return m, nil
	}
	mgr, err := agentOpen(m.app)
	d := &agentDialog{stage: agList, mgr: mgr, item: item}
	m.agt = d
	if err != nil {
		d.err = err
		return m, nil
	}
	if all {
		d.item = nil
	}
	if d.item != nil {
		d.notes = m.itemNotes(d.item)
		for i := range d.item.MRs {
			if mr := &d.item.MRs[i]; mr.State == "opened" && mr.Unresolved > 0 {
				d.mr = mr
				break
			}
		}
	}
	return m, tea.Batch(m.agLoad(d), agTick(d))
}

func agTick(d *agentDialog) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return agTickMsg{d} })
}

func (m model) agLoad(d *agentDialog) tea.Cmd {
	return func() tea.Msg {
		var runs []agent.Run
		var err error
		if d.item != nil {
			runs, err = d.mgr.Store.ForItem(d.item.Ref)
		} else {
			runs, err = d.mgr.Store.List()
		}
		return agRunsMsg{d: d, runs: runs, err: err}
	}
}

// --- update -------------------------------------------------------------------

// agentUpdate routes messages to the open dialog; while it is open it owns
// every key but ctrl+c. ok reports the message was consumed.
func (m model) agentUpdate(msg tea.Msg) (model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case agRunsMsg:
		d := m.agt
		if d == nil || msg.d != d {
			return m, nil, true
		}
		if msg.err != nil {
			d.err = msg.err
			return m, nil, true
		}
		d.runs = msg.runs
		d.sel = clamp(d.sel, 0, len(d.runs)-1)
		// The open run follows its stored state: a plan that finished while
		// it was open, a run that moved on in another pane.
		for _, r := range d.runs {
			if r.ID == d.cur.ID && d.stage != agList {
				d.cur = r
			}
		}
		return m, nil, true
	case agTickMsg:
		d := m.agt
		if d == nil || msg.d != d {
			return m, nil, true
		}
		return m, tea.Batch(m.agLoad(d), agTick(d)), true
	case agStartedMsg:
		return m.agStarted(msg)
	case agStepMsg:
		d := m.agt
		if d == nil || msg.d != d {
			return m, nil, true
		}
		d.busy = ""
		if msg.err != nil && !errors.Is(msg.err, agent.ErrCancelled) {
			d.warn = msg.err.Error()
		}
		if msg.run.ID != "" && d.cur.ID == msg.run.ID {
			d.cur = msg.run
		}
		return m, m.agLoad(d), true
	case agOpenedMsg:
		d := m.agt
		if d == nil || msg.d != d {
			return m, nil, true
		}
		if msg.err != nil {
			d.warn = msg.err.Error()
			return m, nil, true
		}
		d.cur, d.plan, d.diff, d.scroll, d.warn = msg.run, msg.plan, msg.diff, 0, ""
		d.stage = agDetail
		if msg.run.State == agent.StateAwaiting {
			d.stage = agPlan
		}
		return m, nil, true
	case agEditMsg:
		if d := m.agt; d != nil && msg.d == d {
			return m, m.agOpenRun(d, d.cur), true
		}
		return m, nil, true
	case agLazyDone:
		if d := m.agt; d != nil && msg.err != nil {
			d.warn = "lazygit: " + msg.err.Error()
		}
		return m, nil, true
	case tea.KeyPressMsg:
		if k := msg.String(); m.agt == nil {
			if k == "P" && m.mr != nil && m.mr.modal == nil {
				if m.mr.item == nil {
					return m, nil, false
				}
				next, cmd := m.openAgent(m.mr.item, false)
				return next, cmd, true
			}
			return m, nil, false
		}
		if msg.String() == "ctrl+c" {
			return m, nil, false
		}
		next, cmd := m.agKey(msg)
		return next, cmd, true
	}
	return m, nil, false
}

// agStarted follows a created run into its first long step.
func (m model) agStarted(msg agStartedMsg) (model, tea.Cmd, bool) {
	d := m.agt
	if d == nil || msg.d != d {
		return m, nil, true
	}
	if msg.err != nil {
		d.busy, d.warn = "", msg.err.Error()
		return m, nil, true
	}
	d.warn, d.cur = "", msg.run
	ctx, mgr, id := m.ctx, d.mgr, msg.run.ID
	return m, tea.Batch(m.agLoad(d), func() tea.Msg {
		var r agent.Run
		var err error
		if msg.kind == agent.KindAddress {
			r, err = mgr.Address(ctx, id)
		} else {
			r, err = mgr.Plan(ctx, id)
		}
		return agStepMsg{d: d, run: r, err: err}
	}), true
}

// agStart creates a run for the dialog's item and starts its first step: the
// target and the issue's context are read first (the network part).
func (m model) agStart(d *agentDialog, kind agent.Kind) tea.Cmd {
	ctx, a, mgr, ref := m.ctx, m.app, d.mgr, d.item.Ref
	d.busy, d.warn = "reading "+ref.String()+" and its worktree", ""
	return func() tea.Msg {
		t, fg, err := agent.ResolveTarget(ctx, a, nil, ref.String(), "")
		if err != nil {
			return agStartedMsg{d: d, err: err}
		}
		c, err := agent.Gather(ctx, fg, t)
		if err != nil {
			return agStartedMsg{d: d, err: err}
		}
		if d.notes != "" && !d.noNotes {
			if err := agentNotes(&c, a, t); err != nil {
				return agStartedMsg{d: d, err: err}
			}
		}
		// The pre-flight's confirmation is the dialog itself: the user
		// pressed p, and the run is capped.
		r, _, err := mgr.New(ctx, agent.Request{Kind: kind, Target: t, Context: c, Assume: true})
		return agStartedMsg{d: d, run: r, kind: kind, err: err}
	}
}

// agOpenRun loads what a run's page shows: the plan, or the diff summary.
func (m model) agOpenRun(d *agentDialog, r agent.Run) tea.Cmd {
	ctx, mgr := m.ctx, d.mgr
	return func() tea.Msg {
		r, err := mgr.Store.Get(r.ID)
		if err != nil {
			return agOpenedMsg{d: d, err: err}
		}
		out := agOpenedMsg{d: d, run: r}
		if r.State == agent.StateAwaiting {
			if out.plan, err = mgr.PlanText(r); err != nil {
				out.err = err
			}
			return out
		}
		if r.BaseSHA != "" {
			if ds, _, err := mgr.Diff(ctx, r.ID, false); err == nil {
				out.diff = &ds
			}
		}
		if txt, err := mgr.PlanText(r); err == nil {
			out.plan = txt
		}
		return out
	}
}

// --- keys ---------------------------------------------------------------------

func (m model) agKey(k tea.KeyPressMsg) (model, tea.Cmd) {
	d := m.agt
	ks := k.String()
	if d.err != nil { // nothing works without the manager
		if ks == "esc" || ks == "enter" || ks == "q" {
			m.agt = nil
		}
		return m, nil
	}
	switch d.stage {
	case agList:
		return m.agListKey(d, ks)
	case agPlan:
		return m.agPlanKey(d, ks)
	case agDetail:
		return m.agDetailKey(d, ks)
	case agConfirm:
		switch ks {
		case "y", "enter":
			d.stage, d.busy = agDetail, "committing and pushing"
			ctx, mgr, id := m.ctx, d.mgr, d.cur.ID
			return m, func() tea.Msg {
				r, err := mgr.Push(ctx, id, agent.PushOpts{})
				return agStepMsg{d: d, run: r, err: err}
			}
		case "n", "esc":
			d.stage = agDetail
		}
	}
	return m, nil
}

func (m model) agListKey(d *agentDialog, ks string) (model, tea.Cmd) {
	switch ks {
	case "esc", "q", "a", "A":
		m.agt = nil
	case "j", "down":
		d.sel = clamp(d.sel+1, 0, len(d.runs)-1)
	case "k", "up":
		d.sel = clamp(d.sel-1, 0, len(d.runs)-1)
	case "g", "home":
		d.sel = 0
	case "G", "end":
		d.sel = max(len(d.runs)-1, 0)
	case "p":
		if d.item == nil || d.busy != "" {
			return m, nil
		}
		return m, m.agStart(d, agent.KindPlan)
	case "c":
		switch {
		case d.item == nil || d.busy != "":
		case d.mr == nil:
			d.warn = "no open MR with unresolved threads to address"
		default:
			return m, m.agStart(d, agent.KindAddress)
		}
	case "n":
		if d.item != nil && d.notes != "" {
			d.noNotes = !d.noNotes
		}
	case "enter", "l", "right":
		if d.sel < len(d.runs) {
			d.cur = d.runs[d.sel]
			return m, m.agOpenRun(d, d.cur)
		}
	case "x":
		if d.sel < len(d.runs) && !d.runs[d.sel].State.Terminal() {
			if _, err := d.mgr.Cancel(d.runs[d.sel].ID); err != nil {
				d.warn = err.Error()
			}
			return m, m.agLoad(d)
		}
	}
	return m, nil
}

func (m model) agPlanKey(d *agentDialog, ks string) (model, tea.Cmd) {
	switch ks {
	case "esc", "h", "left":
		d.stage, d.warn = agList, ""
		return m, m.agLoad(d)
	case "j", "down":
		d.scroll++
	case "k", "up":
		d.scroll = max(d.scroll-1, 0)
	case "pgdown", "ctrl+d", "space":
		d.scroll += 10
	case "pgup", "ctrl+u":
		d.scroll = max(d.scroll-10, 0)
	case "g", "home":
		d.scroll = 0
	case "e":
		if d.cur.Approved {
			d.warn = "approved: editing the plan voids the approval; x discards the run"
		}
		path := filepath.Join(d.mgr.Store.Dir(d.cur), agent.FilePlan)
		return m, tea.ExecProcess(agEditor(m.hostEditor(), path), func(error) tea.Msg { return agEditMsg{d} })
	case "enter", "y":
		if d.busy != "" {
			return m, nil
		}
		if !d.cur.Approved {
			r, err := d.mgr.Approve(d.cur.ID)
			if err != nil {
				d.warn = err.Error()
				return m, nil
			}
			d.cur, d.warn = r, ""
			return m, m.agLoad(d)
		}
		ctx, mgr, id := m.ctx, d.mgr, d.cur.ID
		d.stage, d.busy = agList, ""
		return m, tea.Batch(m.agLoad(d), func() tea.Msg {
			r, err := mgr.Implement(ctx, id)
			return agStepMsg{d: d, run: r, err: err}
		})
	case "x":
		if _, err := d.mgr.Cancel(d.cur.ID); err != nil {
			d.warn = err.Error()
			return m, nil
		}
		d.stage = agList
		return m, m.agLoad(d)
	}
	return m, nil
}

func (m model) agDetailKey(d *agentDialog, ks string) (model, tea.Cmd) {
	switch ks {
	case "esc", "h", "left":
		d.stage, d.warn = agList, ""
		return m, m.agLoad(d)
	case "l":
		if d.cur.Worktree != "" {
			return m, agLazygit(d.cur.Worktree)
		}
	case "P":
		if d.cur.State != agent.StateReview {
			d.warn = fmt.Sprintf("run %s is %s: only a run in review can be pushed", d.cur.ID, d.cur.State)
			return m, nil
		}
		d.stage, d.warn = agConfirm, ""
	case "x":
		if d.cur.State.Terminal() {
			return m, nil
		}
		if _, err := d.mgr.Cancel(d.cur.ID); err != nil {
			d.warn = err.Error()
			return m, nil
		}
		return m, m.agOpenRun(d, d.cur)
	case "p":
		if d.cur.Kind == agent.KindPlan && d.plan != "" {
			d.stage = agPlan
			if d.cur.State != agent.StateAwaiting {
				d.stage = agDetail
			}
		}
	}
	return m, nil
}
