package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/coredev-uk/ade/internal/commitprefix"
	"github.com/coredev-uk/ade/internal/project"
)

const commitHookGroup = "commit-prefix"

// commitHookChecks reports, for each worktree that records a commit prefix
// (ade.commitPrefix), whether commits made there get it: ADE's commit-msg hook
// is installed, or a commit-msg hook of the user's is in place (left alone, so
// not a problem). A worktree with neither is a warning: the first commit of its
// MR will lack the prefix unless the developer types it.
func commitHookChecks(ctx context.Context, o Options) []Check {
	if o.Config == nil || o.Root == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	entries, err := os.ReadDir(o.Root)
	if err != nil {
		return nil
	}
	var checks []Check
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		projectDir := filepath.Join(o.Root, e.Name())
		if _, err := project.LoadManifest(projectDir); err != nil {
			continue
		}
		for _, tree := range prefixedTrees(ctx, projectDir) {
			checks = append(checks, commitHookCheck(ctx, e.Name(), tree))
		}
	}
	return checks
}

// prefixedTree is a worktree with a commit prefix.
type prefixedTree struct{ repo, path, prefix string }

// prefixedTrees lists the project's worktrees that record a commit prefix. The
// worktrees come from each bare clone's worktrees/*/gitdir files.
func prefixedTrees(ctx context.Context, projectDir string) []prefixedTree {
	repos, err := os.ReadDir(projectDir)
	if err != nil {
		return nil
	}
	var out []prefixedTree
	for _, r := range repos {
		admin, err := os.ReadDir(filepath.Join(projectDir, r.Name(), ".bare", "worktrees"))
		if err != nil {
			continue
		}
		for _, a := range admin {
			gitdir, err := os.ReadFile(filepath.Join(projectDir, r.Name(), ".bare", "worktrees", a.Name(), "gitdir"))
			if err != nil {
				continue
			}
			path := filepath.Dir(strings.TrimSpace(string(gitdir)))
			if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
				continue
			}
			if prefix := commitprefix.Read(ctx, path); prefix != "" {
				out = append(out, prefixedTree{repo: r.Name(), path: path, prefix: prefix})
			}
		}
	}
	return out
}

func commitHookCheck(ctx context.Context, projectName string, t prefixedTree) Check {
	c := Check{Group: commitHookGroup, Name: projectName + "/" + t.repo + "/" + filepath.Base(t.path)}
	plan, err := commitprefix.PlanHook(ctx, t.path)
	switch {
	case err != nil:
		c.Status, c.Detail = Warn, "cannot inspect the git hooks: "+err.Error()
	case plan.Installed:
		c.Status, c.Detail = OK, "commit-msg hook adds "+t.prefix+" to the MR's first commit"
	case plan.Stale:
		c.Status, c.Detail = Warn, "ade's commit-msg hook is out of date"
		c.Fix = "run `ade ws hook install " + t.path + "`"
	case plan.Foreign != "":
		c.Status, c.Detail = OK, "your own commit-msg hook is in use ("+plan.Foreign+"): ade leaves it"
	default:
		c.Status, c.Detail = Warn, "commits made here do not get the prefix "+t.prefix+": no commit-msg hook"
		c.Fix = "run `ade ws hook install " + t.path + "` (or set conventions.commit_prefix_hook = true in the manifest so start work does)"
	}
	return c
}
