// Package forgesync pulls each registered project's work from its forge,
// assembles domain work items, caches them for instant rendering, writes item
// records and raises events for what changed since the previous sync.
package forgesync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/store"
)

// Actor is the audit actor of a sync run.
const Actor = "ade:sync"

// Result is one project's synced state.
type Result struct {
	Project string          `json:"project"` // ADE project (manifest name)
	Forge   string          `json:"forge"`   // key into Config.Forges
	Tracker string          `json:"tracker"` // tracker project path
	Me      forge.User      `json:"me"`
	Caps    forge.Caps      `json:"caps"`
	Sprints []domain.Sprint `json:"sprints,omitempty"`
	Window  domain.Window   `json:"window"`
	// Items are the tracker-rooted work items assigned to me or in the
	// current/adjacent sprints, plus untracked issues assigned to me.
	Items []domain.WorkItem `json:"items,omitempty"`
	// Reviews are open MRs requesting my review across the manifest's repos.
	Reviews []forge.MR `json:"reviews,omitempty"`
	// ReviewFacets are the type, apps and weight of the issue a review
	// worked on, by MR ref, for the reviews whose issue is loaded.
	ReviewFacets map[string]ReviewFacets `json:"review_facets,omitempty"`
	SyncedAt     time.Time               `json:"synced_at"`
	// Warnings are non-fatal problems (e.g. a record that could not be written).
	Warnings []string `json:"warnings,omitempty"`
	Err      string   `json:"err,omitempty"`
}

// ReviewFacets are the facets of the issue behind a review request, taken
// from its tracker parent where the issue has none of its own.
type ReviewFacets struct {
	Type   string   `json:"type,omitempty"`
	Apps   []string `json:"apps,omitempty"`
	Weight *int     `json:"weight,omitempty"`
}

// Service syncs every registered project.
type Service struct {
	App *app.App
	// Open builds the forge client for the config forge called name.
	Open func(ctx context.Context, name string, f config.Forge) (forge.Forge, error)
	Now  func() time.Time
}

// New returns a Service using real forge drivers and the wall clock.
func New(a *app.App) *Service {
	return &Service{App: a, Open: openForge, Now: time.Now}
}

func openForge(ctx context.Context, name string, f config.Forge) (forge.Forge, error) {
	switch f.Kind {
	case "gitlab":
		c, err := gitlab.Open(ctx, name, f)
		if err != nil {
			return nil, err
		}
		return c, nil
	case "github":
		c, err := github.Open(ctx, name, f)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	return nil, fmt.Errorf("forge %s: unknown kind %q", name, f.Kind)
}

// Sync refreshes every registered project. A failing project is reported in
// its Result.Err and leaves its cached items untouched; the others carry on.
// The returned error is reserved for problems that stop the whole run.
func (s *Service) Sync(ctx context.Context) ([]Result, error) {
	// A sync is background work: against a forge that reports its rate-limit
	// budget it stops short of the end, keeping the rest for dialogs.
	ctx = forge.Background(ctx)
	st, err := s.App.Cache()
	if err != nil {
		return nil, fmt.Errorf("open cache: %w", err)
	}
	cached, err := Load(st)
	if err != nil {
		return nil, err
	}
	prevByProject := map[string]Result{}
	for _, r := range cached {
		prevByProject[r.Project] = r
	}

	var results []Result
	var evs []events.Event
	for _, ref := range s.App.Config.Projects {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		res := s.syncProject(ctx, st, ref, prevByProject)
		results = append(results, res.Result)
		evs = append(evs, res.events...)
	}
	for _, e := range evs {
		s.App.Bus.Publish(e)
	}
	s.App.Bus.Publish(summary(results, prevByProject))
	return results, s.audit(results)
}

type projectOutcome struct {
	Result
	events []events.Event
}

func (s *Service) syncProject(ctx context.Context, st *store.Store, ref config.ProjectRef, prev map[string]Result) projectOutcome {
	m, err := project.FetchManifest(ctx, s.App, ref)
	if err != nil {
		return projectOutcome{Result: Result{Project: ref.Source, Err: err.Error(), SyncedAt: s.Now()}}
	}
	res := Result{Project: m.Name, Forge: m.Forge, Tracker: m.Tracker.Project, SyncedAt: s.Now()}
	old, hadPrev := prev[m.Name]
	fail := func(err error) projectOutcome {
		res.Err = err.Error()
		if saveErr := saveFailure(st, old, hadPrev, res); saveErr != nil {
			res.Err += "; " + saveErr.Error()
		}
		return projectOutcome{Result: res}
	}

	f, err := s.openFor(ctx, m)
	if err != nil {
		return fail(err)
	}
	p := &pipeline{f: f, st: st, m: m, cfg: s.App.Config, forgeName: m.Forge, now: s.Now()}
	if hadPrev {
		p.prev = old
	}
	if err := p.run(ctx, &res); err != nil {
		return fail(err)
	}
	// Move workflow labels to match stage changes since the last good sync
	// (labels.workflow.sync); failures are warnings, never fatal to the sync.
	if w, ok := f.(forge.IssueWriter); ok && hadPrev && (old.Err == "" || len(old.Items) > 0) {
		o := startwork.SyncWorkflow(ctx, w, s.App, m, old.Items, res.Items)
		for _, err := range o.Errs {
			res.Warnings = append(res.Warnings, "workflow label: "+err.Error())
		}
		for _, mv := range o.Pending {
			res.Warnings = append(res.Warnings, "workflow label not moved: "+mv.String()+": "+startwork.PendingAdvice(mv, m.Labels))
		}
	}
	res.Warnings = append(res.Warnings, s.writeRecords(ctx, f, m, res, old, hadPrev)...)
	// Features the instance lacks (an older self-managed GitLab) are named
	// once per sync, never per item, and never fail it.
	if u, ok := f.(interface{ Unavailable() []string }); ok {
		for _, name := range u.Unavailable() {
			res.Warnings = append(res.Warnings, "not available on this GitLab: "+name)
		}
	}
	if err := saveResult(st, res); err != nil {
		return fail(err)
	}
	out := projectOutcome{Result: res}
	// Only a good previous sync is a baseline: a project whose first syncs
	// failed has nothing cached, and everything in it would look new.
	if hadPrev && (old.Err == "" || len(old.Items) > 0) {
		out.events = diff(old, res)
		out.events = append(out.events, removableEvents(ctx, project.New(s.App, Actor), res.Project, mergedMRs(old, res))...)
	}
	return out
}

// openFor resolves the forge a manifest names and opens its client.
func (s *Service) openFor(ctx context.Context, m *config.Manifest) (forge.Forge, error) {
	if m.Tracker.Project == "" {
		return nil, errors.New("manifest has no tracker.project")
	}
	fc, ok := s.App.Config.Forges[m.Forge]
	if !ok {
		return nil, fmt.Errorf("forge %q is not defined in the user config", m.Forge)
	}
	return s.Open(ctx, m.Forge, fc)
}

func (s *Service) writeRecords(ctx context.Context, f forge.Forge, m *config.Manifest, res Result, old Result, hadPrev bool) []string {
	rs := records.New(s.App.RecordsDir)
	prevUpdated := map[string]time.Time{}
	if hadPrev {
		for _, wi := range old.Items {
			prevUpdated[wi.Ref.String()] = wi.Issue.UpdatedAt
		}
	}
	var warns []string
	for _, wi := range res.Items {
		if at, ok := prevUpdated[wi.Ref.String()]; ok && at.Equal(wi.Issue.UpdatedAt) {
			continue
		}
		var notes []forge.Note
		// A listing that says "no human comments" needs no discussion fetch.
		if c := wi.Issue.Comments; c == nil || *c > 0 {
			var err error
			if notes, err = issueNotes(ctx, f, wi.Ref); err != nil {
				warns = append(warns, fmt.Sprintf("%s: notes: %v", wi.Ref, err))
			}
		}
		if _, _, err := rs.WriteSnapshot(m.Forge, wi, notes, s.App.Config.Records.SnapshotComments); err != nil {
			warns = append(warns, fmt.Sprintf("%s: record: %v", wi.Ref, err))
		}
	}
	return warns
}

func issueNotes(ctx context.Context, f forge.Forge, ref forge.Ref) ([]forge.Note, error) {
	ds, err := f.Discussions(ctx, ref)
	if err != nil {
		return nil, err
	}
	var notes []forge.Note
	for _, d := range ds {
		notes = append(notes, d.Notes...)
	}
	return notes, nil
}

func (s *Service) audit(results []Result) error {
	items, failed := 0, 0
	for _, r := range results {
		items += len(r.Items)
		if r.Err != "" {
			failed++
		}
	}
	e := audit.Entry{
		Actor:  Actor,
		Action: "sync.run",
		Params: map[string]any{"projects": len(results), "items": items, "failed": failed},
	}
	if failed > 0 {
		e.Outcome, e.Error = "error", fmt.Sprintf("%d of %d projects failed", failed, len(results))
	}
	if err := s.App.Audit.Append(e); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}
