package workspace

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/project"
)

// Target is a worktree to open a workspace on, with the work item's own trees
// of the other repos.
type Target struct {
	Project   string
	Repo      string
	Worktree  string
	Title     string            // tab title: the branch, or the repo for a baseline
	ItemTrees map[string]string // repo → the work item's worktree there
	Baseline  bool              // no worktree of the item exists: the repo's baseline tree
}

// repoByPath finds the manifest repo that lives at a forge path.
func repoByPath(m *config.Manifest, path string) (string, bool) {
	for _, name := range m.RepoNames() {
		if strings.EqualFold(m.Repos[name].Path, path) {
			return name, true
		}
	}
	return "", false
}

// OwnsBranch reports whether branch belongs to wi in the repo at repoPath:
// it is the source branch of one of the item's MRs there, or it carries the
// iid of one of the item's child issues there (the default `{child_iid}-{slug}`
// branch convention).
func OwnsBranch(wi domain.WorkItem, repoPath, branch string) bool {
	for _, mr := range wi.MRs {
		if strings.EqualFold(mr.Ref.Project, repoPath) && mr.SourceBranch == branch {
			return true
		}
	}
	for _, c := range wi.Children {
		if strings.EqualFold(c.Ref.Project, repoPath) && strings.HasPrefix(branch, strconv.Itoa(c.Ref.IID)+"-") {
			return true
		}
	}
	return false
}

// worktreesOf lists the branch-bearing worktrees of a repo.
func worktreesOf(ctx context.Context, projectDir, repo string) ([]git.Worktree, error) {
	return git.Client{}.Worktrees(ctx, filepath.Join(projectDir, repo, ".bare"))
}

// ItemRef is a cached work item with the project and forge it came from.
type ItemRef struct {
	Project string
	Forge   string
	Item    domain.WorkItem
}

// ItemOf finds the work item whose child issue or MR owns branch in the repo
// at repoPath, for run records. ok is false when no item claims it.
func ItemOf(items []ItemRef, projectName, repoPath, branch string) (forgeName string, tracker forge.Ref, ok bool) {
	if branch == "" {
		return "", forge.Ref{}, false
	}
	for _, it := range items {
		if it.Project == projectName && OwnsBranch(it.Item, repoPath, branch) {
			return it.Forge, it.Item.Ref, true
		}
	}
	return "", forge.Ref{}, false
}

// BranchOf returns the branch checked out in a worktree ("" when detached or unknown).
func BranchOf(ctx context.Context, loc Location) string {
	trees, err := worktreesOf(ctx, loc.ProjectDir, loc.Repo)
	if err != nil {
		return ""
	}
	for _, t := range trees {
		if filepath.Clean(t.Path) == loc.Tree {
			return t.Branch
		}
	}
	return ""
}

// ItemTrees finds the work item's own worktree in every repo of the project
// that has one (a worktree on a branch the item owns): repo → worktree path and
// repo → branch. Repos that are not cloned have none. When an issue has had
// several MRs, the worktree of an open MR's branch wins over one left from an
// earlier, merged or closed MR.
func ItemTrees(ctx context.Context, root string, m *config.Manifest, wi domain.WorkItem) (trees, branches map[string]string) {
	projectDir := filepath.Join(root, m.Name)
	trees, branches = map[string]string{}, map[string]string{}
	for _, repo := range m.RepoNames() {
		spec := m.Repos[repo]
		wts, err := worktreesOf(ctx, projectDir, repo)
		if err != nil {
			continue // not cloned yet: no tree there
		}
		best := -1
		for _, w := range wts {
			if !OwnsBranch(wi, spec.Path, w.Branch) {
				continue
			}
			if r := branchRank(wi, spec.Path, w.Branch); best < 0 || r < best {
				best, trees[repo], branches[repo] = r, w.Path, w.Branch
			}
		}
	}
	return trees, branches
}

// branchRank orders the item's branches in one repo: the focus MR's branch,
// another open MR's, a branch with no MR yet (fresh work), then branches of
// merged or closed MRs.
func branchRank(wi domain.WorkItem, repoPath, branch string) int {
	if f := wi.FocusMR(); f != nil && strings.EqualFold(f.Ref.Project, repoPath) && f.SourceBranch == branch {
		return 0
	}
	rank := 2
	for _, mr := range wi.MRs {
		if !strings.EqualFold(mr.Ref.Project, repoPath) || mr.SourceBranch != branch {
			continue
		}
		if mr.State == "opened" {
			return 1
		}
		rank = 3
	}
	return rank
}

// TargetForItem picks what `w` opens for a work item: the item's own worktree
// in the first routed repo that has one, else the baseline of the first routed
// repo. Every repo's item worktree is passed along as an item tree.
func TargetForItem(ctx context.Context, root string, m *config.Manifest, wi domain.WorkItem) (Target, error) {
	projectDir := filepath.Join(root, m.Name)
	trees, titles := ItemTrees(ctx, root, m, wi)
	for _, repo := range wi.Repos {
		if p, ok := trees[repo]; ok {
			return Target{Project: m.Name, Repo: repo, Worktree: p, Title: titles[repo], ItemTrees: trees}, nil
		}
	}
	for _, repo := range m.RepoNames() { // an unrouted repo with a tree still beats the baseline
		if p, ok := trees[repo]; ok {
			return Target{Project: m.Name, Repo: repo, Worktree: p, Title: titles[repo], ItemTrees: trees}, nil
		}
	}
	if len(wi.Repos) == 0 {
		return Target{}, fmt.Errorf("no repo for %s · add an app label so it routes to one", wi.Ref)
	}
	repo := wi.Repos[0]
	base, err := baseline(ctx, projectDir, m, repo)
	if err != nil {
		return Target{}, err
	}
	return Target{Project: m.Name, Repo: repo, Worktree: base, Title: filepath.Base(base), Baseline: true}, nil
}

// TargetForMR picks the worktree of an MR's source branch, else its repo's baseline.
func TargetForMR(ctx context.Context, root string, m *config.Manifest, mr forge.MR) (Target, error) {
	repo, ok := repoByPath(m, mr.Ref.Project)
	if !ok {
		return Target{}, fmt.Errorf("%s is not in a repo of project %s", mr.Ref, m.Name)
	}
	projectDir := filepath.Join(root, m.Name)
	if wts, err := worktreesOf(ctx, projectDir, repo); err == nil {
		for _, w := range wts {
			if w.Branch != "" && w.Branch == mr.SourceBranch {
				return Target{Project: m.Name, Repo: repo, Worktree: w.Path, Title: w.Branch}, nil
			}
		}
	}
	base, err := baseline(ctx, projectDir, m, repo)
	if err != nil {
		return Target{}, err
	}
	return Target{Project: m.Name, Repo: repo, Worktree: base, Title: filepath.Base(base), Baseline: true}, nil
}

// BaselineDir is the path of repo's baseline worktree, named after its default
// branch (the manifest's default_branch, else the bare clone's HEAD).
func BaselineDir(ctx context.Context, projectDir string, m *config.Manifest, repo string) (string, error) {
	return baseline(ctx, projectDir, m, repo)
}

// baseline is a repo's baseline worktree: <repo>/<default branch>.
func baseline(ctx context.Context, projectDir string, m *config.Manifest, repo string) (string, error) {
	r := &treeResolver{loc: Location{ProjectDir: projectDir, Manifest: m}}
	t, err := r.baseline(ctx, repo, filepath.Join(projectDir, repo), m.Repos[repo])
	if err != nil {
		return "", err
	}
	return t.Path, nil
}

// ItemFor finds the cached work item whose child issue or MR owns the branch
// checked out in loc's worktree. ok is false for baseline trees and branches
// no item claims.
func ItemFor(ctx context.Context, items []ItemRef, loc Location) (ItemRef, bool) {
	branch := BranchOf(ctx, loc)
	if branch == "" {
		return ItemRef{}, false
	}
	repoPath := loc.Manifest.Repos[loc.Repo].Path
	for _, it := range items {
		if it.Project == loc.Project && OwnsBranch(it.Item, repoPath, branch) {
			return it, true
		}
	}
	return ItemRef{}, false
}

// ItemWorktree is the work item's own worktree in one repo.
type ItemWorktree struct {
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	Primary bool   `json:"primary"`
}

// ItemWorktrees lists the work item's worktrees, the primary one first: the
// repos the item routes to in their order, then the other repos by name, the
// order TargetForItem opens in. Repos without a worktree are left out.
func ItemWorktrees(ctx context.Context, root string, m *config.Manifest, wi domain.WorkItem) []ItemWorktree {
	trees, branches := ItemTrees(ctx, root, m, wi)
	var out []ItemWorktree
	for _, repo := range append(slices.Clone(wi.Repos), m.RepoNames()...) {
		if p, ok := trees[repo]; ok && !slices.ContainsFunc(out, func(w ItemWorktree) bool { return w.Repo == repo }) {
			out = append(out, ItemWorktree{Repo: repo, Path: p, Branch: branches[repo], Primary: len(out) == 0})
		}
	}
	return out
}

// RepoBaseline is a repo's baseline worktree (the default branch's tree) and
// the branch it is on.
func RepoBaseline(ctx context.Context, root string, m *config.Manifest, repo string) (ItemWorktree, error) {
	projectDir := filepath.Join(root, m.Name)
	path, err := baseline(ctx, projectDir, m, repo)
	if err != nil {
		return ItemWorktree{}, err
	}
	w := ItemWorktree{Repo: repo, Path: path, Branch: filepath.Base(path), Primary: true}
	if wts, err := worktreesOf(ctx, projectDir, repo); err == nil {
		for _, t := range wts {
			if filepath.Clean(t.Path) == filepath.Clean(path) && t.Branch != "" {
				w.Branch = t.Branch
			}
		}
	}
	return w, nil
}

// FindRepo resolves a repo named by arg among the projects cloned under root:
// "<repo>" when exactly one project has it, "<project>/<repo>" for any. The
// error says what the choices are.
func FindRepo(root, arg string) (*config.Manifest, string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, "", fmt.Errorf("projects root: %w", err)
	}
	manifests := map[string]*config.Manifest{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if m, err := project.LoadManifest(filepath.Join(root, e.Name())); err == nil {
			manifests[e.Name()] = m
		}
	}
	if proj, repo, ok := strings.Cut(arg, "/"); ok {
		m, have := manifests[proj]
		if !have {
			return nil, "", fmt.Errorf("no project %q under %s", proj, root)
		}
		if _, ok := m.Repos[repo]; !ok {
			return nil, "", fmt.Errorf("project %s has no repo %q (repos: %s)", proj, repo, strings.Join(m.RepoNames(), ", "))
		}
		return m, repo, nil
	}
	var found []string
	var all []string
	for _, name := range slices.Sorted(maps.Keys(manifests)) {
		for _, repo := range manifests[name].RepoNames() {
			all = append(all, name+"/"+repo)
			if repo == arg {
				found = append(found, name)
			}
		}
	}
	switch len(found) {
	case 0:
		return nil, "", fmt.Errorf("%q is neither a work item ref nor a repo (repos: %s)", arg, strings.Join(all, ", "))
	case 1:
		return manifests[found[0]], arg, nil
	}
	for i, name := range found {
		found[i] = name + "/" + arg
	}
	return nil, "", fmt.Errorf("repo %q is in several projects: use %s", arg, strings.Join(found, " or "))
}
