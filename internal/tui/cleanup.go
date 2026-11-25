package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/workspace"
)

// `x` on a Home item cleans up its finished work, and the palette's "Clean up
// merged work everywhere" does the same for every project (`ade gc`). Both
// use the dialog every Home write uses: they judge the worktrees exactly as
// the CLI does (merge requests merged or closed, tree clean, everything
// pushed), list what would go and what stays with the reason, and remove only
// on enter. `x` is `ade cleanup <ref>`: it also fast-forwards the repo's
// default branch (clean trees only). Both delete the local branch of a removed
// worktree: with `git branch -d` when git sees it merged, with `git branch -D`
// when it is a squash-merged branch whose commits are all in the merged MR's
// head; the preview says which, and why a branch stays. The palette entry is
// `ade gc`: every project's worktrees, no default-branch update.

// mrLookup asks the forge each manifest names for the MRs of a branch, opening
// one client per forge (through the sync service's opener in tests).
func (m model) mrLookup() project.MRLookup {
	open := forgesync.New(m.app).Open
	if m.sync != nil && m.sync.Open != nil {
		open = m.sync.Open
	}
	clients := map[string]forge.Forge{}
	return func(ctx context.Context, mf *config.Manifest, repoPath, branch string) ([]forge.MR, error) {
		f, ok := clients[mf.Forge]
		if !ok {
			fc, defined := m.app.Config.Forges[mf.Forge]
			if !defined {
				return nil, fmt.Errorf("forge %q is not defined in the user config", mf.Forge)
			}
			var err error
			if f, err = open(ctx, mf.Forge, fc); err != nil {
				return nil, err
			}
			clients[mf.Forge] = f
		}
		return f.ListMRs(ctx, forge.MRQuery{Project: repoPath, State: "all", SourceBranch: branch})
	}
}

// openCleanup is Home's `x`: clean up the selected item.
func (m model) openCleanup() (model, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok || r.item == nil {
		return m, nil
	}
	it, a, lookup := *r.item, m.app, m.mrLookup()
	return m.openAction(actionSpec{
		title:   "Clean up · " + it.Ref.String(),
		heading: it.Issue.Title,
		reading: "judging the worktrees and asking the forge…",
		retry:   "x",
		resolve: func(ctx context.Context) (actionPlan, error) {
			mf, err := loadProject(a, r.project)
			if err != nil {
				return actionPlan{}, err
			}
			var paths []string
			for _, w := range wsItemWorktrees(ctx, a.Root, mf, it) {
				paths = append(paths, w.Path)
			}
			if len(paths) == 0 {
				return actionPlan{nothing: true, lines: []string{"This item has no worktrees on this machine: nothing to clean up."}}, nil
			}
			ps := project.New(a, "user")
			trees, err := ps.TreesAt(ctx, it.Project, paths, lookup)
			if err != nil {
				return actionPlan{}, err
			}
			return cleanupPlan(ctx, ps, trees, true), nil
		},
	})
}

// openGC is the palette's "Clean up merged work everywhere": `ade gc`.
func (m model) openGC() (model, tea.Cmd) {
	a, lookup := m.app, m.mrLookup()
	return m.openAction(actionSpec{
		title:   "Clean up merged work",
		heading: "every worktree of every project",
		reading: "judging the worktrees and asking the forge…",
		retry:   "",
		resolve: func(ctx context.Context) (actionPlan, error) {
			ps := project.New(a, "user")
			trees, err := ps.Trees(ctx, "", lookup)
			if err != nil {
				return actionPlan{}, err
			}
			return cleanupPlan(ctx, ps, trees, false), nil
		},
	})
}

// cleanupPlan lists the verdict on each tree and, for the removable ones, what
// enter does. item says the work is `ade cleanup`'s (baseline and branches too)
// rather than `ade gc`'s (worktrees).
func cleanupPlan(ctx context.Context, ps *project.Service, trees []project.Tree, item bool) actionPlan {
	var doomed []project.Tree
	var lines []string
	for _, t := range trees {
		if t.Removable() {
			doomed = append(doomed, t)
			lines = append(lines, fmt.Sprintf("remove  %s (%s; pushed: %s)", t.Label(), t.MR, t.Pushed))
			continue
		}
		lines = append(lines, fmt.Sprintf("keep    %s  %s", t.Label(), strings.Join(t.Keep, "; ")))
	}
	if len(trees) == 0 {
		return actionPlan{nothing: true, lines: []string{"No feature worktrees."}}
	}
	if len(doomed) == 0 {
		return actionPlan{nothing: true, lines: append(lines, "", "Nothing can be removed yet.")}
	}
	verb := "remove"
	lines = append(lines, "")
	if item {
		// The same dry run `ade cleanup --dry-run` shows: the branches and the
		// default branch's fast-forward.
		cleaned, baselines, _ := ps.CleanTrees(ctx, doomed, true)
		for i, c := range cleaned {
			lines = append(lines, branchPlanLine(doomed[i].Branch, c.WouldForce, c.WouldWhy))
		}
		for _, b := range baselines {
			name := b.Project + "/" + b.Repo + "/" + b.Branch
			switch b.Action {
			case "fast-forward":
				lines = append(lines, fmt.Sprintf("%s: fast-forward (%d new)", name, b.Behind))
			case "current":
				lines = append(lines, name+": up to date")
			default:
				lines = append(lines, name+": not updated: "+b.Reason)
			}
		}
		verb = "clean up"
	} else {
		for _, t := range doomed {
			forced, why := ps.PlanBranch(ctx, t)
			lines = append(lines, branchPlanLine(t.Branch, forced, why))
		}
	}
	return actionPlan{
		lines: lines, verb: verb,
		note: "Only worktrees that are clean, pushed, and whose merge requests are merged or closed are removed, and git refuses one that changed since. Each one's services stop and its tabs close first, as ade ws close does. A branch git does not see as merged is deleted with -D only when all its commits are in a merged MR's head (the rows above say which).",
		apply: func(ctx context.Context) ([]string, error) {
			if item {
				return applyCleanup(ctx, ps, doomed)
			}
			return applyGC(ctx, ps, doomed)
		},
		after: func(m model) tea.Cmd { return m.loadStatus() },
	}
}

// branchPlanLine says what enter does to a branch: git's -d if it sees it
// merged, -D on the proof that a merged MR holds its commits, else nothing.
func branchPlanLine(branch string, forced bool, why string) string {
	if forced {
		return fmt.Sprintf("branch %s: deleted with git branch -D (%s)", branch, why)
	}
	return fmt.Sprintf("branch %s: deleted if git sees it merged, else kept (%s)", branch, why)
}

// retireTrees closes the workspace of each tree about to be removed
// (workspace.Retire). It returns the trees that may go, the lines that say what
// was done, and how many stay because their instances could not be stopped.
func retireTrees(ctx context.Context, a *app.App, trees []project.Tree) (ready []project.Tree, out []string, failed int) {
	for _, t := range trees {
		res, err := workspace.Retire(ctx, a, t.Path)
		for _, n := range res.RetireNotes() {
			out = append(out, t.Label()+": "+n)
		}
		if err != nil {
			out = append(out, "failed  "+t.Label()+"  stop its workspace: "+err.Error())
			failed++
			continue
		}
		ready = append(ready, t)
	}
	return ready, out, failed
}

func applyCleanup(ctx context.Context, ps *project.Service, doomed []project.Tree) ([]string, error) {
	ready, out, failed := retireTrees(ctx, ps.App, doomed)
	cleaned, baselines, removeFailed := ps.CleanTrees(ctx, ready, false)
	failed += removeFailed
	for i, c := range cleaned {
		t := ready[i]
		switch {
		case c.Error != "":
			out = append(out, "failed  "+t.Label()+"  "+c.Error)
		default:
			out = append(out, branchResult(t, c.Fate))
		}
	}
	for _, b := range baselines {
		if b.Done {
			out = append(out, fmt.Sprintf("fast-forwarded %s/%s/%s (%d new)", b.Project, b.Repo, b.Branch, b.Behind))
		}
	}
	if failed > 0 {
		return out, fmt.Errorf("%d of %d worktrees could not be removed", failed, len(doomed))
	}
	return out, nil
}

func applyGC(ctx context.Context, ps *project.Service, doomed []project.Tree) ([]string, error) {
	ready, out, failed := retireTrees(ctx, ps.App, doomed)
	for _, t := range ready {
		fate, err := ps.RemoveTree(ctx, t)
		if err != nil {
			out = append(out, "failed  "+t.Label()+"  "+err.Error())
			failed++
			continue
		}
		out = append(out, branchResult(t, fate))
	}
	if failed > 0 {
		return out, fmt.Errorf("%d of %d worktrees could not be removed", failed, len(doomed))
	}
	return out, nil
}

func branchResult(t project.Tree, f project.BranchFate) string {
	switch {
	case !f.Deleted:
		return fmt.Sprintf("removed %s (branch %s kept: %s)", t.Label(), t.Branch, f.Why)
	case f.Forced:
		return fmt.Sprintf("removed %s, deleted branch %s with git branch -D (%s)", t.Label(), t.Branch, f.Why)
	}
	return fmt.Sprintf("removed %s, deleted branch %s", t.Label(), t.Branch)
}
