package project

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/git"
)

// Info is one registered project as `ade project list` shows it.
type Info struct {
	Source     string    `json:"source"`
	Name       string    `json:"name,omitempty"` // empty until first cloned
	Dir        string    `json:"dir,omitempty"`
	Repos      int       `json:"repos"`
	LastUpdate time.Time `json:"last_update,omitzero"`
}

// TreeStatus is the state of one worktree.
type TreeStatus struct {
	Name        string `json:"name"` // directory name
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	Dirty       bool   `json:"dirty"`                  // also true when git could not tell
	StatusError string `json:"status_error,omitempty"` // why git could not tell
	HasUpstream bool   `json:"has_upstream"`
	Ahead       int    `json:"ahead"`
	Behind      int    `json:"behind"`
}

// RepoStatus is the state of one repo directory.
type RepoStatus struct {
	Name          string       `json:"name"`
	Present       bool         `json:"present"`
	InManifest    bool         `json:"in_manifest"` // false: on disk, dropped from the manifest
	DefaultBranch string       `json:"default_branch,omitempty"`
	Baseline      *TreeStatus  `json:"baseline,omitempty"`
	Worktrees     []TreeStatus `json:"worktrees,omitempty"`
	Groups        []string     `json:"groups,omitempty"` // [repo_groups] the repo is in, sorted
	Error         string       `json:"error,omitempty"`
}

// ProjectStatus is the state of one project on disk.
type ProjectStatus struct {
	Name       string       `json:"name"`
	Source     string       `json:"source"`
	Dir        string       `json:"dir"`
	LastUpdate time.Time    `json:"last_update,omitzero"`
	Repos      []RepoStatus `json:"repos"`
	// RepoGroups are the manifest's display-only [repo_groups], sorted by name.
	RepoGroups []GroupStatus `json:"repo_groups,omitempty"`
	// Setup is the project-wide part (`[project]`); nil when the manifest sets none.
	Setup *SetupStatus `json:"setup,omitempty"`
}

// GroupStatus is one display-only repo group and its workspace file.
type GroupStatus struct {
	Name      string   `json:"name"`
	Repos     []string `json:"repos"`
	Workspace string   `json:"workspace,omitempty"` // file stem, as written in the manifest
	File      string   `json:"file,omitempty"`
	Written   bool     `json:"written,omitempty"` // File exists and ADE generated it
}

// SetupStatus is the state of the project-wide setup on disk.
type SetupStatus struct {
	Agents *AgentsStatus `json:"agents,omitempty"`
	Shell  *ShellStatus  `json:"shell,omitempty"`
}

// AgentsStatus is the generated project AGENTS.md.
type AgentsStatus struct {
	Source  string `json:"source"` // project.agents as written in the manifest
	File    string `json:"file"`
	Written bool   `json:"written"`
	// Note says why it cannot be written ("an https manifest source has no file beside it").
	Note string `json:"note,omitempty"`
}

// ShellStatus is the generated project shell wrapper.
type ShellStatus struct {
	File     string   `json:"file"`
	Packages []string `json:"packages"`
	Nixpkgs  string   `json:"nixpkgs,omitempty"`
	Written  bool     `json:"written"`
	// Note is set when the packages are not applied: nix is not on PATH.
	Note string `json:"note,omitempty"`
}

// List returns every project registered in the user config.
func (s *Service) List() ([]Info, error) {
	all, err := snapshots(s.App.Root)
	if err != nil {
		return nil, err
	}
	bySource := map[string]string{}
	for name, sn := range all {
		bySource[sn.Source] = name
	}
	var out []Info
	for _, p := range s.App.Config.Projects {
		canon, err := s.canonical(p.Source)
		if err != nil {
			return nil, err
		}
		info := Info{Source: canon}
		if name, ok := bySource[canon]; ok {
			sn := all[name]
			info.Name, info.Dir, info.LastUpdate = name, filepath.Join(s.App.Root, name), sn.LastUpdate
			if m, err := sn.parse(); err == nil {
				info.Repos = len(m.Repos)
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// Status inspects projects on disk without touching the network. An empty
// name means all of them.
func (s *Service) Status(ctx context.Context, name string) ([]ProjectStatus, error) {
	all, err := snapshots(s.App.Root)
	if err != nil {
		return nil, err
	}
	var names []string
	for n := range all {
		if name == "" || n == name {
			names = append(names, n)
		}
	}
	if name != "" && len(names) == 0 {
		return nil, fmt.Errorf("project %q is not cloned (see `ade project list`)", name)
	}
	sort.Strings(names)
	out := make([]ProjectStatus, 0, len(names))
	for _, n := range names {
		ps, err := s.projectStatus(ctx, n, all[n])
		if err != nil {
			return nil, err
		}
		out = append(out, ps)
	}
	return out, nil
}

func (s *Service) projectStatus(ctx context.Context, name string, sn *snapshot) (ProjectStatus, error) {
	m, err := sn.parse()
	if err != nil {
		return ProjectStatus{}, fmt.Errorf("project %s: %w", name, err)
	}
	dir := filepath.Join(s.App.Root, name)
	names, err := repoDirs(dir, m.RepoNames())
	if err != nil {
		return ProjectStatus{}, err
	}
	repos := make([]RepoStatus, len(names))
	forEach(s.concurrency(), len(names), func(i int) {
		_, inManifest := m.Repos[names[i]]
		repos[i] = repoStatus(ctx, filepath.Join(dir, names[i]), names[i], inManifest)
	})
	groups := groupStatuses(dir, m)
	for i := range repos {
		for _, g := range groups {
			if slices.Contains(g.Repos, repos[i].Name) {
				repos[i].Groups = append(repos[i].Groups, g.Name)
			}
		}
	}
	return ProjectStatus{Name: name, Source: sn.Source, Dir: dir, LastUpdate: sn.LastUpdate, Repos: repos, RepoGroups: groups, Setup: setupStatus(dir, sn.Source, m)}, nil
}

// groupStatuses lists the manifest's repo groups sorted by name.
func groupStatuses(dir string, m *config.Manifest) []GroupStatus {
	var out []GroupStatus
	for _, name := range slices.Sorted(maps.Keys(m.RepoGroups)) {
		g := m.RepoGroups[name]
		gs := GroupStatus{Name: name, Repos: g.Repos, Workspace: g.Workspace}
		if g.Workspace != "" {
			gs.File = GroupWorkspacePath(dir, g)
			if b, err := os.ReadFile(gs.File); err == nil {
				g, gerr := generatedGroup(b, filepath.Base(gs.File))
				gs.Written = gerr == nil && g == name
			}
		}
		out = append(out, gs)
	}
	return out
}

// repoDirs is the manifest's repos plus any other directory holding a bare clone.
func repoDirs(projectDir string, manifest []string) ([]string, error) {
	seen := map[string]bool{}
	names := append([]string(nil), manifest...)
	for _, n := range manifest {
		seen[n] = true
	}
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() && !seen[e.Name()] && exists(filepath.Join(projectDir, e.Name(), ".bare")) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func repoStatus(ctx context.Context, dir, name string, inManifest bool) RepoStatus {
	rs := RepoStatus{Name: name, InManifest: inManifest}
	bare := filepath.Join(dir, ".bare")
	if !exists(bare) {
		return rs
	}
	rs.Present = true
	var c git.Client
	def, err := c.DefaultBranch(ctx, bare)
	if err != nil {
		rs.Error = err.Error()
		return rs
	}
	rs.DefaultBranch = def
	trees, err := c.Worktrees(ctx, bare)
	if err != nil {
		rs.Error = err.Error()
		return rs
	}
	baseDir := filepath.Join(dir, git.BranchDir(def))
	for _, t := range trees {
		ts := treeStatus(ctx, c, t)
		if filepath.Clean(t.Path) == filepath.Clean(baseDir) {
			rs.Baseline = &ts
		} else {
			rs.Worktrees = append(rs.Worktrees, ts)
		}
	}
	sort.Slice(rs.Worktrees, func(i, j int) bool { return rs.Worktrees[i].Name < rs.Worktrees[j].Name })
	return rs
}

// StatusOf is the disk state of one worktree, as `ade project status` reports it.
func StatusOf(ctx context.Context, t git.Worktree) TreeStatus {
	return treeStatus(ctx, git.Client{}, t)
}

func treeStatus(ctx context.Context, c git.Client, t git.Worktree) TreeStatus {
	ts := TreeStatus{Name: filepath.Base(t.Path), Path: t.Path, Branch: t.Branch}
	if clean, err := c.IsClean(ctx, t.Path); err != nil {
		ts.Dirty, ts.StatusError = true, git.Reason(err)
	} else {
		ts.Dirty = !clean
	}
	ahead, behind, err := c.AheadBehind(ctx, t.Path)
	if err == nil {
		ts.HasUpstream, ts.Ahead, ts.Behind = true, ahead, behind
	}
	return ts
}
