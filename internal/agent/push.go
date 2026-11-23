package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/commitmsg"
	"github.com/coredev-uk/ade/internal/commitprefix"
	"github.com/coredev-uk/ade/internal/git"
)

// PushOpts are the user's words for the commit.
type PushOpts struct {
	Message string // empty: the issue title and the run id
	DryRun  bool   // PushPlan only: nothing is committed or pushed
}

// PushPreview is what a push would do, for the dry run and the confirmation.
type PushPreview struct {
	Run     Run    `json:"run"`
	Branch  string `json:"branch"`
	Remote  string `json:"remote"` // origin/<branch>
	Commit  bool   `json:"commit"` // the worktree's changes will be committed first
	Message string `json:"message,omitempty"`
	// Author is who that commit is made as.
	Author string `json:"author,omitempty"`
	// Commits are the commits that will be pushed beyond the run's start
	// ("<sha> <subject>"): ADE's own from accept, and any of yours made since.
	Commits []string    `json:"commits,omitempty"`
	Diff    DiffSummary `json:"diff"`
	// Left are uncommitted paths in the worktree that the push does not touch
	// (an accepted run: your own work in progress).
	Left []string `json:"left,omitempty"`
}

// accepted reports that the run's work was fast-forwarded into the item's
// branch by accept: it is committed already, and the worktree may hold
// uncommitted work of the user's that is not the agent's.
func (r Run) accepted() bool { return !r.AcceptedAt.IsZero() }

// PushPlan checks run id can be pushed and says what Push would do, changing
// nothing. A run taken in by accept is pushed as it is: its commit exists, and
// nothing else in the worktree (your uncommitted files) is committed as the
// agent's. Any other run commits everything in its worktree, which was clean
// when the run started.
func (m *Manager) PushPlan(ctx context.Context, id string, o PushOpts) (PushPreview, error) {
	r, err := m.Store.Get(id)
	if err != nil {
		return PushPreview{}, err
	}
	switch {
	case r.State != StateReview:
		return PushPreview{}, fmt.Errorf("run %s is %s: only a run in review can be pushed", r.ID, r.State)
	case r.Isolated():
		return PushPreview{}, fmt.Errorf("run %s keeps its changes on %s: ade agent accept %s (or discard) before pushing", r.ID, r.AIBranch, r.ID)
	case r.accepted() && o.Message != "":
		return PushPreview{}, fmt.Errorf("run %s was accepted: its commit is made already, so push takes no message (ade agent accept -m sets it)", r.ID)
	}
	if err := checkWorktree(ctx, r.Worktree, r.Branch); err != nil {
		return PushPreview{}, err
	}
	p := PushPreview{Run: r, Branch: r.Branch, Remote: "origin/" + r.Branch}
	if r.accepted() {
		if p.Diff, err = committedDiff(ctx, r.Worktree, r.BaseSHA, false); err != nil {
			return p, err
		}
		if p.Commits, err = commitsSince(ctx, r.Worktree, r.BaseSHA); err != nil {
			return p, err
		}
		if p.Left, err = dirtyPaths(ctx, r.Worktree); err != nil {
			return p, err
		}
	} else {
		if p.Diff, err = worktreeDiff(ctx, r.Worktree, r.BaseSHA, false); err != nil {
			return p, err
		}
		p.Commit, p.Message = true, m.fullMessage(ctx, r, o.Message)
		p.Author = AuthorFromGit(ctx, r.Worktree).Name
		if p.Commits, err = commitsSince(ctx, r.Worktree, r.BaseSHA); err != nil {
			return p, err
		}
	}
	if p.Diff.Empty() {
		return p, errNoChanges
	}
	return p, nil
}

// Push is the user's approval of the diff made real: commit the worktree with
// the synthetic author (a run taken in by accept has its commit already, and
// nothing is committed), push the branch, and the MR (which tracks the branch)
// updates by itself. review → pushed.
func (m *Manager) Push(ctx context.Context, id string, o PushOpts) (Run, error) {
	p, err := m.PushPlan(ctx, id, o)
	r := p.Run
	if err != nil {
		return r, err
	}
	if o.DryRun {
		m.audit(audit.Entry{
			Action: "git.push", Target: r.Worktree, Item: r.Item(), DryRun: true,
			Params: map[string]any{"run": r.ID, "branch": r.Branch, "commit": p.Commit, "files": len(p.Diff.Files)},
		})
		return r, nil
	}
	if p.Commit {
		if err := m.commitWork(ctx, &r, r.Worktree, o.Message); err != nil {
			return r, m.pushFailed(r, "commit", err)
		}
	}
	gc := git.Client{}
	if m.GitFor != nil {
		if gc, err = m.GitFor(ctx, r); err != nil {
			return r, m.pushFailed(r, "credentials", err)
		}
	}
	perr := pushBranch(ctx, gc, r.Worktree, r.Branch)
	e := audit.Entry{Action: "git.push", Target: r.Worktree, Item: r.Item(), Params: map[string]any{"run": r.ID, "branch": r.Branch, "commits": len(p.Commits)}}
	if perr != nil {
		e.Outcome, e.Error = "error", perr.Error()
	}
	m.audit(e)
	if perr != nil {
		return r, m.pushFailed(r, "push", perr)
	}
	r.Error = ""
	if err := m.move(&r, StatePushed); err != nil {
		return r, err
	}
	return r, nil
}

// fullMessage is the commit message ADE will use: the user's words (or the
// issue title) with the MR's commit prefix on the first commit, and always the
// line that says an agent wrote it.
func (m *Manager) fullMessage(ctx context.Context, r Run, message string) string {
	msg := message
	if msg == "" {
		msg = m.commitTitle(r)
	}
	// Only the MR's first commit carries the prefix.
	if commitprefix.FirstCommit(ctx, r.Worktree) {
		prefix, _ := m.commitPrefix(ctx, r)
		msg = commitmsg.Apply(prefix, msg)
	}
	return strings.TrimRight(msg, "\n") + "\n\n" + m.writtenBy(r) + "\n"
}

// writtenBy is the line every ADE agent commit carries, whoever worded the
// message.
func (m *Manager) writtenBy(r Run) string {
	return fmt.Sprintf("Written by ade agent run %s (%s, %s) and reviewed before pushing.", r.ID, r.Driver, r.Model)
}

// commitWork commits everything in dir (the run's worktree, or its -ai
// worktree) as the synthetic author, with the user's message or the default
// one, and records the commit on the run. Nothing to commit is not an error.
func (m *Manager) commitWork(ctx context.Context, r *Run, dir, message string) error {
	msg := m.fullMessage(ctx, *r, message)
	author := AuthorFromGit(ctx, r.Worktree)
	sha, err := commitAll(ctx, dir, author, msg)
	if err != nil {
		return err
	}
	if sha == "" {
		return nil
	}
	r.Commits = append(r.Commits, sha)
	if err := m.Store.Save(*r); err != nil {
		return err
	}
	m.audit(audit.Entry{
		Action: "git.commit", Target: dir, Item: r.Item(),
		Params: map[string]any{"run": r.ID, "sha": sha, "author": author.Name},
	})
	return nil
}

// commitTitle is the default commit subject: the issue title, or for an
// address run the MR it answers.
func (m *Manager) commitTitle(r Run) string {
	c, _ := readContext(m.Store.Dir(r))
	title := c.Issue.Title
	if title == "" {
		title = "Agent changes for " + r.Item()
	}
	if r.Kind == KindAddress && r.MR != nil {
		title = "Address review comments on " + r.MR.String()
	}
	return title
}

// restoreStart discards an in-place run's changes: the worktree goes back to
// the commit the run started from. It refuses when HEAD has moved (a commit
// of yours would be thrown away with it).
func (m *Manager) restoreStart(ctx context.Context, r Run) error {
	if r.BaseSHA == "" {
		return errors.New("the run recorded no start commit to restore")
	}
	tip, err := head(ctx, r.Worktree)
	if err != nil {
		return err
	}
	if tip != r.BaseSHA {
		return fmt.Errorf("%s has moved from %s to %s since the run started: commits are not thrown away; reset them yourself", r.Branch, short(r.BaseSHA), short(tip))
	}
	return restoreTo(ctx, r.Worktree, r.BaseSHA)
}
