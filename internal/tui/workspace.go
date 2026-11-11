package tui

import (
	"errors"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/host"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/workspace"
)

// `w` opens the selected row's workspace: the work item's own worktree when it
// has one (else its repo's baseline), an MR's source branch tree, or the tree
// under the cursor on the Worktrees page. The tab opens in the zellij session;
// every other host answers with a hint.

// wsOpen is workspace.Open; tests replace it so no host is ever touched.
var wsOpen = workspace.Open

// wsState is Home's view of running instances.
type wsState struct {
	chips map[string][]svcChip // work item ref → its instances
}

// svcChip is one entry of the Services strip.
type svcChip struct {
	node    string
	port    int
	state   string // ready | starting | stopped | failed | blocked
	feature bool   // runs in a feature tree, not the repo's baseline
	shared  bool   // more than one worktree's workspace holds it
	blocker string // for blocked: the requirement that died
	row     workspace.Row
}

type (
	wsOpenedMsg struct {
		label string // what was opened, for the pop-up
		res   workspace.OpenResult
		err   error
	}
	wsChipsMsg struct{ chips map[string][]svcChip }
)

// openWorkspace handles `w`; other is `W`, which opens the tab with the other
// main pane: omp with the ADE implement skill when the default is not omp, the
// editor when it is.
func (m model) openWorkspace(other bool) (model, tea.Cmd) {
	if m.mr != nil || m.isAgilePage() || m.isToolPage() {
		return m, nil
	}
	ctx, a := m.ctx, m.app
	var items []workspace.ItemRef
	for _, res := range m.results {
		for _, it := range res.Items {
			items = append(items, workspace.ItemRef{Project: res.Project, Forge: res.Forge, Item: it})
		}
	}
	var target func() (workspace.Target, error)
	switch {
	case m.page == pageHome && m.homeSel < len(m.homeRows):
		r := m.homeRows[m.homeSel]
		target = func() (workspace.Target, error) {
			mf, err := project.LoadManifest(filepath.Join(a.Root, r.project))
			if err != nil {
				return workspace.Target{}, &recoverable{
					msg:   "project " + r.project + " is not cloned\nrun ade project add",
					cause: err,
				}
			}
			if r.item != nil {
				return workspace.TargetForItem(ctx, a.Root, mf, *r.item)
			}
			return workspace.TargetForMR(ctx, a.Root, mf, *r.mr)
		}
	case m.page == pageTrees && m.sel < len(m.rows) && m.rows[m.sel].tree.Path != "":
		r := m.rows[m.sel]
		target = func() (workspace.Target, error) {
			return workspace.Target{Project: r.project, Repo: r.repo.Name, Worktree: r.tree.Path, Title: r.tree.Name, Baseline: r.base}, nil
		}
	default:
		return m, nil
	}
	return m, func() tea.Msg {
		t, err := target()
		if err != nil {
			return wsOpenedMsg{err: err}
		}
		label := t.Repo + "/" + t.Title
		opts := []workspace.OpenOption{workspace.WithItemTrees(t.ItemTrees), workspace.WithTitle(t.Title), workspace.WithItems(items)}
		if other {
			opts = append(opts, workspace.WithLayout(otherMainPane(a, t.Project)))
		}
		res, err := wsOpen(ctx, a, t.Project, t.Repo, t.Worktree, opts...)
		return wsOpenedMsg{label: label, res: res, err: err}
	}
}

// otherMainPane is the layout `W` asks for: omp unless that is the default, then the editor.
func otherMainPane(a *app.App, proj string) string {
	manifestLayout := ""
	if mf, err := project.LoadManifest(filepath.Join(a.Root, proj)); err == nil {
		manifestLayout = mf.Workspaces[workspace.DefaultWorkspace].Layout
	}
	if workspace.DefaultLayout(a.Config.Host.Layout, manifestLayout) == workspace.LayoutAgent {
		return workspace.LayoutCode
	}
	return workspace.LayoutAgent
}

// onWsOpened turns the outcome into a pop-up. A session that is not running
// is a hint, not a failure: the dashboard may simply run in another terminal.
func (m *model) onWsOpened(msg wsOpenedMsg) tea.Cmd {
	var (
		e   events.Event
		cmd tea.Cmd
	)
	switch {
	case errors.Is(msg.err, workspace.ErrNoSession):
		// The pop-up shows title and subject, not detail: both must carry the hint.
		e = events.New("workspace.hint", events.Warn, "workspace not opened: no "+host.SessionName+" session")
		e.Subject = "start ADE with `ade`, then press w"
		e.Detail = "the zellij session " + host.SessionName + " is not running; start ADE with `ade` (it creates the session), then press w again"
		e.Transient = true
	case msg.err != nil:
		e = events.New("workspace.failed", events.Error, "workspace not opened")
		e.Subject = popupText(msg.err)
		e.Detail = fullError(msg.err)
	case msg.res.Hint != "":
		// The command is too long for a pop-up to show cleanly, so it is copied
		// (OSC 52, works in VS Code and browser terminals) and the pop-up says so.
		e = events.New("workspace.hint", events.Info, msg.label+": no tab on this host")
		e.Subject = "command copied, paste it in a shell\nade ws up --no-zellij runs its services there"
		e.Transient = true
		cmd = tea.SetClipboard(msg.res.Hint)
	case msg.res.Created:
		e = events.New("workspace.opened", events.Success, "opened workspace "+msg.label)
		e.Transient = true
	default:
		e = events.New("workspace.opened", events.Info, "focused workspace "+msg.label)
		e.Transient = true
	}
	m.nc.push(e)
	return cmd
}

// recoverable is an error whose message already says what to do next; the
// underlying cause (a path, an OS message) stays out of the pop-up and goes to
// the detail.
type recoverable struct {
	msg   string
	cause error
}

func (e *recoverable) Error() string { return e.msg }
func (e *recoverable) Unwrap() error { return e.cause }

// popupText is the error as a pop-up shows it: a recoverable error whole (it
// already says what to do), any other error's first line.
func popupText(err error) string {
	var r *recoverable
	if errors.As(err, &r) {
		return r.msg
	}
	return firstLine(err.Error())
}

func fullError(err error) string {
	var r *recoverable
	if errors.As(err, &r) && r.cause != nil {
		return r.msg + "\n" + r.cause.Error()
	}
	return err.Error()
}

func firstLine(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return first
}

// --- services -------------------------------------------------------------------

// wsPoll reads the running instances and maps them to work items.
func (m model) wsPoll() tea.Cmd {
	a, ctx := m.app, m.ctx
	trees := m.rows
	var items []workspace.ItemRef
	for _, res := range m.results {
		for _, it := range res.Items {
			items = append(items, workspace.ItemRef{Project: res.Project, Forge: res.Forge, Item: it})
		}
	}
	return func() tea.Msg {
		mgr, err := workspace.NewManager(a)
		if err != nil {
			return wsChipsMsg{}
		}
		rows, err := mgr.Status(ctx)
		if err != nil || len(rows) == 0 {
			return wsChipsMsg{}
		}
		manifests := map[string]*config.Manifest{}
		repoPath := func(proj, repo string) string {
			mf, ok := manifests[proj]
			if !ok {
				mf, _ = project.LoadManifest(filepath.Join(a.Root, proj))
				manifests[proj] = mf
			}
			if mf == nil {
				return ""
			}
			return mf.Repos[repo].Path
		}
		return wsChipsMsg{chips: buildChips(rows, trees, items, repoPath)}
	}
}

// buildChips groups instances by the work items whose workspaces hold them. A
// shared instance (one api behind two web worktrees) shows under both items.
func buildChips(instances []workspace.Row, trees []row, items []workspace.ItemRef, repoPath func(project, repo string) string) map[string][]svcChip {
	byPath := map[string]row{}
	baseline := map[string]bool{}
	for _, r := range trees {
		byPath[filepath.Clean(r.tree.Path)] = r
		if r.base {
			baseline[filepath.Clean(r.tree.Path)] = true
		}
	}
	chips := map[string][]svcChip{}
	seen := map[string]bool{}
	for _, in := range instances {
		if in.Kind == "oneshot" {
			continue
		}
		for _, h := range in.Holders {
			wt, _, _ := strings.Cut(h, "#")
			info, ok := byPath[filepath.Clean(wt)]
			if !ok {
				continue
			}
			_, tracker, ok := workspace.ItemOf(items, info.project, repoPath(info.project, info.repo.Name), info.tree.Branch)
			if !ok {
				continue
			}
			key := tracker.String()
			if seen[key+"|"+in.ID] {
				continue
			}
			seen[key+"|"+in.ID] = true
			c := svcChip{
				node: in.Node, port: in.Port, state: "ready", shared: heldByMany(in.Holders), row: in,
				feature: filepath.Clean(in.Tree) != filepath.Clean(wt) && !baseline[filepath.Clean(in.Tree)],
			}
			switch {
			case !in.Alive && in.State == workspace.StateStarting:
				c.state = "failed" // died before it ever became ready
			case !in.Alive:
				c.state = "stopped"
			case in.State == workspace.StateStarting:
				c.state = "starting"
			case in.State == workspace.StateBlocked:
				c.state, c.blocker = "blocked", in.BlockedBy
			}
			chips[key] = append(chips[key], c)
		}
	}
	return chips
}

// wsPollEach is how often (in seconds) Home re-reads the running instances.
const wsPollEach = 2

// heldByMany reports whether holders name more than one worktree: a task pane
// of a workspace holds as "<tree>#task:<node>", which is the same workspace.
func heldByMany(holders []string) bool {
	trees := map[string]bool{}
	for _, h := range holders {
		wt, _, _ := strings.Cut(h, "#")
		trees[filepath.Clean(wt)] = true
	}
	return len(trees) > 1
}
