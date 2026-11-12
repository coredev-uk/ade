package startwork

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/project"
)

// Resolved is a tracker issue in its project: the manifest, the forge client
// and the work item freshly read from the forge (never the cache, so labels
// are current when a write is about to depend on them).
type Resolved struct {
	Project  string // ADE project (manifest name)
	Manifest *config.Manifest
	Forge    Forge
	Item     domain.WorkItem
	Me       forge.User
}

// OpenFunc builds the forge client for the configured forge called name.
type OpenFunc func(ctx context.Context, name string, f config.Forge) (Forge, error)

// OpenForge opens the real driver for a forge config entry.
func OpenForge(ctx context.Context, name string, f config.Forge) (Forge, error) {
	switch f.Kind {
	case "gitlab":
		c, err := gitlab.Open(ctx, name, f)
		if err != nil {
			return nil, err
		}
		return c, nil
	case "github":
		tok, err := f.Token(ctx)
		if err != nil {
			return nil, fmt.Errorf("forge %s: %w", name, err)
		}
		return github.New(f.URL, tok), nil
	}
	return nil, fmt.Errorf("forge %s: unknown kind %q", name, f.Kind)
}

var bareIID = regexp.MustCompile(`^#?(\d+)$`)

// Resolve finds the registered project whose tracker holds the issue named by
// arg ("group/tracker#3", or "3"/"#3" when exactly one project is registered)
// and reads the issue. open nil uses OpenForge.
func Resolve(ctx context.Context, a *app.App, open OpenFunc, arg string) (*Resolved, error) {
	if open == nil {
		open = OpenForge
	}
	if len(a.Config.Projects) == 0 {
		return nil, fmt.Errorf("no projects registered: run `ade project add <manifest>`")
	}
	type candidate struct{ m *config.Manifest }
	var cands []candidate
	for _, ref := range a.Config.Projects {
		m, err := project.CachedManifest(ctx, a, ref)
		if err != nil {
			return nil, fmt.Errorf("manifest %s: %w", ref.Source, err)
		}
		cands = append(cands, candidate{m})
	}
	var ref forge.Ref
	if mt := bareIID.FindStringSubmatch(strings.TrimSpace(arg)); mt != nil {
		if len(cands) != 1 {
			return nil, fmt.Errorf("%q is ambiguous with %d projects registered: use group/project#N", arg, len(cands))
		}
		iid, _ := strconv.Atoi(mt[1])
		ref = forge.Ref{Project: cands[0].m.Tracker.Project, IID: iid, Kind: forge.KindIssue}
	} else {
		var err error
		if ref, err = forge.ParseRef(arg); err != nil {
			return nil, err
		}
		if ref.Kind != forge.KindIssue {
			return nil, fmt.Errorf("%s is a merge request: start work takes a tracker issue", ref)
		}
	}
	for _, c := range cands {
		if c.m.Tracker.Project != ref.Project {
			continue
		}
		return resolveIn(ctx, a, open, c.m, ref)
	}
	return nil, fmt.Errorf("%s is not in the tracker project of any registered project", ref)
}

func resolveIn(ctx context.Context, a *app.App, open OpenFunc, m *config.Manifest, ref forge.Ref) (*Resolved, error) {
	fc, ok := a.Config.Forges[m.Forge]
	if !ok {
		return nil, fmt.Errorf("forge %q is not defined in the user config", m.Forge)
	}
	f, err := open(ctx, m.Forge, fc)
	if err != nil {
		return nil, err
	}
	return resolveWith(ctx, f, m, ref, func(ctx context.Context) (forge.User, error) {
		return CachedMe(ctx, a, m.Forge, f)
	})
}

// ResolveWith reads the tracker issue through an open forge.
func ResolveWith(ctx context.Context, f Forge, m *config.Manifest, ref forge.Ref) (*Resolved, error) {
	return resolveWith(ctx, f, m, ref, f.Me)
}

// resolveWith reads who I am, the issue and its links at once: they do not
// depend on each other, and a dialog waits for the slowest of the three.
func resolveWith(ctx context.Context, f Forge, m *config.Manifest, ref forge.Ref, whoAmI func(context.Context) (forge.User, error)) (*Resolved, error) {
	var (
		me                  forge.User
		is                  forge.Issue
		links               []forge.Link
		meErr, isErr, lnErr error
		wg                  sync.WaitGroup
	)
	wg.Add(3)
	go func() { defer wg.Done(); me, meErr = whoAmI(ctx) }()
	go func() { defer wg.Done(); is, isErr = f.Issue(ctx, ref) }()
	go func() { defer wg.Done(); links, lnErr = f.Links(ctx, ref) }()
	wg.Wait()
	if meErr != nil {
		return nil, fmt.Errorf("who am I: %w", meErr)
	}
	if isErr != nil {
		return nil, fmt.Errorf("%s: %w", ref, isErr)
	}
	if is.State != "opened" {
		return nil, fmt.Errorf("%s is %s: nothing to start", ref, is.State)
	}
	if lnErr != nil {
		return nil, fmt.Errorf("links of %s: %w", ref, lnErr)
	}
	item := domain.Assemble(domain.AssembleInput{
		Project: m.Name, Manifest: m, Me: me.Username, MeID: me.ID, Tracker: is, Links: links, Now: time.Now(),
	})
	return &Resolved{Project: m.Name, Manifest: m, Forge: f, Item: item, Me: me}, nil
}

// PlanFor builds the plan for r from what Discover found: the manifest's
// child body is loaded, the rest is pure. A dialog calls it again after every
// choice (strip a quick action, edit the body) without touching the forge.
func (e *Env) PlanFor(r *Resolved, ch Choices, found map[string]Found) (Plan, error) {
	body, note := LoadChildBody(r.Manifest)
	p, err := BuildPlan(PlanInput{
		Item: r.Item, Manifest: r.Manifest, Choices: ch, Me: r.Me, Root: e.App.Root, Found: found, ChildBody: body,
	})
	if err != nil {
		return Plan{}, err
	}
	if note != "" {
		p.Warnings = append(p.Warnings, note)
	}
	return p, nil
}

// Start runs the whole flow for r with the choices: discover what exists,
// plan, then execute (or report, when dryRun). The plan is returned even when
// execution fails, so a caller can show what was attempted.
func (e *Env) Start(ctx context.Context, r *Resolved, ch Choices, dryRun bool) (Plan, Result, error) {
	found, err := e.Discover(ctx, r.Item, r.Manifest, ch)
	if err != nil {
		return Plan{}, Result{}, err
	}
	p, err := e.PlanFor(r, ch, found)
	if err != nil {
		return Plan{}, Result{}, err
	}
	if dryRun {
		return p, e.DryRun(p), nil
	}
	res, err := e.Execute(ctx, p)
	return p, res, err
}
