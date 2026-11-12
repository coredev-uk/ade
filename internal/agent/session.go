package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/review"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/workspace"
)

// ForApp wires a Manager to the app: cache.db for the index and drafts, the
// item records for run directories, the AI service for the pre-flight, and
// the project service's git credentials for the push.
func ForApp(a *app.App) (*Manager, error) {
	st, err := a.Cache()
	if err != nil {
		return nil, fmt.Errorf("open cache: %w", err)
	}
	m := NewManager(a.Config, NewStore(st, records.New(a.RecordsDir), a.RecordsDir), a.Audit, a.Bus)
	if svc, err := ai.NewService(a); err == nil {
		m.AI = svc
	}
	m.Reviews = review.NewStore(st)
	psvc := project.New(a, "user")
	m.GitFor = func(ctx context.Context, r Run) (git.Client, error) {
		return psvc.GitClient(ctx, r.Forge, filepath.Join(a.Root, r.Project, r.Repo, ".bare"))
	}
	return m, nil
}

// ResolveTarget finds the worktree start work made for a tracker issue: the
// repo the app labels route to (or repo), its branch, child issue and MR, and
// the trees of repos it requires, which the agent may read.
func ResolveTarget(ctx context.Context, a *app.App, open startwork.OpenFunc, refArg, repo string) (Target, forge.Forge, error) {
	r, err := startwork.Resolve(ctx, a, open, refArg)
	if err != nil {
		return Target{}, nil, err
	}
	ch := startwork.DefaultChoices(r.Manifest, r.Item)
	repos := ch.Repos
	if repo != "" {
		repos = []string{repo}
	}
	if len(repos) != 1 {
		return Target{}, nil, fmt.Errorf("%s routes to %d repos (%s): pick one with --repo", r.Item.Ref, len(repos), strings.Join(repos, ", "))
	}
	repo = repos[0]
	required := startwork.RequiredRepos(r.Manifest, repos)
	ch.Repos = append(append([]string{}, repos...), required...)
	env := &startwork.Env{App: a, Forge: r.Forge, Git: project.New(a, "user").GitClient}
	found, err := env.Discover(ctx, r.Item, r.Manifest, ch)
	if err != nil {
		return Target{}, nil, err
	}
	f := found[repo]
	if f.Worktree == "" || f.Branch == "" {
		return Target{}, nil, fmt.Errorf("%s has no worktree for %s: `ade start %s` creates it", r.Item.Ref, repo, refArg)
	}
	t := Target{Forge: r.Manifest.Forge, Project: r.Project, Tracker: r.Item.Ref, Repo: repo, Worktree: f.Worktree, Branch: f.Branch}
	if f.Child != nil {
		c := f.Child.Ref
		t.Child = &c
	}
	if f.MR != nil {
		mr := f.MR.Ref
		t.MR = &mr
	}
	for _, req := range required {
		if w := found[req].Worktree; w != "" {
			t.AddDirs = append(t.AddDirs, w)
		} else if base, err := workspace.BaselineDir(ctx, filepath.Join(a.Root, r.Project), r.Manifest, req); err == nil {
			t.AddDirs = append(t.AddDirs, base)
		}
	}
	return t, r.Forge, nil
}
