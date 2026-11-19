package tui

import hostedit "github.com/coredev-uk/ade/internal/editor"

// editor is host.editor resolved against the environment (PLAN 6.7): `code`
// inside a VS Code terminal, else $VISUAL / $EDITOR, else nvim, vim, vi or nano.
func (m model) hostEditor() hostedit.Editor {
	setting := ""
	if m.app != nil && m.app.Config != nil {
		setting = m.app.Config.Host.Editor
	}
	return hostedit.Resolve(setting, hostedit.FromOS())
}
