package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/git"
)

// Blocker is one place where removing a project would lose work.
type Blocker struct {
	Where   string   `json:"where"` // worktree or branch, relative to the project
	Reasons []string `json:"reasons"`
}

// RemovePlan is what `ade project remove` is about to do.
type RemovePlan struct {
	Name       string    `json:"name"`
	Source     string    `json:"source"`
	Dir        string    `json:"dir"`
	Registered bool      `json:"registered"` // a [[projects]] entry in the user config
	Repos      int       `json:"repos"`
	Blockers   []Blocker `json:"blockers,omitempty"`
}

// PlanRemove inspects what removing the project would delete. ref is the
// project's directory name or its manifest source. Nothing is changed and no
// network is used.
func (s *Service) PlanRemove(ctx context.Context, ref string) (RemovePlan, error) {
	all, err := snapshots(s.App.Root)
	if err != nil {
		return RemovePlan{}, err
	}
	name, sn := "", (*snapshot)(nil)
	if got, ok := all[ref]; ok {
		name, sn = ref, got
	} else if canon, err := s.canonical(ref); err == nil {
		for n, c := range all {
			if c.Source == canon {
				name, sn = n, c
			}
		}
	}
	if sn == nil {
		return RemovePlan{}, fmt.Errorf("project %q is not cloned (see `ade project list`)", ref)
	}
	plan := RemovePlan{Name: name, Source: sn.Source, Dir: filepath.Join(s.App.Root, name)}
	for _, p := range s.App.Config.Projects {
		if c, err := s.canonical(p.Source); err == nil && c == sn.Source {
			plan.Registered = true
		}
	}
	sts, err := s.Status(ctx, name)
	if err != nil {
		return RemovePlan{}, err
	}
	for _, ps := range sts {
		for _, rs := range ps.Repos {
			if !rs.Present {
				continue
			}
			plan.Repos++
			plan.Blockers = append(plan.Blockers, repoBlockers(ctx, ps.Dir, rs)...)
		}
	}
	return plan, nil
}

// repoBlockers lists what a repo holds that exists nowhere else: dirty
// trees, commits no remote branch has, and local branches nobody pushed.
func repoBlockers(ctx context.Context, projectDir string, rs RepoStatus) []Blocker {
	var c git.Client
	rel := func(p string) string {
		if r, err := filepath.Rel(projectDir, p); err == nil {
			return r
		}
		return p
	}
	var out []Blocker
	checked := map[string]bool{}
	trees := rs.Worktrees
	if rs.Baseline != nil {
		trees = append([]TreeStatus{*rs.Baseline}, trees...)
	}
	for _, ts := range trees {
		if ts.Branch != "" {
			checked[ts.Branch] = true
		}
		if keep := localGuard(ctx, c, ts, nil); len(keep) > 0 {
			out = append(out, Blocker{Where: rel(ts.Path), Reasons: keep})
		}
	}
	bare := filepath.Join(projectDir, rs.Name, ".bare")
	heads, err := c.Run(ctx, bare, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return append(out, Blocker{Where: rel(bare), Reasons: []string{"cannot list branches: " + git.Reason(err)}})
	}
	for _, b := range strings.Fields(heads) {
		if checked[b] {
			continue
		}
		on, err := c.Run(ctx, bare, "branch", "-r", "--contains", b)
		switch {
		case err != nil:
			out = append(out, Blocker{Where: rs.Name + " branch " + b, Reasons: []string{"cannot tell whether it is pushed: " + git.Reason(err)}})
		case strings.TrimSpace(on) == "":
			out = append(out, Blocker{Where: rs.Name + " branch " + b, Reasons: []string{"commits not on any remote branch (never pushed)"}})
		}
	}
	return out
}

// Remove unregisters the project from the user config and deletes its
// directory. It refuses when the plan has blockers unless force is set.
func (s *Service) Remove(ctx context.Context, plan RemovePlan, force bool) error {
	e := audit.Entry{
		Action: "project.remove", Target: plan.Source, Project: plan.Name,
		Params: map[string]any{"force": force, "blockers": len(plan.Blockers), "dir": plan.Dir},
	}
	err := s.remove(plan, force)
	e.Outcome = outcome(err)
	if err != nil {
		e.Error = err.Error()
	}
	s.audit(e)
	return err
}

func (s *Service) remove(plan RemovePlan, force bool) error {
	if len(plan.Blockers) > 0 && !force {
		return fmt.Errorf("project %s holds work that exists only here (use --force to delete it anyway)", plan.Name)
	}
	// The directory comes from the plan: never delete anything that is not a
	// project directly under the root.
	if !validSegment(plan.Name) || filepath.Clean(plan.Dir) != filepath.Join(s.App.Root, plan.Name) ||
		!exists(filepath.Join(plan.Dir, snapshotFile)) {
		return fmt.Errorf("%s is not a project directory under %s", plan.Dir, s.App.Root)
	}
	if plan.Registered {
		if err := s.unregister(plan.Source); err != nil {
			return err
		}
	}
	return os.RemoveAll(plan.Dir)
}

var (
	projectsHeader = regexp.MustCompile(`^\s*\[\[\s*projects\s*\]\]\s*(#.*)?$`)
	anyHeader      = regexp.MustCompile(`^\s*\[`)
)

// unregister drops the [[projects]] blocks for source from the user config.
// Like register it edits the text, so comments and layout elsewhere survive.
func (s *Service) unregister(source string) error {
	file := s.App.Config.File
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	lines := strings.SplitAfter(string(data), "\n")
	var kept []string
	removed := 0
	for i := 0; i < len(lines); {
		if !projectsHeader.MatchString(lines[i]) {
			kept = append(kept, lines[i])
			i++
			continue
		}
		j := i + 1
		for j < len(lines) && !anyHeader.MatchString(lines[j]) {
			j++
		}
		// Blank and comment lines closing the block belong to what follows
		// (a comment above the next table describes that table).
		end := j
		for end > i+1 && (strings.TrimSpace(lines[end-1]) == "" || strings.HasPrefix(strings.TrimSpace(lines[end-1]), "#")) {
			end--
		}
		var parsed struct {
			Projects []config.ProjectRef `toml:"projects"`
		}
		block := strings.Join(lines[i:end], "")
		if _, err := toml.Decode(block, &parsed); err == nil && len(parsed.Projects) == 1 && s.sameSource(parsed.Projects[0].Source, source) {
			removed++
			// The blank line that set the block apart goes with it.
			if len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
				kept = kept[:len(kept)-1]
			}
			i = end
			continue
		}
		kept = append(kept, lines[i:j]...)
		i = j
	}
	if removed == 0 {
		return fmt.Errorf("no [[projects]] entry for %s in %s: remove it from the config by hand", source, file)
	}
	if err := writeFileAtomic(file, []byte(strings.Join(kept, ""))); err != nil {
		return err
	}
	var left []config.ProjectRef
	for _, p := range s.App.Config.Projects {
		if !s.sameSource(p.Source, source) {
			left = append(left, p)
		}
	}
	s.App.Config.Projects = left
	return nil
}

func (s *Service) sameSource(a, canon string) bool {
	c, err := s.canonical(a)
	return err == nil && c == canon
}

// writeFileAtomic replaces path with data, keeping its permissions.
func writeFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
