package review

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/project"
)

// Backend is a forge driver that can read and write merge requests.
type Backend interface {
	forge.Forge
	forge.MRWriter
}

// Dial opens the driver for the configured forge called name. Tests replace it
// to point at a fake server.
var Dial = func(ctx context.Context, name string, f config.Forge) (Backend, error) {
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

// ForgeFor names the configured forge that serves the MR's project. An
// explicit override wins; otherwise the registered projects are searched for
// one whose tracker or repos are that project (or, failing that, share its
// group), and a single configured forge is the last resort.
func ForgeFor(ctx context.Context, a *app.App, ref forge.Ref, override string) (string, error) {
	forges := a.Config.Forges
	if override != "" {
		if _, ok := forges[override]; !ok {
			return "", fmt.Errorf("forge %q is not defined in the user config (have: %s)", override, joinKeys(forges))
		}
		return override, nil
	}
	exact, group := map[string]bool{}, map[string]bool{}
	for _, pr := range a.Config.Projects {
		m, err := project.FetchManifest(ctx, a, pr)
		if err != nil {
			continue // an unreachable manifest must not block the others
		}
		paths := []string{m.Tracker.Project}
		for _, r := range m.Repos {
			paths = append(paths, r.Path)
		}
		for _, p := range paths {
			switch {
			case p == "":
			case p == ref.Project:
				exact[m.Forge] = true
			case forge.Group(p) == forge.Group(ref.Project):
				group[m.Forge] = true
			}
		}
	}
	for _, set := range []map[string]bool{exact, group} {
		switch names := slices.Sorted(maps.Keys(set)); len(names) {
		case 0:
		case 1:
			if _, ok := forges[names[0]]; ok {
				return names[0], nil
			}
			return "", fmt.Errorf("forge %q (from the manifest serving %s) is not defined in the user config", names[0], ref.Project)
		default:
			return "", fmt.Errorf("%s is served by several forges (%s): pass --forge", ref.Project, strings.Join(names, ", "))
		}
	}
	if len(forges) == 1 {
		for name := range forges {
			return name, nil
		}
	}
	return "", fmt.Errorf("no registered project serves %s: pass --forge (have: %s)", ref.Project, joinKeys(forges))
}

// OpenBackend resolves the forge for ref (see ForgeFor) and opens its driver.
func OpenBackend(ctx context.Context, a *app.App, ref forge.Ref, override string) (Backend, error) {
	name, err := ForgeFor(ctx, a, ref, override)
	if err != nil {
		return nil, err
	}
	return Dial(ctx, name, a.Config.Forges[name])
}

func joinKeys(m map[string]config.Forge) string {
	if len(m) == 0 {
		return "none"
	}
	return strings.Join(slices.Sorted(maps.Keys(m)), ", ")
}
