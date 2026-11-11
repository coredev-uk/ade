// Package tui is the ADE dashboard (Interchange direction; see
// .impeccable/surfaces/internal-tui.md).
package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/project"
)

// Run shows the dashboard until the user quits. It owns the update loop and
// the notifier for this process: events become toasts and inbox rows, and
// terminal notifications are written through the renderer, never around it.
func Run(ctx context.Context, a *app.App) error {
	cache, err := a.Cache()
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	svc := project.New(a, "ade:update")
	m := model{ctx: ctx, app: a, svc: svc, in: notify.NewInbox(cache), g: glyphSet(a.Config.Host.Glyphs)}
	p := tea.NewProgram(m, tea.WithContext(ctx))

	svc.OnProgress = func(pr project.Progress) { p.Send(progressMsg(pr)) }
	stop, err := notify.Attach(ctx, a, notify.Channels{
		Toast:    func(e events.Event) { p.Send(eventMsg(e)) },
		Terminal: rawWriter{p},
	})
	if err != nil {
		return err
	}
	defer stop()

	_, err = p.Run()
	if err != nil && ctx.Err() != nil {
		return nil // interrupted: not a failure
	}
	return err
}

// rawWriter routes terminal-notification escape sequences through Bubble Tea
// so they never interleave with a half-drawn frame.
type rawWriter struct{ p *tea.Program }

func (w rawWriter) Write(b []byte) (int, error) {
	w.p.Send(rawMsg(string(b)))
	return len(b), nil
}
