package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/overview"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/workspace"
)

// itemWorktree is one worktree of a work item in `ade items --json` and
// `ade item`: where it is and what `ade project status` says about it.
type itemWorktree struct {
	Repo        string `json:"repo"`
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	Dirty       bool   `json:"dirty"`
	HasUpstream bool   `json:"has_upstream"`
	Ahead       int    `json:"ahead"`
	Behind      int    `json:"behind"`
}

// itemWithTrees is a cached work item with its worktrees on disk. They are not
// part of domain.WorkItem, which is what the cache stores; embedding keeps the
// JSON flat. An item without a worktree has "worktrees": [].
type itemWithTrees struct {
	domain.WorkItem
	Worktrees []itemWorktree `json:"worktrees"`
}

// treeFinder maps work items to their worktrees. It lists each repo's
// worktrees once however many items ask, and reads disk only.
type treeFinder struct {
	root      string
	manifests map[string]*config.Manifest // nil: project not cloned
	listed    map[string][]git.Worktree   // <project>/<repo>
	status    map[string]project.TreeStatus
}

func newTreeFinder(root string) *treeFinder {
	return &treeFinder{
		root:      root,
		manifests: map[string]*config.Manifest{},
		listed:    map[string][]git.Worktree{},
		status:    map[string]project.TreeStatus{},
	}
}

// of returns the worktrees on branches wi owns, in manifest repo order.
func (f *treeFinder) of(ctx context.Context, wi domain.WorkItem) []itemWorktree {
	out := []itemWorktree{}
	m := f.manifest(wi.Project)
	if m == nil {
		return out
	}
	for _, repo := range m.RepoNames() {
		for _, w := range f.worktrees(ctx, m, repo) {
			if !workspace.OwnsBranch(wi, m.Repos[repo].Path, w.Branch) {
				continue
			}
			ts, ok := f.status[w.Path]
			if !ok {
				ts = project.StatusOf(ctx, w)
				f.status[w.Path] = ts
			}
			out = append(out, itemWorktree{
				Repo: repo, Path: w.Path, Branch: w.Branch,
				Dirty: ts.Dirty, HasUpstream: ts.HasUpstream, Ahead: ts.Ahead, Behind: ts.Behind,
			})
		}
	}
	return out
}

func (f *treeFinder) manifest(name string) *config.Manifest {
	m, ok := f.manifests[name]
	if !ok {
		m, _ = project.LoadManifest(filepath.Join(f.root, name))
		f.manifests[name] = m
	}
	return m
}

func (f *treeFinder) worktrees(ctx context.Context, m *config.Manifest, repo string) []git.Worktree {
	key := m.Name + "/" + repo
	trees, ok := f.listed[key]
	if !ok {
		trees, _ = git.Client{}.Worktrees(ctx, filepath.Join(f.root, m.Name, repo, ".bare")) // not cloned: none
		f.listed[key] = trees
	}
	return trees
}

// worktreesOfRef finds the work item that owns ref in the cache and lists its
// worktrees. A missing cache or item gives none: `ade item` works from the
// record alone.
func worktreesOfRef(ctx context.Context, root string, results []forgesync.Result, ref forge.Ref) []itemWorktree {
	_, wi, ok := overview.Find(results, ref)
	if !ok {
		return []itemWorktree{}
	}
	return newTreeFinder(root).of(ctx, wi)
}

func printItemWorktrees(w io.Writer, trees []itemWorktree) {
	if len(trees) == 0 {
		return
	}
	fmt.Fprintln(w, "worktrees:")
	for _, t := range trees {
		fmt.Fprintf(w, "  %s  %s  %s  %s\n", t.Repo, t.Branch, t.Path, treeState(t))
	}
}

// treeState is "clean, 2 ahead" or "dirty, no upstream".
func treeState(t itemWorktree) string {
	parts := []string{"clean"}
	if t.Dirty {
		parts[0] = "dirty"
	}
	switch {
	case !t.HasUpstream:
		parts = append(parts, "no upstream")
	case t.Ahead == 0 && t.Behind == 0:
		parts = append(parts, "in sync")
	default:
		if t.Ahead > 0 {
			parts = append(parts, fmt.Sprintf("%d ahead", t.Ahead))
		}
		if t.Behind > 0 {
			parts = append(parts, fmt.Sprintf("%d behind", t.Behind))
		}
	}
	return strings.Join(parts, ", ")
}
