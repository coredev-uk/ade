package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/workspace"
)

// `w` on a work item with worktrees in more than one repo asks which one to
// open: a picker (the palette's list) with one entry per repo and an `all`
// entry that opens one tab per tree. With none or one tree there is nothing to
// choose and the tab opens at once.

// wsItemWorktrees is workspace.ItemWorktrees; tests replace it so no repo is read.
var wsItemWorktrees = workspace.ItemWorktrees

type (
	// wsChoice is one entry of the picker: the repo's tree and its label.
	wsChoice struct {
		label  string
		target workspace.Target
	}
	// wsPickMsg asks Home to show the picker.
	wsPickMsg struct {
		choices []wsChoice
		other   bool
	}
	// wsManyMsg is the outcome of opening several tabs, in order.
	wsManyMsg []wsOpenedMsg
)

// openItemWorkspace opens the item's workspace, or asks which tree when the
// item has worktrees in several repos.
func (m model) openItemWorkspace(proj string, it domain.WorkItem, items []workspace.ItemRef, other bool) tea.Cmd {
	ctx, a := m.ctx, m.app
	return func() tea.Msg {
		mf, err := loadProject(a, proj)
		if err != nil {
			return wsOpenedMsg{err: err}
		}
		wts := wsItemWorktrees(ctx, a.Root, mf, it)
		if len(wts) > 1 {
			trees := make(map[string]string, len(wts))
			for _, w := range wts {
				trees[w.Repo] = w.Path
			}
			choices := make([]wsChoice, 0, len(wts))
			for _, w := range wts {
				label := w.Repo + " " + m.g.Sep + " " + w.Branch
				if mr := mrOnBranch(it, mf.Repos[w.Repo].Path, w.Branch); mr != nil {
					label += " " + m.g.Sep + " " + fmt.Sprintf("!%d %s", mr.Ref.IID, mrStateWord(mr.State))
				}
				choices = append(choices, wsChoice{label: label, target: workspace.Target{
					Project: proj, Repo: w.Repo, Worktree: w.Path, Title: w.Branch, ItemTrees: trees,
				}})
			}
			return wsPickMsg{choices: choices, other: other}
		}
		t, err := workspace.TargetForItem(ctx, a.Root, mf, it)
		if err != nil {
			return wsOpenedMsg{err: err}
		}
		return m.openTarget(t, items, other)
	}
}

// mrOnBranch is the item's MR whose source branch is branch in the repo at repoPath.
func mrOnBranch(it domain.WorkItem, repoPath, branch string) *forge.MR {
	for i := range it.MRs {
		if mr := &it.MRs[i]; strings.EqualFold(mr.Ref.Project, repoPath) && mr.SourceBranch == branch {
			return mr
		}
	}
	return nil
}

func mrStateWord(state string) string {
	if state == "opened" {
		return "open"
	}
	return state
}

// openWsPicker shows the picker: each tree, then `all`.
func (m model) openWsPicker(msg wsPickMsg) model {
	if m.hs.pal != nil {
		return m
	}
	other := msg.other
	var es []palEntry
	for _, c := range msg.choices {
		es = append(es, palEntry{name: c.label, run: func(m model) (model, tea.Cmd) {
			items := m.wsItems()
			return m, func() tea.Msg { return m.openTarget(c.target, items, other) }
		}})
	}
	es = append(es, palEntry{name: fmt.Sprintf("all %s %d tabs", m.g.Sep, len(msg.choices)), run: func(m model) (model, tea.Cmd) {
		items := m.wsItems()
		return m, func() tea.Msg {
			out := make(wsManyMsg, 0, len(msg.choices))
			for _, c := range msg.choices {
				out = append(out, m.openTarget(c.target, items, other))
			}
			return out
		}
	}})
	m.hs.pal = &palette{title: "Open workspace", all: es}
	return m
}
