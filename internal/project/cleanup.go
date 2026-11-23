package project

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/git"
)

// Baseline is what per-item cleanup did to a repo's default-branch tree.
type Baseline struct {
	Project string `json:"project"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch,omitempty"`
	Path    string `json:"path,omitempty"`
	// Action is fast-forward, current (already up to date) or skip.
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	Behind int    `json:"behind,omitempty"`
	// Done is true when a real run moved the tree.
	Done bool   `json:"done,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// FastForwardBaseline brings the default-branch worktree of repo up to its
// upstream, after fetching. It only ever fast-forwards a clean tree that has
// nothing of its own (the same rule as `ade update`): anything else is skipped
// with the reason. With dry set it fetches and reports, and moves nothing.
func (s *Service) FastForwardBaseline(ctx context.Context, projectName, repo string, dry bool) Baseline {
	b := Baseline{Project: projectName, Repo: repo, Action: "skip"}
	m, err := LoadManifest(filepath.Join(s.App.Root, projectName))
	if err != nil {
		b.Reason = err.Error()
		return b
	}
	bare := filepath.Join(s.App.Root, projectName, repo, ".bare")
	c, err := s.GitClient(ctx, m.Forge, bare)
	if err != nil {
		b.Reason = git.Reason(err)
		return b
	}
	def, err := c.DefaultBranch(ctx, bare)
	if err != nil {
		b.Reason = "default branch: " + git.Reason(err)
		return b
	}
	b.Branch = def
	trees, err := c.Worktrees(ctx, bare)
	if err != nil {
		b.Reason = git.Reason(err)
		return b
	}
	for _, t := range trees {
		if t.Branch == def {
			b.Path = t.Path
		}
	}
	if b.Path == "" {
		b.Reason = "no worktree on " + def
		return b
	}
	if err := c.Fetch(ctx, bare); err != nil {
		b.Reason = "fetch failed: " + git.Reason(err)
		return b
	}
	ahead, behind, err := c.AheadBehind(ctx, b.Path)
	switch {
	case err != nil:
		b.Reason = git.Reason(err)
		return b
	case behind == 0:
		b.Action = "current"
		return b
	case ahead > 0:
		b.Reason = fmt.Sprintf("%s has %d commits of its own: diverged", def, ahead)
		return b
	}
	if clean, err := c.IsClean(ctx, b.Path); err != nil || !clean {
		b.Reason = "uncommitted changes"
		return b
	}
	b.Action, b.Behind = "fast-forward", behind
	if dry {
		return b
	}
	b.From, _ = c.Head(ctx, b.Path)
	err = c.FastForward(ctx, b.Path)
	b.To, _ = c.Head(ctx, b.Path)
	e := audit.Entry{
		Action: "git.pull", Target: b.Path, Project: projectName, Outcome: outcome(err),
		Params: map[string]any{"from": b.From, "to": b.To, "commits": behind, "for": "cleanup"},
	}
	if err != nil {
		e.Error = git.Reason(err)
		b.Action, b.Reason = "skip", git.Reason(err)
	} else {
		b.Done = true
	}
	s.audit(e)
	return b
}

// Cleaned is what CleanTrees did to one tree.
type Cleaned struct {
	Removed bool   `json:"removed,omitempty"`
	Error   string `json:"error,omitempty"`
	// Fate says what became of the branch after a real removal.
	Fate BranchFate `json:"-"`
	// WouldForce and WouldWhy are the dry-run plan for the branch: whether it
	// would need `git branch -D`, and the proof or the reason it would stay.
	WouldForce bool   `json:"-"`
	WouldWhy   string `json:"-"`
}

// CleanTrees is the work of `ade cleanup` on trees Trees judged removable: it
// removes the worktrees, fast-forwards each repo's baseline once, then deletes
// the branches, which the fast-forward lets git see as merged (a squash merge
// is proven from the merged MR's head instead). With dry set it only plans: the
// baselines are fetched and reported, the branches judged, nothing is touched.
// cleaned is parallel to trees; failed counts the removals that failed.
func (s *Service) CleanTrees(ctx context.Context, trees []Tree, dry bool) (cleaned []Cleaned, baselines []Baseline, failed int) {
	cleaned = make([]Cleaned, len(trees))
	var removed []int
	for i, t := range trees {
		if dry {
			cleaned[i].WouldForce, cleaned[i].WouldWhy = s.PlanBranch(ctx, t)
			removed = append(removed, i)
			continue
		}
		if err := s.RemoveWorktree(ctx, t); err != nil {
			cleaned[i].Error = err.Error()
			failed++
			continue
		}
		cleaned[i].Removed = true
		removed = append(removed, i)
	}
	var seen []string
	for _, i := range removed {
		t := trees[i]
		if key := t.Project + "/" + t.Repo; !slices.Contains(seen, key) {
			seen = append(seen, key)
			baselines = append(baselines, s.FastForwardBaseline(ctx, t.Project, t.Repo, dry))
		}
	}
	if !dry {
		for _, i := range removed {
			cleaned[i].Fate = s.DeleteBranch(ctx, trees[i])
		}
	}
	return cleaned, baselines, failed
}
