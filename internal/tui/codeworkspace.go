package tui

import (
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/workspace"
)

// `v` writes the selected row's multi-root VS Code workspace (every repo of
// the project: the item's worktrees, baselines elsewhere) and opens it when
// `code` is on PATH. It is `ade vscode workspace <ref> --open`.

// codeWorkspace is workspace.CodeWorkspaceFor; tests replace it so VS Code is never started.
var codeWorkspace = workspace.CodeWorkspaceFor

type codeWsMsg struct {
	label string
	res   workspace.CodeOutcome
	err   error
}

// openCodeWorkspace handles `v`: on Home the row's work item (an MR row's item,
// else the MR's tree), on the Worktrees page the tree under the cursor.
func (m model) openCodeWorkspace() (model, tea.Cmd) {
	if m.mr != nil || m.isAgilePage() || m.isToolPage() {
		return m, nil
	}
	ctx, a := m.ctx, m.app
	items := m.itemRefs()
	var (
		label string
		req   func() (workspace.CodeRequest, error)
	)
	switch {
	case m.page == pageHome && m.homeSel < len(m.homeRows):
		r := m.homeRows[m.homeSel]
		label = r.ref()
		req = func() (workspace.CodeRequest, error) {
			if r.item != nil {
				return workspace.CodeRequest{Item: &workspace.ItemRef{Project: r.project, Forge: forgeOf(items, r.project), Item: *r.item}, Open: true}, nil
			}
			if it, ok := workspace.FindItem(items, r.mr.Ref); ok {
				return workspace.CodeRequest{Item: &it, Open: true}, nil
			}
			mf, err := project.LoadManifest(filepath.Join(a.Root, r.project))
			if err != nil {
				return workspace.CodeRequest{}, &recoverable{msg: "project " + r.project + " is not cloned\nrun ade project add", cause: err}
			}
			t, err := workspace.TargetForMR(ctx, a.Root, mf, *r.mr)
			if err != nil {
				return workspace.CodeRequest{}, err
			}
			return workspace.CodeRequest{Worktree: t.Worktree, Open: true}, nil
		}
	case m.page == pageTrees && m.sel < len(m.rows) && m.rows[m.sel].tree.Path != "":
		r := m.rows[m.sel]
		label = r.repo.Name + "/" + r.tree.Name
		req = func() (workspace.CodeRequest, error) {
			c := workspace.CodeRequest{Worktree: r.tree.Path, Open: true}
			if loc, err := workspace.Locate(a.Root, r.tree.Path); err == nil {
				if it, ok := workspace.ItemFor(ctx, items, loc); ok {
					c.Item = &it
				}
			}
			return c, nil
		}
	default:
		return m, nil
	}
	return m, func() tea.Msg {
		c, err := req()
		if err != nil {
			return codeWsMsg{label: label, err: err}
		}
		res, err := codeWorkspace(ctx, a, c)
		return codeWsMsg{label: label, res: res, err: err}
	}
}

// itemRefs is every cached work item with its project and forge.
func (m model) itemRefs() []workspace.ItemRef {
	var items []workspace.ItemRef
	for _, res := range m.results {
		for _, it := range res.Items {
			items = append(items, workspace.ItemRef{Project: res.Project, Forge: res.Forge, Item: it})
		}
	}
	return items
}

// forgeOf is the forge a project's items come from.
func forgeOf(items []workspace.ItemRef, project string) string {
	for _, it := range items {
		if it.Project == project {
			return it.Forge
		}
	}
	return ""
}

// onCodeWs reports the outcome in a pop-up: the file's path, and the hint when
// VS Code could not be started.
func (m *model) onCodeWs(msg codeWsMsg) {
	var e events.Event
	switch {
	case msg.err != nil:
		e = events.New("workspace.vscode.failed", events.Error, "VS Code workspace not written")
		e.Subject = popupText(msg.err)
		e.Detail = fullError(msg.err)
	case msg.res.Hint != "":
		e = events.New("workspace.vscode", events.Warn, "VS Code workspace: "+msg.res.Path)
		e.Subject = msg.res.Hint
		e.Transient = true
	default:
		e = events.New("workspace.vscode", events.Success, "VS Code workspace: "+msg.res.Path)
		e.Transient = true
	}
	m.nc.push(e)
}

// paletteSubject names the selection for palette entries: the Home row's ref,
// else "the selection".
func (m model) paletteSubject() string {
	if ref := m.selectedRef(); ref != "" && m.page == pageHome {
		return ref
	}
	return "the selection"
}
