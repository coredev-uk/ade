// Package stagemove applies the workflow label move a work item is waiting
// for. Under labels.workflow.sync = "ask" (the default) a sync only reports
// that an item's stage moved on and its label did not; this is what makes the
// move, on request: `ade issue stage` and Home's `m`.
package stagemove

import (
	"context"
	"fmt"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/overview"
	"github.com/coredev-uk/ade/internal/startwork"
)

// Actor and Reason label the audit entry of a move a person asked for.
const (
	Actor  = "user"
	Reason = "workflow stage"
)

// Target is a work item and the move it is waiting for.
type Target struct {
	Project  string // ADE project (manifest name)
	Manifest *config.Manifest
	Forge    startwork.Forge
	Item     domain.WorkItem // as the forge says now: labels and workflow re-read
	// Move is the label change, when there is one; Why says what stands in the
	// way when there is none.
	Move startwork.Move
	Has  bool
	Why  string
}

// Resolve finds the cached work item that owns ref, reads its tracker issue
// again (the cache can be a sync behind, and a label somebody just set must not
// be undone) and works out the move. open nil uses startwork.OpenForge.
func Resolve(ctx context.Context, a *app.App, open startwork.OpenFunc, results []forgesync.Result, ref forge.Ref) (*Target, error) {
	if open == nil {
		open = startwork.OpenForge
	}
	res, it, ok := overview.Find(results, ref)
	if !ok {
		return nil, fmt.Errorf("%s is not in the cache: run `ade sync` first", ref)
	}
	m, err := startwork.ServingManifest(ctx, a, res.Project, it.Ref.Project)
	if err != nil {
		return nil, err
	}
	fc, ok := a.Config.Forges[m.Forge]
	if !ok {
		return nil, fmt.Errorf("forge %q %w", m.Forge, startwork.ErrForgeUndefined)
	}
	f, err := open(ctx, m.Forge, fc)
	if err != nil {
		return nil, err
	}
	return ResolveWith(ctx, f, m, it)
}

// ResolveWith is Resolve through an open forge, for the work item it.
func ResolveWith(ctx context.Context, f startwork.Forge, m *config.Manifest, it domain.WorkItem) (*Target, error) {
	live, err := f.Issue(ctx, it.Ref)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", it.Ref, err)
	}
	it.Issue = live
	it.Workflow = domain.FacetsOf(live.Labels, m.Labels).Workflow
	t := &Target{Project: m.Name, Manifest: m, Forge: f, Item: it}
	if t.Move, t.Why = startwork.MoveReason(it, m); t.Why == "" {
		t.Has = true
	}
	return t, nil
}

// Apply makes the move: one audited label update (forge.issue.labels).
func (t *Target) Apply(ctx context.Context, a *app.App) (startwork.Move, error) {
	if !t.Has {
		return startwork.Move{}, fmt.Errorf("%s: nothing to move: %s", t.Item.Ref, t.Why)
	}
	applied, errs := startwork.ApplyMoves(ctx, t.Forge, a, t.Project, Actor, Reason, []startwork.Move{t.Move})
	if len(errs) > 0 {
		return startwork.Move{}, errs[0]
	}
	return applied[0], nil
}
