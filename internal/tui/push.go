package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/workspace"
)

// openPush is `u` on a Home item (every worktree of the item) and `p` on the MR
// page (the worktree of the MR's source branch; `u` there is another action):
// push the branch to origin.
// The dialog first lists, per worktree, the remote branch, how many commits
// are ahead (read from the last fetch) and their subjects; `enter` pushes
// with the forge's write token and sets the upstream, audited as git.push,
// like `ade push`. A branch with nothing ahead, a diverged one and the
// default branch are listed with the reason and never pushed.
func (m model) openPush() (model, tea.Cmd) {
	a := m.app
	var (
		proj, heading string
		trees         func(ctx context.Context) ([]string, string, error) // worktree paths, or why there are none
	)
	switch {
	case m.mr != nil:
		p := m.mr
		if p.cur < 0 || p.cur >= len(p.mrs) {
			return m, nil
		}
		mr := p.mrs[p.cur]
		proj, heading = p.project, mr.Title
		trees = func(ctx context.Context) ([]string, string, error) {
			mf, err := loadProject(a, proj)
			if err != nil {
				return nil, "", err
			}
			t, err := workspace.TargetForMR(ctx, a.Root, mf, mr)
			if err != nil {
				return nil, "", err
			}
			if t.Baseline {
				return nil, "no worktree for " + mr.SourceBranch + ": w opens one", nil
			}
			return []string{t.Worktree}, "", nil
		}
	default:
		r, ok := m.selectedRow()
		if !ok || r.item == nil {
			return m, nil
		}
		it := *r.item
		proj, heading = r.project, it.Issue.Title
		trees = func(ctx context.Context) ([]string, string, error) {
			mf, err := loadProject(a, proj)
			if err != nil {
				return nil, "", err
			}
			var paths []string
			for _, w := range wsItemWorktrees(ctx, a.Root, mf, it) {
				paths = append(paths, w.Path)
			}
			if len(paths) == 0 {
				return nil, "no worktree for this item: s starts work on it", nil
			}
			return paths, "", nil
		}
	}
	retry := "u"
	if m.mr != nil {
		retry = "p"
	}
	return m.openAction(actionSpec{
		title:   "Push your branch",
		heading: heading,
		reading: "reading the branch…",
		retry:   retry,
		resolve: func(ctx context.Context) (actionPlan, error) {
			paths, why, err := trees(ctx)
			if err != nil {
				return actionPlan{}, err
			}
			if why != "" {
				return actionPlan{nothing: true, lines: []string{"Nothing to push: " + why + "."}}, nil
			}
			ps := project.New(a, "user")
			var plans []project.PushPlan
			var lines []string
			for _, path := range paths {
				pl, err := ps.PushPlan(ctx, path)
				if err != nil {
					return actionPlan{}, err
				}
				if len(lines) > 0 {
					lines = append(lines, "")
				}
				lines = append(lines, pushPlanLines(pl)...)
				if pl.Pushable() {
					plans = append(plans, pl)
				}
			}
			if len(plans) == 0 {
				return actionPlan{nothing: true, lines: lines}, nil
			}
			return actionPlan{
				lines: lines, verb: "push",
				note: "A push sends commits only, never forces, and uses the forge's write token. The merge request tracking the branch updates by itself.",
				apply: func(ctx context.Context) ([]string, error) {
					var done []string
					for _, pl := range plans {
						if err := ps.Push(ctx, pl); err != nil {
							return done, err
						}
						done = append(done, fmt.Sprintf("pushed %s to %s", pl.Label(), pl.Upstream))
					}
					return done, nil
				},
				after: func(m model) tea.Cmd { return m.loadStatus() },
			}, nil
		},
	})
}

// pushPlanLines is one worktree of the push dialog: its branch and remote
// branch, the ahead count with the commit subjects, or why nothing is pushed.
func pushPlanLines(p project.PushPlan) []string {
	head := p.Label()
	if p.Upstream != "" {
		head += " → " + p.Upstream
		if p.NewBranch {
			head += " (new branch)"
		}
	}
	lines := []string{head}
	if p.Refuse != "" {
		return append(lines, "  "+p.Refuse)
	}
	n := "  " + countWord(p.Ahead, "commit") + " ahead"
	if p.SetUpstream && !p.NewBranch {
		n += ", and it becomes the upstream"
	}
	lines = append(lines, n)
	for _, c := range p.Commits {
		lines = append(lines, "    "+c)
	}
	if p.Ahead > len(p.Commits) {
		lines = append(lines, fmt.Sprintf("    … and %d more", p.Ahead-len(p.Commits)))
	}
	if p.Dirty {
		lines = append(lines, "  uncommitted changes stay behind: a push sends commits only")
	}
	return lines
}
