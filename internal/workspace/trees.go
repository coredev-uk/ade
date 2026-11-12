package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/project"
)

// Tree sources, in the order PLAN 2.1 resolves them.
const (
	SourceSelf     = "self"     // the worktree the workspace was opened for
	SourceItem     = "item"     // the same work item's own worktree in that repo
	SourcePin      = "pin"      // explicit `--with repo=branch`
	SourceBaseline = "baseline" // the repo's baseline worktree (named after its default branch)
)

// Tree is the worktree a repo resolves to for one workspace.
type Tree struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Source string `json:"source"`
}

// Location says where a worktree sits in <root>/<project>/<repo>/<tree>.
type Location struct {
	Root       string
	Project    string
	ProjectDir string
	Repo       string
	RepoDir    string
	Tree       string // worktree root
	Manifest   *config.Manifest
}

// Locate finds the project and repo owning worktree and loads the project's
// manifest snapshot. A path inside a worktree resolves to the worktree root.
func Locate(root, worktree string) (Location, error) {
	absRoot, err := resolve(root)
	if err != nil {
		return Location{}, fmt.Errorf("projects root: %w", err)
	}
	abs, err := resolve(worktree)
	if err != nil {
		return Location{}, fmt.Errorf("worktree: %w", err)
	}
	rel, err := filepath.Rel(absRoot, abs)
	parts := strings.Split(rel, string(filepath.Separator))
	if err != nil || strings.HasPrefix(rel, "..") || len(parts) < 3 {
		return Location{}, fmt.Errorf("%s is not a worktree under %s (want <project>/<repo>/<worktree>)", worktree, absRoot)
	}
	loc := Location{Root: absRoot, Project: parts[0], Repo: parts[1]}
	loc.ProjectDir = filepath.Join(absRoot, parts[0])
	loc.RepoDir = filepath.Join(loc.ProjectDir, parts[1])
	loc.Tree = filepath.Join(loc.RepoDir, parts[2])
	m, err := project.LoadManifest(loc.ProjectDir)
	if err != nil {
		return Location{}, fmt.Errorf("project %s: %w (run `ade project add`)", loc.Project, err)
	}
	if _, ok := m.Repos[loc.Repo]; !ok {
		return Location{}, fmt.Errorf("%s is not a repo of project %s", loc.Repo, loc.Project)
	}
	loc.Manifest = m
	return loc, nil
}

func resolve(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// TreeOptions steers tree resolution for required repos.
type TreeOptions struct {
	// Pins maps a repo to a branch name or worktree path (`--with api=branch`).
	Pins map[string]string
	// ItemTrees maps a repo to the work item's own worktree there, when it has one.
	ItemTrees map[string]string
}

// treeResolver caches resolved trees for one location.
type treeResolver struct {
	loc  Location
	opts TreeOptions
	git  git.Client
	done map[string]Tree
}

func newTreeResolver(loc Location, o TreeOptions) *treeResolver {
	return &treeResolver{loc: loc, opts: o, done: map[string]Tree{loc.Repo: {Repo: loc.Repo, Path: loc.Tree, Source: SourceSelf}}}
}

// tree resolves repo: self, then a pin, then the work item's tree, then the
// baseline. (PLAN lists the item's tree first; an explicit pin is the user
// overriding it, so it must win.)
func (r *treeResolver) tree(ctx context.Context, repo string) (Tree, error) {
	if t, ok := r.done[repo]; ok {
		return t, nil
	}
	spec, ok := r.loc.Manifest.Repos[repo]
	if !ok {
		return Tree{}, fmt.Errorf("unknown repo %q", repo)
	}
	repoDir := filepath.Join(r.loc.ProjectDir, repo)
	var (
		t   Tree
		err error
	)
	switch {
	case r.opts.Pins[repo] != "":
		t, err = r.pinned(ctx, repo, repoDir, r.opts.Pins[repo])
	case r.opts.ItemTrees[repo] != "":
		t, err = r.owned(repo, repoDir, r.opts.ItemTrees[repo], SourceItem)
	default:
		t, err = r.baseline(ctx, repo, repoDir, spec)
	}
	if err != nil {
		return Tree{}, err
	}
	r.done[repo] = t
	return t, nil
}

// owned validates that path is a worktree of repoDir.
func (r *treeResolver) owned(repo, repoDir, path, source string) (Tree, error) {
	abs, err := resolve(path)
	if err != nil {
		return Tree{}, fmt.Errorf("%s tree %s: %w", repo, path, err)
	}
	rel, err := filepath.Rel(repoDir, abs)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return Tree{}, fmt.Errorf("%s is not a worktree of repo %s", path, repo)
	}
	return Tree{Repo: repo, Path: filepath.Join(repoDir, strings.Split(rel, string(filepath.Separator))[0]), Source: source}, nil
}

func (r *treeResolver) pinned(ctx context.Context, repo, repoDir, pin string) (Tree, error) {
	if filepath.IsAbs(pin) {
		return r.owned(repo, repoDir, pin, SourcePin)
	}
	trees, err := r.git.Worktrees(ctx, filepath.Join(repoDir, ".bare"))
	if err != nil {
		return Tree{}, fmt.Errorf("list %s worktrees: %w", repo, err)
	}
	var have []string
	for _, w := range trees {
		if w.Branch == pin {
			return Tree{Repo: repo, Path: w.Path, Source: SourcePin}, nil
		}
		if w.Branch != "" {
			have = append(have, w.Branch)
		}
	}
	slices.Sort(have)
	return Tree{}, fmt.Errorf("--with %s=%s: no worktree on that branch (have: %s)", repo, pin, strings.Join(have, ", "))
}

func (r *treeResolver) baseline(ctx context.Context, repo, repoDir string, spec config.Repo) (Tree, error) {
	def := spec.DefaultBranch
	if def == "" {
		var err error
		if def, err = r.git.DefaultBranch(ctx, filepath.Join(repoDir, ".bare")); err != nil {
			return Tree{}, fmt.Errorf("%s: default branch: %w", repo, err)
		}
	}
	dir := filepath.Join(repoDir, git.BranchDir(def))
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return Tree{}, fmt.Errorf("baseline tree of %s is missing (%s): run `ade project add`", repo, dir)
	}
	return Tree{Repo: repo, Path: dir, Source: SourceBaseline}, nil
}

// CheckoutClosure returns repo plus every repo it transitively requires, sorted.
func CheckoutClosure(m *config.Manifest, repo string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(r string) {
		if seen[r] {
			return
		}
		seen[r] = true
		for _, req := range m.Repos[r].Requires {
			walk(req)
		}
	}
	walk(repo)
	return slices.Sorted(func(yield func(string) bool) {
		for r := range seen {
			if !yield(r) {
				return
			}
		}
	})
}
