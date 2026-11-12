package tui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/config"
	hostedit "github.com/coredev-uk/ade/internal/editor"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/workspace"
)

// The Start work dialog (PLAN 6.3). `s` on a Home item opens it; `M` creates
// the MR for work started as Branch only. It is a short flow, not a yes/no:
//
//	choose   repos (app labels preselect), mode, child issue
//	confirm  every write as a station list, the MR body from the template,
//	         its quick actions each kept or stripped
//	done     what was created, with the references
//
// All forge and git work runs in commands; the dialog never blocks the list.

type swStage int

const (
	swResolving swStage = iota // reading the issue and manifest
	swChoose
	swPlanning // looking for what exists already
	swConfirm
	swRunning
	swDone
	swFailed // nothing to show but the error
)

// swRepo is one selectable target in the choose stage.
type swRepo struct {
	name, path string
	on         bool
	tracker    bool
}

// startDialog is the dialog's state. The model holds a pointer to it (nil
// when closed), so copies of the model share one dialog.
type startDialog struct {
	mrOnly bool // `M`: create the MR for started work, nothing else
	stage  swStage
	ref    forge.Ref

	res *startwork.Resolved
	env *startwork.Env
	ch  startwork.Choices

	repos []swRepo
	cur   int // cursor: repos, then modes, then child policies

	found   map[string]startwork.Found
	plan    startwork.Plan
	preview int // target whose MR body is shown
	action  int // quick action under the cursor
	result  *startwork.Result

	body *editor // the MR body being edited in the dialog (E); nil otherwise

	// branchIn is the branch name being typed for the repo at cur (b); nil
	// otherwise. The names kept are in ch.Branches.
	branchIn *textinput.Model

	warn string // one line under the dialog body
	err  error
}

var swModes = []struct {
	mode  startwork.Mode
	label string
	what  string
}{
	{startwork.ModeDraft, "Draft MR", "child, link, branch, draft MR, worktree"},
	{startwork.ModeBranch, "Branch only", "child, link, branch, worktree; M opens the MR later"},
	{startwork.ModeBrowse, "Browse", "worktree on the default branch; no forge write"},
}

var swChildren = []struct {
	policy startwork.ChildPolicy
	what   string
}{
	{startwork.ChildAuto, "a child when the repo is not the tracker's"},
	{startwork.ChildAlways, "always a child issue"},
	{startwork.ChildNever, "none: branch and MR reference the tracker issue"},
}

// swLayouts are the choices for the workspace tab's main pane.
var swLayouts = []struct {
	layout string
	label  string
	what   string
}{
	{workspace.LayoutCode, "editor", "your editor beside lazygit, the overview and the services"},
	{workspace.LayoutAgent, "omp", "an agent briefed on the ticket (ADE implement skill); stops for review"},
	{workspace.LayoutManual, "shell", "a plain shell"},
}

// --- messages ----------------------------------------------------------------

type (
	swResolvedMsg struct {
		d   *startDialog
		res *startwork.Resolved
		err error
	}
	swPlannedMsg struct {
		d     *startDialog
		found map[string]startwork.Found
		err   error
	}
	swDoneMsg struct {
		d   *startDialog
		res startwork.Result
		err error
	}
	swEditedMsg struct {
		d    *startDialog
		body string
		err  error
	}
)

// swEditorCmd builds the editor invocation for the MR body (host.editor
// resolved by the caller); it waits until the file is closed. Tests replace it.
var swEditorCmd = func(ed hostedit.Editor, path string) *exec.Cmd { return ed.Command(path, 0, true) }

// --- opening -----------------------------------------------------------------

// openStart is Home's `s` (mrOnly false) and `M` (true): it opens the dialog
// for the selected work item and starts reading the issue.
func (m model) openStart(mrOnly bool) (model, tea.Cmd) {
	if m.page != pageHome || m.mr != nil || m.isAgilePage() || m.homeSel >= len(m.homeRows) || m.homeRows[m.homeSel].item == nil {
		return m, nil
	}
	it := m.homeRows[m.homeSel].item
	d := &startDialog{mrOnly: mrOnly, ref: it.Ref, stage: swResolving}
	m.sw = d
	ctx, a, sync := m.ctx, m.app, m.sync
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
				return nil, fmt.Errorf("forge %s cannot write issues or merge requests", name)
			}
			return w, nil
		}
		r, err := startwork.Resolve(ctx, a, open, d.ref.String())
		return swResolvedMsg{d: d, res: r, err: err}
	}
}

// swWorkspace builds the workspace opener; tests replace it so no zellij
// session is ever touched.
var swWorkspace = startwork.WorkspaceOpener

// swEnv wires start work to real git auth and the workspace host.
func (m model) swEnv() *startwork.Env {
	env := &startwork.Env{App: m.app, Workspace: swWorkspace(m.app)}
	if m.svc != nil {
		env.Git = m.svc.GitClient
	}
	return env
}

// --- update ------------------------------------------------------------------

// startUpdate routes messages to the open dialog. ok reports the message was
// consumed: while the dialog is open it owns every key press except ctrl+c.
func (m model) startUpdate(msg tea.Msg) (next model, cmd tea.Cmd, ok bool) {
	switch msg := msg.(type) {
	case swResolvedMsg:
		return m.swResolved(msg)
	case swPlannedMsg:
		return m.swPlanned(msg)
	case swDoneMsg:
		return m.swFinished(msg)
	case swEditedMsg:
		if msg.d != m.sw || m.sw == nil {
			return m, nil, true
		}
		d := m.sw
		if msg.err != nil {
			d.warn = "editor: " + msg.err.Error()
			return m, nil, true
		}
		d.ch.MRBody = msg.body
		d.warn = d.replan()
		return m, nil, true
	case tea.KeyPressMsg:
		if m.sw == nil || msg.String() == "ctrl+c" {
			return m, nil, false
		}
		next, cmd := m.swKey(msg)
		return next, cmd, true
	}
	return m, nil, false
}

func (m model) swResolved(msg swResolvedMsg) (model, tea.Cmd, bool) {
	d := m.sw
	if d == nil || msg.d != d {
		return m, nil, true // a closed dialog's answer
	}
	if msg.err != nil {
		d.stage, d.err = swFailed, msg.err
		return m, nil, true
	}
	d.res, d.env = msg.res, m.swEnv()
	d.env.Forge = d.res.Forge
	d.ch = startwork.DefaultChoices(d.res.Manifest, d.res.Item)
	d.ch.Layout = swDefaultLayout(m.app.Config.Host.Layout, d.res.Manifest)
	d.buildRepos()
	if d.mrOnly {
		d.ch.Mode, d.ch.OnlyMR = startwork.ModeDraft, true
		d.ch.Repos = slices.Clone(d.res.Item.Repos)
		if len(d.ch.Repos) == 0 {
			d.stage, d.err = swFailed, errors.New("no repo routes from this issue's app labels: nothing to create an MR in")
			return m, nil, true
		}
		d.stage = swPlanning
		return m, m.swDiscover(d), true
	}
	d.stage = swChoose
	return m, nil, true
}

// swDefaultLayout is the main-pane choice the dialog starts on: host.layout,
// else the manifest's workspace, else the editor. Layouts the dialog does not
// offer (review) fall back to the editor.
func swDefaultLayout(user string, m *config.Manifest) string {
	l := workspace.DefaultLayout(user, m.Workspaces[workspace.DefaultWorkspace].Layout)
	if slices.ContainsFunc(swLayouts, func(x struct{ layout, label, what string }) bool { return x.layout == l }) {
		return l
	}
	return workspace.LayoutCode
}

// buildRepos lists the manifest repos plus the tracker project, preselecting
// the repos the app labels route to.
func (d *startDialog) buildRepos() {
	m := d.res.Manifest
	d.repos = nil
	for _, name := range m.RepoNames() {
		d.repos = append(d.repos, swRepo{name: name, path: m.Repos[name].Path, on: slices.Contains(d.ch.Repos, name)})
	}
	if _, clash := m.Repos[startwork.TrackerRepo]; !clash && m.Tracker.Project != "" {
		d.repos = append(d.repos, swRepo{name: startwork.TrackerRepo, path: m.Tracker.Project, tracker: true})
	}
}

// swDiscover looks for what exists for the chosen repos (the network part of
// planning).
func (m model) swDiscover(d *startDialog) tea.Cmd {
	ctx, env, res, ch := m.ctx, d.env, d.res, d.ch
	return func() tea.Msg {
		found, err := env.Discover(ctx, res.Item, res.Manifest, ch)
		return swPlannedMsg{d: d, found: found, err: err}
	}
}

func (m model) swPlanned(msg swPlannedMsg) (model, tea.Cmd, bool) {
	d := m.sw
	if d == nil || msg.d != d {
		return m, nil, true
	}
	if msg.err != nil {
		d.warn = msg.err.Error()
		d.stage = swChoose
		if d.mrOnly {
			d.stage, d.err = swFailed, msg.err
		}
		return m, nil, true
	}
	d.found = msg.found
	if d.mrOnly {
		// Only repos whose branch exists and still lack their MR.
		var repos []string
		for _, name := range d.ch.Repos {
			if f := msg.found[name]; f.Branch != "" && f.MR == nil {
				repos = append(repos, name)
			}
		}
		if len(repos) == 0 {
			d.stage, d.err = swFailed, errors.New("nothing to create: no repo has a branch without its MR (s starts work)")
			return m, nil, true
		}
		d.ch.Repos = repos
	}
	if w := d.replan(); w != "" {
		d.warn = w
		d.stage = swChoose
		if d.mrOnly {
			d.stage, d.err = swFailed, errors.New(w)
		}
		return m, nil, true
	}
	d.stage, d.warn, d.preview, d.action = swConfirm, "", 0, 0
	return m, nil, true
}

// replan rebuilds the plan from what was found and the current choices; it
// returns a message when the choices cannot be planned.
func (d *startDialog) replan() string {
	p, err := d.env.PlanFor(d.res, d.ch, d.found)
	if err != nil {
		return err.Error()
	}
	d.plan = p
	d.preview = min(d.preview, max(len(p.Targets)-1, 0))
	return ""
}

func (m model) swFinished(msg swDoneMsg) (model, tea.Cmd, bool) {
	d := m.sw
	if d == nil || msg.d != d {
		return m, nil, true
	}
	d.stage, d.result, d.err = swDone, &msg.res, msg.err
	// What was created changes Home's row and the worktrees list: read this
	// one item again and the worktrees' status, not every project.
	var cmd tea.Cmd
	if msg.res.Created() > 0 {
		cmd = m.refreshItem(d.res.Manifest, d.res.Forge, d.ref)
		if m.svc != nil {
			cmd = tea.Batch(cmd, m.loadStatus())
		}
	}
	return m, cmd, true
}

// --- keys --------------------------------------------------------------------

func (m model) swKey(k tea.KeyPressMsg) (model, tea.Cmd) {
	d := m.sw
	ks := k.String()
	switch d.stage {
	case swChoose:
		return m.swChooseKey(d, k)
	case swConfirm:
		if d.body != nil {
			return m.swBodyKey(d, k)
		}
		return m.swConfirmKey(d, ks)
	case swDone, swFailed:
		if ks == "esc" || ks == "enter" || ks == "q" {
			m.sw = nil
		}
	default: // resolving, planning, running: only esc leaves
		if ks == "esc" {
			m.sw = nil
		}
	}
	return m, nil
}

// rows of the choose stage: repos, then modes, then child policies.
func (d *startDialog) rowCount() int { return len(d.repos) + len(swModes) + len(swChildren) }

func (m model) swChooseKey(d *startDialog, k tea.KeyPressMsg) (model, tea.Cmd) {
	if d.branchIn != nil {
		d.branchKey(k)
		return m, nil
	}
	nr, nm := len(d.repos), len(swModes)
	switch ks := k.String(); ks {
	case "esc", "q":
		m.sw = nil
	case "j", "down":
		d.cur = min(d.cur+1, d.rowCount()-1)
	case "k", "up":
		d.cur = max(d.cur-1, 0)
	case "g", "home":
		d.cur = 0
	case "G", "end":
		d.cur = d.rowCount() - 1
	case "m":
		d.ch.Mode = swModes[(swModeIndex(d.ch.Mode)+1)%nm].mode
	case "c":
		d.ch.Child = swChildren[(swChildIndex(d.ch.Child)+1)%len(swChildren)].policy
	case "a":
		d.ch.Layout = swLayouts[(swLayoutIndex(d.ch.Layout)+1)%len(swLayouts)].layout
	case "b":
		if d.cur < nr {
			d.editBranch()
		}
	case "space", " ", "x":
		switch {
		case d.cur < nr:
			d.repos[d.cur].on = !d.repos[d.cur].on
		case d.cur < nr+nm:
			d.ch.Mode = swModes[d.cur-nr].mode
		default:
			d.ch.Child = swChildren[d.cur-nr-nm].policy
		}
	case "enter":
		d.ch.Repos = d.chosen()
		if len(d.ch.Repos) == 0 {
			d.warn = "choose at least one repo (space)"
			return m, nil
		}
		maps.DeleteFunc(d.ch.Branches, func(repo, _ string) bool { return !slices.Contains(d.ch.Repos, repo) })
		d.warn, d.stage = "", swPlanning
		return m, m.swDiscover(d)
	}
	return m, nil
}

// editBranch opens the branch input for the repo under the cursor, holding its
// current name; empty keeps the derived default.
func (d *startDialog) editBranch() {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "empty: the derived name"
	in.SetWidth(32)
	in.SetStyles(branchInputStyles())
	in.SetValue(d.ch.Branches[d.repos[d.cur].name])
	_ = in.Focus() // the cursor is static: no blink command to run
	d.branchIn, d.warn = &in, ""
}

// branchInputStyles is the input in the terminal's own ground, with a static
// reverse-video cursor.
func branchInputStyles() textinput.Styles {
	plain := textinput.StyleState{Text: lipgloss.NewStyle(), Placeholder: stFaint, Prompt: lipgloss.NewStyle()}
	return textinput.Styles{Focused: plain, Blurred: plain, Cursor: textinput.CursorStyle{Color: lipgloss.NoColor{}, Shape: tea.CursorBlock}}
}

// branchKey is a key in the branch input: enter keeps the name (a name also
// ticks the repo), esc drops the change.
func (d *startDialog) branchKey(k tea.KeyPressMsg) {
	switch k.String() {
	case "esc":
		d.branchIn = nil
	case "enter":
		repo := &d.repos[d.cur]
		name := strings.TrimSpace(d.branchIn.Value())
		if name != "" {
			if err := startwork.ValidBranchName(name); err != nil {
				d.warn = err.Error()
				return
			}
			if d.ch.Branches == nil {
				d.ch.Branches = map[string]string{}
			}
			d.ch.Branches[repo.name] = name
			repo.on = true
		} else {
			delete(d.ch.Branches, repo.name)
		}
		d.branchIn, d.warn = nil, ""
	default:
		in, _ := d.branchIn.Update(k)
		d.branchIn = &in
	}
}

func (d *startDialog) chosen() []string {
	var out []string
	for _, r := range d.repos {
		if r.on {
			out = append(out, r.name)
		}
	}
	return out
}

func swModeIndex(m startwork.Mode) int {
	for i, x := range swModes {
		if x.mode == m {
			return i
		}
	}
	return 0
}

// swLayoutWhat says what the chosen main pane is.
func swLayoutWhat(layout string) string {
	return swLayouts[swLayoutIndex(layout)].what
}

func swLayoutIndex(l string) int {
	for i, x := range swLayouts {
		if x.layout == l {
			return i
		}
	}
	return 0
}

func swChildIndex(p startwork.ChildPolicy) int {
	for i, x := range swChildren {
		if x.policy == p {
			return i
		}
	}
	return 0
}

// actions are the quick actions of the previewed target's MR body.
func (d *startDialog) actions() []startwork.QuickAction {
	if d.preview >= len(d.plan.Targets) {
		return nil
	}
	tp := d.plan.Targets[d.preview]
	if d.ch.MRBody != "" {
		return startwork.ParseQuickActions(d.ch.MRBody)
	}
	return tp.MR.Actions
}

func (d *startDialog) stripped(name string) bool {
	return slices.Contains(d.ch.StripActions, "*") || slices.Contains(d.ch.StripActions, name)
}

func (d *startDialog) toggleStrip(name string) {
	if i := slices.Index(d.ch.StripActions, name); i >= 0 {
		d.ch.StripActions = slices.Delete(d.ch.StripActions, i, i+1)
		return
	}
	d.ch.StripActions = append(d.ch.StripActions, name)
}

func (m model) swConfirmKey(d *startDialog, ks string) (model, tea.Cmd) {
	acts := d.actions()
	switch ks {
	case "esc", "backspace":
		if d.mrOnly {
			m.sw = nil
			return m, nil
		}
		d.stage, d.warn = swChoose, ""
	case "j", "down":
		d.action = min(d.action+1, max(len(acts)-1, 0))
	case "k", "up":
		d.action = max(d.action-1, 0)
	case "x", "space", " ":
		if d.ch.MRBody != "" {
			d.warn = "the body was edited by hand: remove the quick action lines in the editor (e)"
			return m, nil
		}
		if d.action < len(acts) {
			d.toggleStrip(acts[d.action].Name)
			d.warn = d.replan()
		}
	case "X":
		if d.ch.MRBody == "" && len(acts) > 0 {
			if slices.Contains(d.ch.StripActions, "*") {
				d.ch.StripActions = nil
			} else {
				d.ch.StripActions = []string{"*"}
			}
			d.warn = d.replan()
		}
	case "p":
		if len(d.plan.Targets) > 0 {
			d.preview, d.action = (d.preview+1)%len(d.plan.Targets), 0
		}
	case "l":
		d.ch.MoveLabel = !d.ch.MoveLabel
		d.warn = d.replan()
	case "t":
		return m.swNextTemplate(d)
	case "e":
		return m.swEdit(d)
	case "E":
		d.swEditHere()
	case "enter":
		if d.plan.Pending() == 0 && d.plan.Writes() == 0 && !d.hasLocalWork() {
			m.sw = nil
			return m, nil
		}
		d.stage = swRunning
		ctx, env, plan := m.ctx, d.env, d.plan
		return m, func() tea.Msg {
			res, err := env.Execute(ctx, plan)
			return swDoneMsg{d: d, res: res, err: err}
		}
	}
	return m, nil
}

func (d *startDialog) hasLocalWork() bool {
	for _, s := range d.plan.Steps {
		if s.Kind == startwork.StepWorkspace {
			return true
		}
	}
	return false
}

// swNextTemplate cycles to the next template the preview repo offers and
// looks the repo up again for its body.
func (m model) swNextTemplate(d *startDialog) (model, tea.Cmd) {
	if d.preview >= len(d.plan.Targets) {
		return m, nil
	}
	tp := d.plan.Targets[d.preview]
	ts := tp.Found.Templates
	if len(ts) < 2 {
		d.warn = "this repo offers one template or none"
		return m, nil
	}
	i := slices.Index(ts, tp.Found.TemplateName)
	d.ch.Template = ts[(i+1)%len(ts)]
	d.ch.MRBody, d.ch.StripActions = "", nil
	d.stage = swPlanning
	return m, m.swDiscover(d)
}

// swEditHere opens the previewed repo's MR body in the dialog's own editor (a
// bubbles textarea), for a quick change that needs no editor of the host. The
// body is kept with ctrl+s; esc throws the change away.
func (d *startDialog) swEditHere() {
	if d.preview >= len(d.plan.Targets) || d.ch.Mode != startwork.ModeDraft {
		return
	}
	tp := d.plan.Targets[d.preview]
	if tp.Found.MR != nil || !hasStep(d.plan, startwork.StepMR, tp.Target.Name) {
		return // no MR step to carry a body
	}
	var child forge.Ref
	if tp.Found.Child != nil {
		child = tp.Found.Child.Ref
	}
	ed := newEditor(strings.TrimRight(tp.MRBody(child, d.plan.Item), "\n") + "\n")
	d.body = &ed
	d.warn = ""
}

// swBodyKey is a key in the inline MR body editor: enter is a newline here
// (the body is prose and lists), ctrl+s keeps the text, esc drops it.
func (m model) swBodyKey(d *startDialog, k tea.KeyPressMsg) (model, tea.Cmd) {
	switch k.String() {
	case "esc":
		d.body = nil
	case "ctrl+s":
		d.ch.MRBody = d.body.String()
		d.body = nil
		d.warn = d.replan()
	case "enter":
		d.body.handle(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	default:
		d.body.handle(k)
	}
	return m, nil
}

// swEdit opens the MR body in $EDITOR (the dialog is suspended meanwhile).
func (m model) swEdit(d *startDialog) (model, tea.Cmd) {
	if d.preview >= len(d.plan.Targets) {
		return m, nil
	}
	tp := d.plan.Targets[d.preview]
	var child forge.Ref
	if tp.Found.Child != nil {
		child = tp.Found.Child.Ref
	}
	f, err := os.CreateTemp("", "ade-mr-body-*.md")
	if err != nil {
		d.warn = err.Error()
		return m, nil
	}
	path := f.Name()
	_, werr := f.WriteString(tp.MRBody(child, d.plan.Item))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		d.warn = werr.Error()
		return m, nil
	}
	return m, tea.ExecProcess(swEditorCmd(m.hostEditor(), path), func(err error) tea.Msg {
		defer os.Remove(path) //nolint:errcheck // a temp file
		if err != nil {
			return swEditedMsg{d: d, err: err}
		}
		b, rerr := os.ReadFile(path)
		return swEditedMsg{d: d, body: string(b), err: rerr}
	})
}

// --- view --------------------------------------------------------------------

// startOverlay draws the dialog, or "" when none is open.
func (m model) startOverlay(w, h int) string {
	d := m.sw
	if d == nil {
		return ""
	}
	dw := min(w, 100) // the Agent dialog's measure; blankFieldLayer centres it
	inner := dw - 4
	title := m.g.T("Start work · ") + d.ref.String()
	if d.mrOnly {
		title = m.g.T("Create MR · ") + d.ref.String()
	}
	var l []string
	if d.res != nil {
		l = append(l, stFaint.Render(ansi.Truncate(d.res.Item.Issue.Title, inner, m.g.Ell)), "")
	}
	var hint string
	switch d.stage {
	case swResolving:
		l = append(l, m.swBusy("reading "+d.ref.String()+" and its project"))
		hint = keys("esc", "cancel")
	case swChoose:
		l = append(l, m.swChooseView(d, inner)...)
		hint = keys("space", "select", "b", "branch", "m", "mode", "c", "child", "a", "main pane", "enter", "continue", "esc", "cancel")
		if d.branchIn != nil {
			hint = keys("enter", "keep", "esc", "discard")
		}
	case swPlanning:
		l = append(l, m.swBusy("looking for an existing child, branch, MR and template"))
		hint = keys("esc", "cancel")
	case swConfirm:
		// Rows left for the confirm lines: the frame, title and hint are fixed.
		room := h - 8
		if d.warn != "" {
			room -= 1 + len(strings.Split(ansi.Wrap(m.g.Warn+" "+d.warn, inner, ""), "\n"))
		}
		var hh string
		l, hh = m.swConfirmView(d, inner, room)
		hint = hh
	case swRunning:
		l = append(l, m.swBusy("creating"))
		hint = keys("esc", "hide")
	case swDone:
		l = append(l, m.swDoneView(d, inner)...)
		hint = keys("enter", "close")
	case swFailed:
		l = append(l, stBad.Render(ansi.Wrap(m.g.Bad+" "+d.err.Error(), inner, "")))
		hint = keys("esc", "close")
	}
	if d.warn != "" && d.stage != swDone {
		l = append(l, "", stWarn.Render(ansi.Wrap(m.g.Warn+" "+d.warn, inner, "")))
	}
	l = append(l, "", hint)
	// On a short terminal drop blank spacer lines (not content) before the
	// dialog would run off the bottom of the screen: from the bottom up, so the
	// gaps above the section rules, which hold the sections apart, go last.
	if over := len(l) + 4 - (h - 2); over > 0 {
		l = dropBlanks(l, over)
	}
	return m.dialog(title, l, dw)
}

// dropBlanks removes up to n empty lines, last first.
func dropBlanks(l []string, n int) []string {
	out := make([]string, len(l))
	copy(out, l)
	for i := len(out) - 1; i >= 0 && n > 0; i-- {
		if out[i] == "" {
			out = slices.Delete(out, i, i+1)
			n--
		}
	}
	return out
}

func (m model) swBusy(what string) string { return stFaint.Render(m.g.Sync + " " + what + m.g.Ell) }

func (m model) swChooseView(d *startDialog, inner int) []string {
	g := m.g
	apps := strings.Join(d.res.Item.Apps, ", ")
	guide := "no app label: pick a repo"
	if apps != "" {
		guide = "app::" + strings.ReplaceAll(apps, ", ", " app::") + " routes here"
	}
	l := []string{section("Repos", guide, inner, g)}
	selected := d.chosen()
	required := startwork.RequiredRepos(d.res.Manifest, selected)
	for i, r := range d.repos {
		cur := " "
		if d.cur == i {
			cur = g.Selected
		}
		name := ink(r.name).Render(pad(m.g, r.name, 9))
		note := ""
		switch {
		case r.tracker:
			note = "work lives in the tracker project"
		case !r.on && slices.Contains(required, r.name):
			note = requiredBy(d.res.Manifest, selected, r.name)
		}
		line := cur + " " + checkbox(g, r.on, "") + name + " " + stFaint.Render(r.path)
		editing := d.branchIn != nil && d.cur == i
		switch {
		case editing:
			line += "  " + stFaint.Render("branch ") + d.branchIn.View()
		case d.ch.Branches[r.name] != "":
			line += "  " + stFaint.Render("branch ") + stBold.Render(d.ch.Branches[r.name])
		}
		if note != "" && !editing {
			line += stFaint.Render("  " + g.Sep + " " + note)
		}
		l = append(l, ansi.Truncate(line, inner, m.g.Ell))
	}
	l = append(l, "", section("What to create", "", inner, g))
	nr := len(d.repos)
	for i, md := range swModes {
		cur := " "
		if d.cur == nr+i {
			cur = g.Selected
		}
		dot := g.Ahead
		label := md.label
		if d.ch.Mode == md.mode {
			dot, label = g.Done, stBold.Render(md.label)
		}
		l = append(l, ansi.Truncate(cur+" "+dot+" "+pad(m.g, label, 12)+" "+stFaint.Render(md.what), inner, m.g.Ell))
	}
	l = append(l, "", section("Child issue", "conventions.child_issue", inner, g))
	for i, c := range swChildren {
		cur := " "
		if d.cur == nr+len(swModes)+i {
			cur = g.Selected
		}
		dot := g.Ahead
		label := string(c.policy)
		if d.ch.Child == c.policy {
			dot, label = g.Done, stBold.Render(label)
		}
		l = append(l, ansi.Truncate(cur+" "+dot+" "+pad(m.g, label, 7)+" "+stFaint.Render(c.what), inner, m.g.Ell))
	}
	// The main pane is one row, toggled with a: its three choices fit the line.
	main := stFaint.Render("Main pane  ")
	for _, x := range swLayouts {
		dot, label := g.Ahead, x.label
		if d.ch.Layout == x.layout {
			dot, label = g.Done, stBold.Render(x.label)
		}
		main += dot + " " + label + "  "
	}
	l = append(l, "", ansi.Truncate(" "+main+stFaint.Render(m.g.T("a toggles · ")+swLayoutWhat(d.ch.Layout)), inner, m.g.Ell))
	return l
}

// requiredBy words the checkout dependency: "web requires api: add an api child?".
func requiredBy(m *config.Manifest, selected []string, req string) string {
	for _, s := range selected {
		if slices.Contains(m.Repos[s].Requires, req) {
			return s + " requires " + req + ": also a child here?"
		}
	}
	return "required by a chosen repo"
}

func (m model) swConfirmView(d *startDialog, inner, room int) ([]string, string) {
	g, p := m.g, d.plan
	guide := fmt.Sprintf(m.g.T("%s · %s"), countWord(p.Writes(), "forge write"), countWord(localSteps(p), "local step"))
	switch {
	case d.ch.Mode == startwork.ModeBrowse:
		guide = m.g.T("no forge writes · ") + countWord(localSteps(p), "local step")
	case p.Pending() == 0:
		guide = "nothing to create: everything exists"
	}
	l := []string{section("Confirm", guide, inner, g)}
	st := m.swStations(d, inner, false)
	draft := d.ch.Mode == startwork.ModeDraft && len(p.Targets) > 0
	// The full step list goes when the terminal could not also show the MR
	// body (or, with no MR block, the list itself): one summary line per repo
	// keeps what will be written readable and leaves the rows to the body.
	if mr, ok := m.swMRParts(d, inner); draft && ok {
		if room-1-len(st)-mr.rows-mr.spacers < min(len(mr.compact), 4) {
			st = m.swStations(d, inner, true)
		}
	} else if len(st)+1 > room {
		st = m.swStations(d, inner, true)
	}
	l = append(l, st...)
	if draft && d.body != nil {
		tp := p.Targets[min(d.preview, len(p.Targets)-1)]
		l = append(l, "", section(m.g.T("MR body · ")+tp.Target.Name, "editing", inner, g))
		l = append(l, d.body.view(inner, max(3, room-len(l)-2), "the merge request body")...)
		return l, keys("ctrl+s", "keep", "enter", "newline", "esc", "discard")
	}
	if draft {
		l = append(l, m.swMRView(d, inner, room-len(l))...)
	}
	hint := keys("enter", "confirm", "x", "keep/strip", "e/E", "edit body", "esc", "back")
	if d.ch.Mode != startwork.ModeDraft {
		hint = keys("enter", "confirm", "esc", "back")
	}
	if len(p.Targets) > 1 {
		hint += "   " + keys("p", "next repo")
	}
	if tp := p.Targets[min(d.preview, len(p.Targets)-1)]; len(tp.Found.Templates) > 1 {
		hint += "   " + keys("t", "template")
	}
	return l, hint
}

// swStations draws the repos as station lines with their steps. compact puts
// each repo's steps on one summary line.
func (m model) swStations(d *startDialog, inner int, compact bool) []string {
	g, p := m.g, d.plan
	var l []string
	cross := len(p.Targets) > 1
	if cross {
		// The repos' lines meet at the tracker item: the interchange.
		names := make([]string, len(p.Targets))
		for i, tp := range p.Targets {
			names[i] = ink(tp.Target.Name).Render(tp.Target.Name)
		}
		l = append(l, stBold.Render(g.Interchange)+" "+stBold.Render(d.ref.String())+" "+stFaint.Render("the lines of ")+strings.Join(names, stFaint.Render(" and "))+stFaint.Render(" meet here"))
	}
	for ti, tp := range p.Targets {
		ik := ink(tp.Target.Name)
		pre := ""
		head := ik.Render(g.Done+g.Travelled) + " "
		if cross {
			conn, trunk := g.Branch, stFaint.Render(g.Vertical)+"  "
			if ti == len(p.Targets)-1 {
				conn, trunk = g.BranchEnd, "   "
			}
			pre, head = trunk, ik.Render(conn+g.Done)+" "
		}
		line := head + stBold.Render(tp.Target.Name) + " " + stFaint.Render(tp.Target.Project)
		if tp.Pinned {
			state := "new"
			if tp.Found.Branch != "" {
				state = "existing"
			}
			line += stFaint.Render("  "+g.Sep+" branch ") + stBold.Render(tp.Branch) + stFaint.Render(" ("+state+")")
		}
		l = append(l, ansi.Truncate(line, inner, m.g.Ell))
		steps := stepsOf(p, tp.Target.Name)
		if compact {
			l = append(l, ansi.Truncate(pre+ik.Render(g.BranchEnd)+" "+stepSummary(g, steps), inner, m.g.Ell))
			continue
		}
		for j, s := range steps {
			conn := g.Branch
			if j == len(steps)-1 {
				conn = g.BranchEnd
			}
			l = append(l, ansi.Truncate(pre+ik.Render(conn)+" "+stepMark(g, s)+" "+stepText(m.g, s, inner-lipgloss.Width(pre)), inner, m.g.Ell))
		}
	}
	// Steps without a repo (the workflow label move) sit under the stations.
	for _, s := range p.Steps {
		if s.Repo == "" {
			l = append(l, ansi.Truncate(stFaint.Render(g.Vertical)+" "+stepMark(g, s)+" "+g.T(s.Text), inner, m.g.Ell))
		}
	}
	if p.Workflow != nil && d.res.Manifest.Labels.Workflow.SyncMode() == "ask" && !d.ch.NoMoveLabel && (d.ch.MoveLabel || p.WorkflowNote != "") {
		l = append(l, stFaint.Render(g.Vertical+" labels.workflow.sync = ask: press l to move the workflow label ")+checkbox(g, d.ch.MoveLabel, ""))
	} else if p.WorkflowNote != "" {
		l = append(l, ansi.Truncate(stFaint.Render(g.Vertical+" "+p.WorkflowNote), inner, m.g.Ell))
	}
	for _, w := range p.Warnings {
		l = append(l, stWarn.Render(g.Warn+" "+ansi.Truncate(w, inner-2, m.g.Ell)))
	}
	return l
}

// stepSummary is a repo's steps on one line: what the forge will write, what
// git will do, what exists already, each group by its mark and words.
func stepSummary(g glyphs, steps []startwork.Step) string {
	var forgeW, local, done []string
	for _, s := range steps {
		w := stepWord(s.Kind)
		switch {
		case s.Done:
			done = append(done, w)
		case s.Write:
			forgeW = append(forgeW, w)
		default:
			local = append(local, w)
		}
	}
	var parts []string
	if len(forgeW) > 0 {
		parts = append(parts, stBold.Render("+ forge")+" "+strings.Join(forgeW, ", "))
	}
	if len(local) > 0 {
		parts = append(parts, "+ local "+strings.Join(local, ", "))
	}
	if len(done) > 0 {
		parts = append(parts, stFaint.Render(g.OK+" exists "+strings.Join(done, ", ")))
	}
	return strings.Join(parts, stFaint.Render(" "+g.Sep+" "))
}

func stepWord(k startwork.StepKind) string {
	if k == startwork.StepMR {
		return "draft MR"
	}
	if k == startwork.StepChild {
		return "child issue"
	}
	return string(k)
}

// swMRBlock is what the MR block is made of, measured before it is drawn.
type swMRBlock struct {
	rule          string
	body, compact []string // the body as sent; without its blank lines
	rows, spacers int      // fixed rows around the body; blank rows above the rules
}

// swMRParts measures the MR block of the previewed repo; ok is false when
// that repo has no MR step to show.
func (m model) swMRParts(d *startDialog, inner int) (swMRBlock, bool) {
	p := d.plan
	tp := p.Targets[d.preview]
	if tp.Found.MR != nil || !hasStep(p, startwork.StepMR, tp.Target.Name) {
		return swMRBlock{}, false
	}
	guide := "no template in " + tp.Target.Project
	if tp.MR.Template != "" {
		guide = "template " + tp.MR.Template
	}
	if d.ch.MRBody != "" {
		guide = "edited by hand"
	}
	var child forge.Ref
	if tp.Found.Child != nil {
		child = tp.Found.Child.Ref
	}
	b := swMRBlock{rule: section(m.g.T("MR · ")+tp.Target.Name, guide, inner, m.g), rows: 1, spacers: 1} // the rule; the blank above it
	b.body = strings.Split(strings.TrimRight(tp.MRBody(child, p.Item), "\n"), "\n")
	for _, ln := range b.body {
		if strings.TrimSpace(ln) != "" {
			b.compact = append(b.compact, ln)
		}
	}
	if n := len(d.actions()); n > 0 {
		b.rows += n + 1 // rule and actions
		b.spacers++
	}
	return b, true
}

// swMRView is the MR block: the body as it will be sent, and the template's
// quick actions with their keep/strip state. room is the rows left for it. The
// body goes in full when it fits; when it does not its blank lines go (they
// carry nothing), then the spacers above the rules, and what still does not
// fit is clipped with an ellipsis line. Only a terminal too short to show two
// lines of it folds it away entirely.
func (m model) swMRView(d *startDialog, inner, room int) []string {
	g := m.g
	mr, ok := m.swMRParts(d, inner)
	if !ok {
		return nil
	}
	l := []string{"", mr.rule}
	body, budget := mr.body, room-mr.rows-mr.spacers
	if len(body) > budget && len(mr.compact) > 0 {
		body = mr.compact
	}
	if len(body) > budget && budget < 3 {
		budget = room - mr.rows
	}
	shown := len(body)
	if shown > budget {
		shown = max(0, budget-1)
		if shown < 2 {
			shown = 0
		}
	}
	for _, ln := range body[:shown] {
		l = append(l, stFaint.Render(g.Vertical)+" "+ansi.Truncate(ln, inner-2, m.g.Ell))
	}
	if shown < len(body) {
		l = append(l, stFaint.Render(g.Vertical+m.g.T(" … ")+countWord(len(body)-shown, "more line")))
	}
	acts := d.actions()
	if len(acts) == 0 {
		return l
	}
	l = append(l, "", section("Quick actions", "the forge runs these when the MR is created", inner, g))
	for i, qa := range acts {
		cur := " "
		if i == d.action {
			cur = g.Selected
		}
		state := "keep "
		if d.stripped(qa.Name) {
			state = "strip"
		}
		mark := stBold.Render(state)
		if state == "strip" {
			mark = stFaint.Render(state)
		}
		l = append(l, ansi.Truncate(cur+" "+mark+" "+qa.Summary+" "+stFaint.Render(qa.Line), inner, m.g.Ell))
	}
	return l
}

func (m model) swDoneView(d *startDialog, inner int) []string {
	g := m.g
	if d.result == nil {
		return nil
	}
	var l []string
	for _, s := range d.result.Steps {
		var mark string
		switch s.Outcome {
		case startwork.OutcomeCreated:
			mark = stOK.Render(g.OK + " created")
		case startwork.OutcomeFailed:
			mark = stBad.Render(g.Bad + " failed ")
		case startwork.OutcomeSkipped:
			mark = stWarn.Render(g.Warn + " skipped")
		default:
			mark = stFaint.Render(g.Sep + " exists ")
		}
		line := mark + " " + doneText(m.g, s, inner)
		l = append(l, ansi.Truncate(line, inner, m.g.Ell))
		// A skipped step's reason also appears as a note below: say it once.
		if s.Error != "" && (s.Outcome != startwork.OutcomeSkipped || !noteHas(d.result.Notes, s.Error)) {
			l = append(l, "          "+stFaint.Render(ansi.Truncate(s.Error, inner-10, m.g.Ell)))
		}
	}
	l = append(l, "")
	for _, t := range d.result.Targets {
		parts := []string{ink(t.Repo).Render(t.Repo)}
		if t.Child != nil {
			parts = append(parts, t.Child.String())
		}
		if t.MR != nil {
			parts = append(parts, t.MR.String())
		}
		if t.Worktree != "" {
			parts = append(parts, truncLeft(m.g, t.Worktree, max(20, inner-40)))
		}
		l = append(l, ansi.Truncate(stFaint.Render(g.Vertical)+" "+strings.Join(parts, stFaint.Render(" "+g.Sep+" ")), inner, m.g.Ell))
	}
	for _, n := range d.result.Notes {
		n = strings.Replace(n, startwork.MoveLabelHint, "press l in the start dialog to apply", 1)
		l = append(l, m.swNote(n, inner)...)
	}
	if d.err != nil {
		l = append(l, "", stBad.Render(ansi.Wrap(g.Bad+" "+d.err.Error(), inner, "")), stFaint.Render("enter closes; s resumes where it stopped"))
	}
	return l
}

// doneText words a finished step with what it produced: the plan's text spoke
// of a child that did not exist yet.
func doneText(g glyphs, s startwork.StepResult, inner int) string {
	if s.Ref == "" || s.Outcome == startwork.OutcomeFailed {
		return g.T(s.Text)
	}
	switch s.Kind {
	case startwork.StepChild:
		return "child issue " + s.Ref
	case startwork.StepBranch:
		return "branch " + s.Ref
	case startwork.StepMR:
		return "draft MR " + s.Ref
	case startwork.StepWorktree:
		return "worktree " + truncLeft(g, s.Ref, max(20, inner-20))
	}
	return g.T(s.Text)
}

// stepText is a step's line; a worktree path is shortened from the left so the
// repo and branch at its end stay visible.
func stepText(g glyphs, s startwork.Step, inner int) string {
	if s.Path == "" || s.Done {
		if s.Path != "" {
			return "worktree " + truncLeft(g, s.Path, max(20, inner-24)) // the mark says exists
		}
		return g.T(s.Text)
	}
	return "fetch, add worktree " + truncLeft(g, s.Path, max(20, inner-34))
}

func noteHas(notes []string, sub string) bool {
	return slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, sub) })
}

func stepsOf(p startwork.Plan, repo string) []startwork.Step {
	var out []startwork.Step
	for _, s := range p.Steps {
		if s.Repo == repo {
			out = append(out, s)
		}
	}
	return out
}

func hasStep(p startwork.Plan, kind startwork.StepKind, repo string) bool {
	return slices.ContainsFunc(p.Steps, func(s startwork.Step) bool { return s.Kind == kind && s.Repo == repo })
}

// localSteps counts the steps the list marks `+ local`: git work and the
// workspace tab.
func localSteps(p startwork.Plan) int {
	n := 0
	for _, s := range p.Steps {
		if !s.Done && !s.Write {
			n++
		}
	}
	return n
}

// swNote is a warning under the done rows. One that does not fit puts what
// follows its first colon (a recovery command, say) on its own lines, wrapped
// at path separators so none of it is cut.
func (m model) swNote(n string, inner int) []string {
	line := m.g.Warn + " " + n
	if lipgloss.Width(line) <= inner {
		return []string{stWarn.Render(line)}
	}
	head, rest, ok := strings.Cut(n, ": ")
	if !ok {
		return strings.Split(stWarn.Render(ansi.Wrap(line, inner, "")), "\n")
	}
	l := []string{stWarn.Render(m.g.Warn + " " + head + ":")}
	for _, ln := range strings.Split(ansi.Wrap(rest, inner-2, "/ "), "\n") {
		l = append(l, "  "+stWarn.Render(ln))
	}
	return l
}

// stepMark is the glyph and word of a step: a write the forge will take, a
// local git step, or something that exists already.
func stepMark(g glyphs, s startwork.Step) string {
	switch {
	case s.Done:
		return stFaint.Render(g.OK + " exists")
	case s.Write:
		return stBold.Render("+ forge")
	default:
		return "+ local"
	}
}

// swOverlayLayer places the dialog like the Agent dialog: at the top, centred,
// in a blank field the size of the body, so nothing of the dashboard shows
// beside or below it and it stays put between states. view.go calls it.
func (m model) swOverlayLayer(w, h int) *lipgloss.Layer {
	return blankFieldLayer(m.g, m.startOverlay(w, h), w, h)
}
