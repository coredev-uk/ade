package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/agent"
	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	hostedit "github.com/coredev-uk/ade/internal/editor"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/project"
)

// The agent surfaces (PLAN 6.6): `a` on a Home item (`P` on the MR page) opens
// the agent menu for that item: start a plan, address the MR's comments, and
// the item's runs. Home's `A` lists every run. Runs show state, spend and
// elapsed time; a plan awaiting approval opens the plan review dialog, a run
// in review shows its diff summary with lazygit and push.
//
// All agent, forge and git work runs in commands; the dialog re-reads the run
// store whenever the cache changes (watch.go), so a run started here or by
// `ade agent` in another pane shows up and moves on its own.

type agStage int

const (
	agList         agStage = iota // start choices and runs
	agPlan                        // a plan awaiting approval, read here
	agDetail                      // a run's state, spend, diff
	agConfirm                     // push, asked
	agDiscard                     // discard a run's changes (the -ai branch, or the worktree's edits), asked
	agStartConfirm                // start a plan or an address run, asked: what is sent, what it may cost
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
	// repos are the repos the item works in, when there are several (the
	// agent acts on one worktree); ri is the targeted one, switched with [ and ].
	// Empty for the common one-repo item.
	repos []agRepo
	ri    int

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

	// aiOff is ai.mode = "off": the start rows give way to a line saying why;
	// the run list keeps working (a run in review can still be pushed or discarded).
	aiOff bool
	// pend is the start the dialog asks about (stage agStartConfirm).
	pend *agPending
	// wantDiscard opens the run on its discard question, not its page.
	wantDiscard bool
}

// agRepo is one repo of a multi-repo item and the MR the agent would address
// there: its open MR with the most unresolved threads, nil when none has any.
type agRepo struct {
	name string
	mr   *forge.MR
}

// agentRepos lists the repos a multi-repo item works in: the repos its labels
// route to, then the repos holding its child issues and MRs, in manifest
// order. Fewer than two repos: nil, the dialog keeps its single target.
func agentRepos(item *domain.WorkItem, man *config.Manifest) []agRepo {
	if man == nil {
		return nil
	}
	names := slices.Clone(item.Repos)
	for _, name := range man.RepoNames() {
		path := man.Repos[name].Path
		held := slices.ContainsFunc(item.MRs, func(mr forge.MR) bool { return strings.EqualFold(mr.Ref.Project, path) }) ||
			slices.ContainsFunc(item.Children, func(c forge.Issue) bool { return strings.EqualFold(c.Ref.Project, path) })
		if held {
			names = append(names, name)
		}
	}
	var out []agRepo
	for _, name := range names {
		if slices.ContainsFunc(out, func(r agRepo) bool { return r.name == name }) {
			continue
		}
		r := agRepo{name: name}
		path := man.Repos[name].Path
		for i := range item.MRs {
			if mr := &item.MRs[i]; mr.State == "opened" && mr.Unresolved > 0 && strings.EqualFold(mr.Ref.Project, path) &&
				(r.mr == nil || mr.Unresolved > r.mr.Unresolved) {
				r.mr = mr
			}
		}
		out = append(out, r)
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// pick targets repo i and the MR to address there.
func (d *agentDialog) pick(i int) {
	d.ri, d.mr = i, d.repos[i].mr
}

// repo is the repo the agent acts in: empty for a one-repo item, which
// resolves its single routed repo itself.
func (d *agentDialog) repo() string {
	if len(d.repos) == 0 {
		return ""
	}
	return d.repos[d.ri].name
}

// agPending is a start that waits for the user's yes: the target and context
// are resolved already (the network part), nothing is created or sent yet.
type agPending struct {
	kind agent.Kind
	t    agent.Target
	c    agent.Context
	pf   ai.Preflight
	// uncapped is set when the driver is codex and a cap applies: the run is
	// created only with the user's explicit yes to running it uncapped.
	uncapped bool
	// runID is set for the implementation of an approved plan (kind run): the
	// run exists already and only the yes to an uncapped codex run is asked.
	runID string
	apart bool
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
	// agPendingMsg is a start that needs the user's yes first.
	agPendingMsg struct {
		d    *agentDialog
		pend *agPending
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
	agEditMsg  struct{ d *agentDialog }
	agLazyDone struct{ err error }
)

// Tests replace these: no real agent, editor or lazygit is ever started.
var (
	agentOpen = agent.ForApp
	agResolve = agent.ResolveTarget
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
	d := &agentDialog{stage: agList, mgr: mgr, item: item, aiOff: m.agAIOff()}
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
		var man *config.Manifest
		if m.app != nil {
			man, _ = project.LoadManifest(filepath.Join(m.app.Root, d.item.Project))
		}
		if d.repos = agentRepos(d.item, man); len(d.repos) > 0 {
			// The repo whose open MR has the most threads; else the first.
			best := 0
			for i, r := range d.repos {
				if r.mr != nil && (d.repos[best].mr == nil || r.mr.Unresolved > d.repos[best].mr.Unresolved) {
					best = i
				}
			}
			d.pick(best)
			return m, m.agLoad(d)
		}
		for i := range d.item.MRs {
			if mr := &d.item.MRs[i]; mr.State == "opened" && mr.Unresolved > 0 {
				d.mr = mr
				break
			}
		}
	}
	return m, m.agLoad(d)
}

func (m model) agLoad(d *agentDialog) tea.Cmd {
	return func() tea.Msg {
		var runs []agent.Run
		var err error
		// Through the manager: a run whose process died is marked failed first.
		if d.item != nil {
			runs, err = d.mgr.ForItem(d.item.Ref)
		} else {
			runs, err = d.mgr.List()
		}
		return agRunsMsg{d: d, runs: runs, err: err}
	}
}

// agAIOff reports ai.mode = "off": no agent can be started.
func (m model) agAIOff() bool {
	return m.app != nil && m.app.Config != nil && m.app.Config.AI.Mode == "off"
}

// agentSteps counts the agent steps in flight (plan, implement, address): quitting
// the dashboard cancels them and waits for each to write its state, so a run is
// never left "running" by a closed dashboard.
var agentSteps sync.WaitGroup

// agGo runs an agent step as a command and counts it in agentSteps.
func agGo(step func() tea.Msg) tea.Cmd {
	return func() tea.Msg {
		agentSteps.Add(1)
		defer agentSteps.Done()
		return step()
	}
}

// waitAgentSteps waits for the steps in flight to settle, at most d.
func waitAgentSteps(d time.Duration) {
	done := make(chan struct{})
	go func() { agentSteps.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
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
	case agStartedMsg:
		return m.agStarted(msg)
	case agPendingMsg:
		d := m.agt
		if d == nil || msg.d != d {
			return m, nil, true
		}
		d.busy, d.warn, d.pend, d.stage = "", "", msg.pend, agStartConfirm
		return m, nil, true
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
		if d.wantDiscard {
			d.wantDiscard = false
			if msg.run.State == agent.StateReview {
				d.stage = agDiscard
			}
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
	return m, tea.Batch(m.agLoad(d), agGo(func() tea.Msg {
		var r agent.Run
		var err error
		if msg.kind == agent.KindAddress {
			r, err = mgr.Address(ctx, id)
		} else {
			r, err = mgr.Plan(ctx, id)
		}
		return agStepMsg{d: d, run: r, err: err}
	})), true
}

// agStart reads the target and the issue's context for the dialog's item (the
// network part), asks the manager what the start would cost, and either
// creates the run at once (a plan that needs no yes) or waits for the user's
// yes in agStartConfirm: always for address (the threads are other people's
// text sent to an agent that runs commands), and whenever the pre-flight asks
// (ai.mode = "ask" over confirm_over_usd or the weekly soft cap) or a codex
// run could not be stopped at its cap. Nothing is created before that.
func (m model) agStart(d *agentDialog, kind agent.Kind) tea.Cmd {
	ctx, a, mgr, ref, repo := m.ctx, m.app, d.mgr, d.item.Ref, d.repo()
	d.busy, d.warn = "reading "+ref.String()+" and its worktree", ""
	return func() tea.Msg {
		t, fg, err := agResolve(ctx, a, nil, ref.String(), repo)
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
		pend := &agPending{kind: kind, t: t, c: c}
		req := agent.Request{Kind: kind, Target: t, Context: c, DryRun: true}
		_, pf, err := mgr.New(ctx, req)
		if errors.Is(err, agent.ErrUncapped) {
			// codex under a cap: ask, then price the call as the run would be.
			pend.uncapped, req.AllowUncapped = true, true
			_, pf, err = mgr.New(ctx, req)
		}
		var need *ai.ErrNeedsConfirm
		switch {
		case errors.As(err, &need):
			pf = need.Preflight
		case err != nil:
			return agStartedMsg{d: d, err: err}
		}
		pend.pf = pf
		if kind == agent.KindAddress || pend.uncapped || len(pf.Confirm) > 0 {
			return agPendingMsg{d: d, pend: pend}
		}
		return agCreate(ctx, mgr, d, pend)
	}
}

// agCreate creates the run of a start the user has agreed to (or that needed
// no yes).
func agCreate(ctx context.Context, mgr *agent.Manager, d *agentDialog, p *agPending) tea.Msg {
	r, _, err := mgr.New(ctx, agent.Request{Kind: p.kind, Target: p.t, Context: p.c, Assume: len(p.pf.Confirm) > 0, AllowUncapped: p.uncapped})
	return agStartedMsg{d: d, run: r, kind: p.kind, err: err}
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
			if !d.cur.AcceptedAt.IsZero() {
				d.busy = "pushing"
			}
			return m, func() tea.Msg {
				r, err := mgr.Push(ctx, id, agent.PushOpts{})
				return agStepMsg{d: d, run: r, err: err}
			}
		case "n", "esc":
			d.stage = agDetail
		}
	case agDiscard:
		switch ks {
		case "y", "enter":
			d.stage, d.busy = agDetail, "removing the -ai worktree and branch"
			if !d.cur.Isolated() {
				d.busy = "putting the worktree back to where the run started"
			}
			ctx, mgr, id := m.ctx, d.mgr, d.cur.ID
			return m, func() tea.Msg {
				r, err := mgr.Discard(ctx, id)
				return agStepMsg{d: d, run: r, err: err}
			}
		case "n", "esc":
			d.stage = agDetail
		}
	case agStartConfirm:
		return m.agStartConfirmKey(d, ks)
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
	case "[", "]":
		if n := len(d.repos); n > 1 && d.busy == "" {
			step := 1
			if ks == "[" {
				step = n - 1
			}
			d.pick((d.ri + step) % n)
		}
	case "p":
		if d.item == nil || d.busy != "" {
			return m, nil
		}
		if d.aiOff {
			d.warn = agAIOffReason
			return m, nil
		}
		return m, m.agStart(d, agent.KindPlan)
	case "c":
		switch {
		case d.item == nil || d.busy != "":
		case d.aiOff:
			d.warn = agAIOffReason
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
			if r := d.runs[d.sel]; r.State == agent.StateReview && !r.Isolated() && r.AcceptedAt.IsZero() {
				// Changes sit in the worktree: cancelling would keep them, and
				// "discard" asks first and puts the worktree back.
				d.cur, d.wantDiscard = r, true
				return m, m.agOpenRun(d, r)
			}
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
			d.warn = "approved: editing the plan voids the approval; enter approves the edited plan, x cancels the run"
		}
		path := filepath.Join(d.mgr.Store.Dir(d.cur), agent.FilePlan)
		return m, tea.ExecProcess(agEditor(m.hostEditor(), path), func(error) tea.Msg { return agEditMsg{d} })
	case "enter", "y":
		if d.busy != "" {
			return m, nil
		}
		if !d.cur.Approved || !d.mgr.PlanIntact(d.cur) {
			// First approval, or the plan was edited after one: approve what is
			// on disk now (the manager keeps the hash of what was approved).
			r, err := d.mgr.Approve(d.cur.ID)
			if err != nil {
				d.warn = err.Error()
				return m, nil
			}
			d.cur, d.warn = r, ""
			return m, m.agLoad(d)
		}
		d.stage, d.busy = agList, ""
		return m, m.agImplement(d, d.cur.ID, false)
	case "i":
		if d.busy != "" || !d.cur.Approved || !d.mgr.PlanIntact(d.cur) {
			if !d.cur.Approved || !d.mgr.PlanIntact(d.cur) {
				d.warn = "approve the plan first (enter)"
			}
			return m, nil
		}
		d.stage, d.busy = agList, ""
		return m, m.agImplement(d, d.cur.ID, true)
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
			return m, agLazygit(d.cur.WorkDir())
		}
	case "a":
		if !d.cur.Isolated() || d.busy != "" {
			return m, nil
		}
		d.busy, d.warn = "fast-forwarding "+d.cur.Branch, ""
		ctx, mgr, id := m.ctx, d.mgr, d.cur.ID
		return m, func() tea.Msg {
			r, err := mgr.Accept(ctx, id, agent.AcceptOpts{})
			return agStepMsg{d: d, run: r, err: err}
		}
	case "D":
		if !d.cur.Isolated() {
			d.warn = "this run has no -ai branch to discard"
			return m, nil
		}
		if d.cur.State.Active() {
			d.warn = "the run is still working: x cancels it first"
			return m, nil
		}
		d.stage, d.warn = agDiscard, ""
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
		if d.cur.State == agent.StateReview && !d.cur.Isolated() && d.cur.AcceptedAt.IsZero() {
			// The agent's edits are in the worktree: discard asks, then puts it back.
			d.stage, d.warn = agDiscard, ""
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

// agAIOffReason is the one line that replaces the start rows with ai.mode = "off".
const agAIOffReason = `AI is off (ai.mode = "off"): plans and replies to comments need it`

// agImplement implements the approved plan of run id (in the worktree, or on a
// -ai branch). A codex run that a cap cannot stop first asks the user.
func (m model) agImplement(d *agentDialog, id string, apart bool) tea.Cmd {
	ctx, mgr := m.ctx, d.mgr
	return tea.Batch(m.agLoad(d), agGo(func() tea.Msg {
		if err := mgr.CheckImplement(ctx, id, apart); errors.Is(err, agent.ErrUncapped) {
			return agPendingMsg{d: d, pend: &agPending{kind: agent.KindRun, runID: id, apart: apart, uncapped: true}}
		}
		implement := mgr.Implement
		if apart {
			implement = mgr.ImplementApart
		}
		r, err := implement(ctx, id)
		return agStepMsg{d: d, run: r, err: err}
	}))
}

// agStartConfirmKey answers the question of a pending start.
func (m model) agStartConfirmKey(d *agentDialog, ks string) (model, tea.Cmd) {
	p := d.pend
	switch ks {
	case "n", "esc":
		d.stage, d.pend = agList, nil
		return m, m.agLoad(d)
	case "y", "enter":
		if p == nil {
			return m, nil
		}
		ctx, mgr := m.ctx, d.mgr
		d.stage, d.pend = agList, nil
		if p.runID != "" {
			// An approved plan whose codex run cannot be capped: the yes is recorded
			// on the run (audited) before it starts.
			d.busy = "implementing"
			id, apart := p.runID, p.apart
			return m, tea.Batch(m.agLoad(d), agGo(func() tea.Msg {
				if _, err := mgr.AllowUncapped(id); err != nil {
					return agStepMsg{d: d, err: err}
				}
				implement := mgr.Implement
				if apart {
					implement = mgr.ImplementApart
				}
				r, err := implement(ctx, id)
				return agStepMsg{d: d, run: r, err: err}
			}))
		}
		d.busy = "starting"
		return m, func() tea.Msg { return agCreate(ctx, mgr, d, p) }
	}
	return m, nil
}
