package project

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
)

// ManifestLoader reads the manifest of a registered project: FetchManifest,
// or CachedManifest where the caller must not wait on the network.
type ManifestLoader func(ctx context.Context, a *app.App, ref config.ProjectRef) (*config.Manifest, error)

// Serving returns the manifest of the first registered project whose tracker
// or one of whose repos is the project at path. A manifest that fails to load
// is skipped, so one stale source does not hide the others; when nothing
// serves path the error says which manifests could not be read.
func Serving(ctx context.Context, a *app.App, load ManifestLoader, path string) (*config.Manifest, error) {
	if len(a.Config.Projects) == 0 {
		return nil, errors.New("no projects registered: run `ade project add <manifest>`")
	}
	var failed []error
	for _, pr := range a.Config.Projects {
		m, err := load(ctx, a, pr)
		if err != nil {
			failed = append(failed, fmt.Errorf("manifest %s: %w", pr.Source, err))
			continue
		}
		if m.Tracker.Project == path || slices.ContainsFunc(slices.Collect(maps.Values(m.Repos)), func(r config.Repo) bool { return r.Path == path }) {
			return m, nil
		}
	}
	err := fmt.Errorf("no registered project serves %s", path)
	if len(failed) > 0 {
		err = fmt.Errorf("%w (%d manifest(s) could not be read; first: %w)", err, len(failed), failed[0])
	}
	return nil, err
}
