// Package tui is the ADE dashboard.
package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/app"
)

type model struct{}

func (model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && (k.String() == "q" || k.String() == "ctrl+c") {
		return m, tea.Quit
	}
	return m, nil
}

func (model) View() tea.View { return tea.NewView("ADE\n") }

// Run shows the dashboard until the user quits.
func Run(ctx context.Context, _ *app.App) error {
	_, err := tea.NewProgram(model{}, tea.WithContext(ctx)).Run()
	return err
}
