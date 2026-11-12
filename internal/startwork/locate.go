package startwork

import (
	"context"
	"errors"
	"fmt"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/project"
)

// Located is an issue of a registered project, read fresh from its forge. Unlike
// Resolved it is any issue, open or closed, of the tracker or of a code repo:
// what classify and weight need, where start work needs an open tracker issue.
type Located struct {
	Project  string // ADE project (manifest name)
	Manifest *config.Manifest
	Forge    Forge
	Issue    forge.Issue
	// Parent is the tracker issue above an issue of a code repo, when it has
	// one that could be read; its facets stand in for the ones the issue lacks.
	Parent *forge.Issue
}

// ErrForgeUndefined means a manifest names a forge the user config lacks.
var ErrForgeUndefined = errors.New("is not defined in the user config")

// ServingManifest returns the manifest of the project named preferred when it
// serves path, else the first registered one that does (see project.Serving).
// Two manifests can both list a repo; the item's own project decides which
// labels and forge apply. preferred "" takes the first.
func ServingManifest(ctx context.Context, a *app.App, preferred, path string) (*config.Manifest, error) {
	if preferred != "" {
		for _, pr := range a.Config.Projects {
			if m, err := project.CachedManifest(ctx, a, pr); err == nil && m.Name == preferred && project.Serves(m, path) {
				return m, nil
			}
		}
	}
	return project.Serving(ctx, a, project.CachedManifest, path)
}

// Locate finds the registered project whose tracker or repos hold the issue
// ref (the one named preferred first, see ServingManifest) and reads it.
// Manifests that fail to load are skipped. open nil uses OpenForge.
func Locate(ctx context.Context, a *app.App, open OpenFunc, preferred string, ref forge.Ref) (*Located, error) {
	if open == nil {
		open = OpenForge
	}
	if ref.Kind != forge.KindIssue {
		return nil, fmt.Errorf("%s is a merge request: want an issue", ref)
	}
	m, err := ServingManifest(ctx, a, preferred, ref.Project)
	if err != nil {
		return nil, err
	}
	fc, ok := a.Config.Forges[m.Forge]
	if !ok {
		return nil, fmt.Errorf("forge %q %w", m.Forge, ErrForgeUndefined)
	}
	f, err := open(ctx, m.Forge, fc)
	if err != nil {
		return nil, err
	}
	return LocateWith(ctx, f, m, ref)
}

// LocateWith reads the issue through an open forge.
func LocateWith(ctx context.Context, f Forge, m *config.Manifest, ref forge.Ref) (*Located, error) {
	is, err := f.Issue(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	l := &Located{Project: m.Name, Manifest: m, Forge: f, Issue: is}
	if ref.Project != m.Tracker.Project {
		// Best effort: without the links the issue is simply read alone.
		links, _ := f.Links(ctx, ref)
		l.Parent = TrackerParent(ctx, f, m, is, links)
	}
	return l, nil
}

// TrackerParent reads the tracker issue above a code-repo issue: the forge's
// hierarchy parent, the one its description names, or a tracker issue it is
// linked to, in that order of trust. links are the issue's own links (nil when
// unknown). It is nil when there is none or it cannot be read; a parent only
// ever adds facets, so a failed read is not worth failing the caller.
func TrackerParent(ctx context.Context, f forge.Forge, m *config.Manifest, is forge.Issue, links []forge.Link) *forge.Issue {
	if m.Tracker.Project == "" || is.Ref.Project == m.Tracker.Project {
		return nil
	}
	var hier map[forge.Ref]forge.Ref
	if pr, ok := f.(forge.Parenter); ok {
		hier, _ = pr.Parents(ctx, []forge.Ref{is.Ref})
	}
	refs := domain.Parents(is, hier)
	for _, l := range links {
		if domain.IsChildLink(l) {
			refs = append(refs, l.Ref)
		}
	}
	for _, r := range refs {
		if r.Project != m.Tracker.Project || r == is.Ref {
			continue
		}
		p, err := f.Issue(ctx, r)
		if err == nil {
			return &p
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
	}
	return nil
}
