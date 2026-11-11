package overview

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// Run shows the live pane until the user quits. The item need not be cached
// yet: the pane says so and fills in when a sync brings it.
func Run(ctx context.Context, o Options) error {
	_, err := tea.NewProgram(newModel(o), tea.WithContext(ctx)).Run()
	if err != nil && ctx.Err() != nil {
		return nil // interrupted: not a failure
	}
	return err
}
