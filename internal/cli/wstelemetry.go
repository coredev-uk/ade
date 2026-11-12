package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/workspace"
)

// wsTelemetry is what a workspace command leaves behind besides processes:
// task.* events in the inbox (the dashboard picks them up within seconds) and
// a run record in the owning work item's directory. Both are best effort:
// a full disk must not stop a dev server from starting.
type wsTelemetry struct {
	a       *app.App
	project string
	stop    func()
	rec     *workspace.Recorder // nil: no work item owns the worktree
}

func newWsTelemetry(ctx context.Context, a *app.App, loc workspace.Location, ws string) *wsTelemetry {
	t := &wsTelemetry{a: a, project: loc.Project, stop: func() {}}
	if stop, err := notify.Attach(ctx, a, notify.Channels{}); err != nil {
		fmt.Fprintln(os.Stderr, "ade: notifications:", err)
	} else {
		t.stop = stop
	}
	t.rec = recorderFor(ctx, a, loc, ws)
	return t
}

// recorderFor finds the work item owning the worktree's branch in the sync
// cache. Baseline trees and unknown branches have no item, so no record.
func recorderFor(ctx context.Context, a *app.App, loc workspace.Location, ws string) *workspace.Recorder {
	if loc.Tree == "" { // a project-level run belongs to no work item
		return nil
	}
	st, err := a.Cache()
	if err != nil {
		return nil
	}
	results, err := forgesync.Load(st)
	if err != nil {
		return nil
	}
	var items []workspace.ItemRef
	for _, r := range results {
		for _, it := range r.Items {
			items = append(items, workspace.ItemRef{Project: r.Project, Forge: r.Forge, Item: it})
		}
	}
	branch := workspace.BranchOf(ctx, loc)
	forgeName, tracker, ok := workspace.ItemOf(items, loc.Project, loc.Manifest.Repos[loc.Repo].Path, branch)
	if !ok {
		return nil
	}
	return workspace.NewRecorder(a.RecordsDir, forgeName, tracker, loc, ws, branch)
}

// result publishes the event of one finished node.
func (t *wsTelemetry) result(r workspace.Result) {
	if e, ok := workspace.ResultEvent(t.project, r); ok {
		t.a.Bus.Publish(e)
	}
}

func (t *wsTelemetry) publish(e events.Event) { t.a.Bus.Publish(e) }

// record rewrites the run record.
func (t *wsTelemetry) record(state string, results []workspace.Result) {
	if err := t.rec.Update(state, results); err != nil {
		fmt.Fprintln(os.Stderr, "ade: run record:", err)
	}
}

// close flushes the events to the inbox.
func (t *wsTelemetry) close() { t.stop() }
