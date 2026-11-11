package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/project"
)

const (
	toastTTL      = 5 * time.Second
	inboxPollEach = 2 * time.Second
)

// row is one selectable line: a worktree of a repo of a project.
type row struct {
	project string
	repo    project.RepoStatus
	tree    project.TreeStatus
	base    bool // the repo's baseline (default-branch) tree
}

type model struct {
	ctx  context.Context
	app  *app.App
	svc  *project.Service
	in   *notify.Inbox
	g    glyphs
	w, h int

	projects []project.ProjectStatus
	rows     []row
	sel      int
	loadErr  error
	loaded   bool // first status read finished: before it, "no projects" would be a lie
	// showDetail swaps the list for the selected tree's details on narrow
	// terminals, where the two-pane layout does not fit.
	showDetail bool

	updating   bool
	progress   project.Progress
	lastUpdate time.Time
	lastReport *project.Report

	unread  int
	recent  []notify.Item
	toasts  []toast
	drawer  bool
	drawSel int
	help    bool
}

type toast struct {
	ev      events.Event
	expires time.Time // zero: stays until acknowledged (failures)
}

// --- messages ----------------------------------------------------------------

type (
	statusMsg struct {
		projects []project.ProjectStatus
		err      error
	}
	progressMsg   project.Progress
	updateDoneMsg struct {
		report project.Report
		err    error
	}
	eventMsg events.Event
	inboxMsg struct {
		recent []notify.Item
		unread int
	}
	rawMsg         string
	tickMsg        time.Time
	updateTickMsg  time.Time
	startUpdateMsg struct{}
)

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.loadStatus(), m.pollInbox(), tick()}
	if m.app.Config.UpdateOnStart() {
		cmds = append(cmds, func() tea.Msg { return startUpdateMsg{} })
	}
	if iv := m.app.Config.Update.Interval.Duration; iv > 0 {
		cmds = append(cmds, tea.Tick(iv, func(t time.Time) tea.Msg { return updateTickMsg(t) }))
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) loadStatus() tea.Cmd {
	return func() tea.Msg {
		ps, err := m.svc.Status(m.ctx, "")
		return statusMsg{ps, err}
	}
}

func (m model) runUpdate() tea.Cmd {
	return func() tea.Msg {
		r, err := m.svc.Update(m.ctx)
		return updateDoneMsg{r, err}
	}
}

func (m model) pollInbox() tea.Cmd {
	return func() tea.Msg {
		items, err := m.in.List(notify.ListOpts{Limit: 50})
		if err != nil {
			return inboxMsg{}
		}
		n, _ := m.in.UnreadCount()
		return inboxMsg{items, n}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case statusMsg:
		m.projects, m.loadErr, m.loaded = msg.projects, msg.err, true
		m.rows = flatten(msg.projects)
		m.sel = min(m.sel, max(len(m.rows)-1, 0))
	case startUpdateMsg:
		if !m.updating {
			m.updating, m.progress = true, project.Progress{}
			return m, m.runUpdate()
		}
	case updateTickMsg:
		next := tea.Tick(m.app.Config.Update.Interval.Duration, func(t time.Time) tea.Msg { return updateTickMsg(t) })
		if m.updating {
			return m, next
		}
		m.updating, m.progress = true, project.Progress{}
		return m, tea.Batch(m.runUpdate(), next)
	case progressMsg:
		m.progress = project.Progress(msg)
	case updateDoneMsg:
		m.updating, m.lastUpdate = false, time.Now()
		if msg.err == nil {
			r := msg.report
			m.lastReport = &r
		}
		return m, tea.Batch(m.loadStatus(), m.pollInbox())
	case eventMsg:
		m.toasts = append(m.toasts, newToast(events.Event(msg)))
		return m, m.pollInbox()
	case inboxMsg:
		m.recent, m.unread = msg.recent, msg.unread
		m.drawSel = min(m.drawSel, max(len(m.recent)-1, 0))
	case rawMsg:
		return m, tea.Raw(string(msg))
	case tickMsg:
		m.toasts = expire(m.toasts, time.Time(msg))
		cmds := []tea.Cmd{tick()}
		if time.Time(msg).Second()%int(inboxPollEach/time.Second) == 0 {
			cmds = append(cmds, m.pollInbox()) // rows written by other ade processes
		}
		return m, tea.Batch(cmds...)
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func newToast(e events.Event) toast {
	t := toast{ev: e}
	if e.Severity != events.Error {
		t.expires = time.Now().Add(toastTTL)
	}
	return t
}

func expire(ts []toast, now time.Time) []toast {
	out := ts[:0]
	for _, t := range ts {
		if t.expires.IsZero() || now.Before(t.expires) {
			out = append(out, t)
		}
	}
	return out
}

func (m model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c", "q":
		if m.drawer || m.help {
			m.drawer, m.help = false, false
			return m, nil
		}
		return m, tea.Quit
	case "esc":
		switch {
		case m.drawer || m.help:
			m.drawer, m.help = false, false
		case len(m.toasts) > 0:
			m.toasts = m.toasts[:0] // acknowledge sticky failures
		}
	case "tab":
		m.showDetail = !m.showDetail
	case "?":
		m.help = !m.help
	case "i":
		m.drawer = !m.drawer
		m.drawSel = 0
	case "U":
		if !m.updating {
			m.updating, m.progress = true, project.Progress{}
			return m, m.runUpdate()
		}
	case "j", "down":
		if m.drawer {
			m.drawSel = min(m.drawSel+1, max(len(m.recent)-1, 0))
		} else {
			m.sel = min(m.sel+1, max(len(m.rows)-1, 0))
		}
	case "k", "up":
		if m.drawer {
			m.drawSel = max(m.drawSel-1, 0)
		} else {
			m.sel = max(m.sel-1, 0)
		}
	case "r":
		if m.drawer && m.drawSel < len(m.recent) {
			m.in.MarkRead(m.recent[m.drawSel].ID) //nolint:errcheck // shown on next poll
			return m, m.pollInbox()
		}
	case "R":
		if m.drawer {
			m.in.MarkAllRead() //nolint:errcheck // shown on next poll
			return m, m.pollInbox()
		}
	case "y":
		if !m.drawer && m.sel < len(m.rows) {
			r := m.rows[m.sel]
			m.toasts = append(m.toasts, newToast(events.New("ui.copied", events.Info, "copied path "+r.tree.Path)))
			return m, tea.SetClipboard(r.tree.Path)
		}
	}
	return m, nil
}

func flatten(ps []project.ProjectStatus) []row {
	var rows []row
	for _, p := range ps {
		for _, r := range p.Repos {
			if r.Baseline != nil {
				rows = append(rows, row{project: p.Name, repo: r, tree: *r.Baseline, base: true})
			}
			for _, t := range r.Worktrees {
				rows = append(rows, row{project: p.Name, repo: r, tree: t})
			}
			if r.Baseline == nil && len(r.Worktrees) == 0 {
				rows = append(rows, row{project: p.Name, repo: r})
			}
		}
	}
	return rows
}
