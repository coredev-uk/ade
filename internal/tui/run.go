// Package tui is the ADE dashboard (Interchange direction; see DESIGN.md).
package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/review"
	"github.com/coredev-uk/ade/internal/watch"
)

// Run shows the dashboard until the user quits. It owns the update (git) and
// sync (forge) loops and the notifier for this process: events become
// pop-ups and inbox rows, shown only inside the dashboard.
func Run(ctx context.Context, a *app.App) error {
	cache, err := a.Cache()
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	svc := project.New(a, "ade:update")
	m := model{
		ctx: ctx, app: a, svc: svc, sync: forgesync.New(a), cache: cache,
		nc: newNotifCenter(notify.NewInbox(cache)), g: glyphSet(a.Config.Host.Glyphs),
		reviews: review.NewStore(cache), be: &mrBackends{},
	}
	ctx, cancel := context.WithCancel(ctx) // stops the file watchers
	defer cancel()
	if m.fw, err = newFileWatch(ctx, a, cache.Path, watch.Options{}); err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	m.ctx = ctx
	p := tea.NewProgram(m, tea.WithContext(ctx))

	svc.OnProgress = func(pr project.Progress) { p.Send(progressMsg(pr)) }
	stop, err := notify.Attach(ctx, a, notify.Channels{
		Toast: func(e events.Event) { p.Send(eventMsg(e)) },
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
