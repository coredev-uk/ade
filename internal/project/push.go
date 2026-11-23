package project

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/git"
)

// maxPushCommits is how many commit subjects a PushPlan lists.
const maxPushCommits = 10

// PushPlan is what pushing one worktree's branch would do: the commits that
// are not on the remote yet. It is read from the refs as the last fetch left
// them (no network), so the push itself can still be refused by the remote.
type PushPlan struct {
	Project string `json:"project"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	Forge   string `json:"forge"`
	// Head is the commit the plan was read at: Push sends exactly this commit,
	// and refuses when the branch has moved since.
	Head string `json:"head"`
	// Upstream is the remote branch the push updates ("origin/42-limit").
	Upstream string `json:"upstream"`
	// NewBranch is true when the remote has no such branch yet: the push
	// creates it.
	NewBranch bool `json:"new_branch,omitempty"`
	// SetUpstream is true when the push also makes the branch track the remote
	// one (it did not before).
	SetUpstream bool `json:"set_upstream,omitempty"`
	Ahead       int  `json:"ahead"`
	Behind      int  `json:"behind"`
	// Commits are the subjects of the commits to push, newest first, at most
	// ten; Ahead is the whole count.
	Commits []string `json:"commits,omitempty"`
	// Dirty says uncommitted changes stay behind: a push sends commits only.
	Dirty bool `json:"dirty,omitempty"`
	// Refuse says why the push does not happen: nothing ahead, a diverged
	// branch, the default branch, a detached HEAD.
	Refuse string `json:"refuse,omitempty"`
	// Nothing is true when Refuse only says there is nothing to push (not a
	// problem: the branch is up to date or the remote is ahead of it).
	Nothing bool `json:"nothing,omitempty"`
}

// Pushable reports whether there is something to push and nothing in the way.
func (p PushPlan) Pushable() bool { return p.Refuse == "" }

// Label is "project/repo/branch-dir", how a worktree is named to the user.
func (p PushPlan) Label() string { return p.Project + "/" + p.Repo + "/" + filepath.Base(p.Path) }

// PushPlan reads what pushing the worktree at path would do. path is a
// worktree of an ADE project (<root>/<project>/<repo>/<tree>); anywhere else
// is an error. A branch that cannot be pushed is a plan with Refuse set, not
// an error.
func (s *Service) PushPlan(ctx context.Context, path string) (PushPlan, error) {
	var c git.Client
	top, err := c.Run(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return PushPlan{}, fmt.Errorf("%s is not a git worktree", path)
	}
	top = strings.TrimSpace(top)
	proj, repo, err := s.locateTree(top)
	if err != nil {
		return PushPlan{}, err
	}
	m, err := LoadManifest(filepath.Join(s.App.Root, proj))
	if err != nil {
		return PushPlan{}, fmt.Errorf("project %s is not cloned: %w", proj, err)
	}
	p := PushPlan{Project: proj, Repo: repo, Path: top, Forge: m.Forge}
	if head, err := c.Run(ctx, top, "rev-parse", "HEAD"); err == nil {
		p.Head = strings.TrimSpace(head)
	}
	bare := filepath.Join(s.App.Root, proj, repo, ".bare")

	branch, err := c.Run(ctx, top, "symbolic-ref", "--short", "-q", "HEAD")
	if p.Branch = strings.TrimSpace(branch); err != nil || p.Branch == "" {
		p.Refuse = "detached HEAD: there is no branch to push"
		return p, nil
	}
	if def, err := c.DefaultBranch(ctx, bare); err == nil && def == p.Branch {
		p.Refuse = p.Branch + " is the repo's default branch: ADE pushes feature branches only; changes reach it through a merge request"
		return p, nil
	}
	clean, err := c.IsClean(ctx, top)
	p.Dirty = err != nil || !clean

	var rng []string // the revisions of the commits to push
	// A branch cut from origin/main tracks origin/main, not a branch of its
	// own: only an upstream of the same name counts, since that is where the
	// push goes (and it is then set to it).
	switch up, uerr := c.Upstream(ctx, top); {
	case uerr == nil && up == "origin/"+p.Branch:
		p.Upstream = up
		p.Ahead, p.Behind, err = c.AheadBehind(ctx, top)
		if err != nil {
			return p, fmt.Errorf("%s: %w", p.Label(), err)
		}
		rng = []string{up + "..HEAD"}
	case remoteBranchExists(ctx, c, top, p.Branch):
		// The remote has the branch but this one does not track it yet.
		p.Upstream, p.SetUpstream = "origin/"+p.Branch, true
		p.Ahead, p.Behind, err = aheadBehind(ctx, c, top, "HEAD...origin/"+p.Branch)
		if err != nil {
			return p, fmt.Errorf("%s: %w", p.Label(), err)
		}
		rng = []string{"origin/" + p.Branch + "..HEAD"}
	default:
		p.Upstream, p.NewBranch, p.SetUpstream = "origin/"+p.Branch, true, true
		out, err := c.Run(ctx, top, "rev-list", "--count", "HEAD", "--not", "--remotes=origin")
		if err != nil {
			return p, fmt.Errorf("%s: %w", p.Label(), err)
		}
		if p.Ahead, err = strconv.Atoi(strings.TrimSpace(out)); err != nil {
			return p, fmt.Errorf("%s: unexpected rev-list output %q", p.Label(), out)
		}
		rng = []string{"HEAD", "--not", "--remotes=origin"}
	}
	if p.Ahead > 0 {
		args := append([]string{"log", "--format=%s", "-n", strconv.Itoa(maxPushCommits)}, rng...)
		if out, err := c.Run(ctx, top, args...); err == nil {
			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				if l != "" {
					p.Commits = append(p.Commits, l)
				}
			}
		}
	}
	switch {
	case p.Behind > 0 && p.Ahead > 0:
		p.Refuse = fmt.Sprintf("%s has %s you lack and you have %s it lacks: update the branch first (git pull --rebase); ADE never force-pushes",
			p.Upstream, plural(p.Behind, "commit"), plural(p.Ahead, "commit"))
	case p.Ahead == 0 && p.Behind > 0:
		p.Refuse, p.Nothing = fmt.Sprintf("nothing to push: %s is %s ahead of this branch", p.Upstream, plural(p.Behind, "commit")), true
	case p.Ahead == 0 && p.NewBranch:
		p.Refuse, p.Nothing = "nothing to push: "+p.Branch+" has no commits that origin does not already have", true
	case p.Ahead == 0:
		p.Refuse, p.Nothing = "nothing to push: everything is on "+p.Upstream, true
	}
	return p, nil
}

// locateTree maps a worktree's top directory to its project and repo by its
// place under the root: <root>/<project>/<repo>/<tree>.
func (s *Service) locateTree(top string) (proj, repo string, err error) {
	root, rerr := filepath.EvalSymlinks(s.App.Root)
	if rerr != nil {
		root = s.App.Root
	}
	if real, rerr := filepath.EvalSymlinks(top); rerr == nil {
		top = real
	}
	rel, rerr := filepath.Rel(root, top)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if rerr != nil || len(parts) < 3 || parts[0] == ".." {
		return "", "", fmt.Errorf("%s is not a worktree of an ADE project (expected %s/<project>/<repo>/<tree>)", top, s.App.Root)
	}
	return parts[0], parts[1], nil
}

func remoteBranchExists(ctx context.Context, c git.Client, dir, branch string) bool {
	_, err := c.Run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return err == nil
}

func aheadBehind(ctx context.Context, c git.Client, dir, rng string) (ahead, behind int, err error) {
	out, err := c.Run(ctx, dir, "rev-list", "--left-right", "--count", rng)
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", out)
	}
	if ahead, err = strconv.Atoi(f[0]); err != nil {
		return 0, 0, err
	}
	behind, err = strconv.Atoi(f[1])
	return ahead, behind, err
}

// Push sends the planned commit to origin as `git push origin
// <sha>:refs/heads/<branch>` with the forge's write token (the one pushes of
// agent runs use), then makes the branch track the remote one, and audits it
// as git.push. It sends the commit the plan was read at and refuses, changing
// nothing, when the branch has moved since the preview. It never forces. A
// plan with Refuse set is not pushed.
func (s *Service) Push(ctx context.Context, p PushPlan) error {
	if !p.Pushable() {
		return errors.New(p.Refuse)
	}
	var c git.Client
	head, herr := c.Run(ctx, p.Path, "rev-parse", "HEAD")
	branch, berr := c.Run(ctx, p.Path, "symbolic-ref", "--short", "-q", "HEAD")
	if herr != nil || berr != nil || strings.TrimSpace(head) != p.Head || strings.TrimSpace(branch) != p.Branch {
		return fmt.Errorf("%s changed since the preview (it was %s at %.8s): look again before pushing", p.Label(), p.Branch, p.Head)
	}
	bare := filepath.Join(s.App.Root, p.Project, p.Repo, ".bare")
	gc, err := s.PushClient(ctx, p.Forge, bare)
	if err == nil {
		_, err = gc.Run(ctx, p.Path, "push", "origin", p.Head+":refs/heads/"+p.Branch)
	}
	var upErr error
	if err == nil && p.SetUpstream {
		_, upErr = c.Run(ctx, p.Path, "branch", "--set-upstream-to=origin/"+p.Branch, p.Branch)
	}
	e := audit.Entry{
		Action: "git.push", Target: p.Path, Project: p.Project, Outcome: outcome(err),
		Params: map[string]any{"repo": p.Repo, "branch": p.Branch, "sha": p.Head, "ahead": p.Ahead, "new_branch": p.NewBranch, "set_upstream": p.SetUpstream && upErr == nil},
	}
	if err != nil {
		e.Error = git.Reason(err)
		s.audit(e)
		return fmt.Errorf("push %s: %s", p.Label(), git.Reason(err))
	}
	s.audit(e)
	if upErr != nil {
		return fmt.Errorf("pushed %s, but it does not track origin/%s: %s", p.Label(), p.Branch, git.Reason(upErr))
	}
	return nil
}
