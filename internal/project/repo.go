package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/git"
)

const featureNotifyOnly = "notify-only"

// repoSync carries the state of syncing one repo.
type repoSync struct {
	s       *Service
	run     *run
	name    string
	repo    config.Repo
	m       mode
	dir     string // <root>/<project>/<repo>
	bare    string
	git     git.Client
	res     RepoResult
	baseDir string // baseline worktree path, once known
}

// syncRepo makes sure one repo is cloned, has its baseline worktree, and (when
// updating) is fetched and fast-forwarded. A failure stops this repo only.
func (s *Service) syncRepo(ctx context.Context, r *run, name string, m mode) RepoResult {
	rs := &repoSync{s: s, run: r, name: name, repo: r.manifest.Repos[name], m: m,
		dir: filepath.Join(r.dir, name), res: RepoResult{Project: r.name, Repo: name}}
	rs.bare = filepath.Join(rs.dir, ".bare")
	rs.sync(ctx)
	return rs.res
}

func (rs *repoSync) sync(ctx context.Context) {
	fresh, ok := rs.ensureBare(ctx)
	if !ok {
		return
	}
	if !fresh && !rs.fetch(ctx) {
		return
	}
	if err := git.WriteGitFile(rs.dir); err != nil {
		rs.fail(ctx, "clone", "git.clone.failed", "", fmt.Errorf("write .git file: %w", err))
		return
	}
	created, ok := rs.ensureBaseline(ctx)
	if !ok {
		return
	}
	if created {
		rs.runHooks(ctx)
	}
	if rs.m == modeUpdate && !fresh {
		rs.pullAll(ctx, created)
	}
}

// ensureBare clones and prepares the bare repo when it is missing or was left
// half-prepared. fresh reports that a fetch just happened, so none is needed.
func (rs *repoSync) ensureBare(ctx context.Context) (fresh, ok bool) {
	url, err := rs.originURL(ctx)
	if err != nil {
		rs.fail(ctx, "clone", "git.clone.failed", "", err)
		return false, false
	}
	rs.git, err = rs.s.client(ctx, rs.run.manifest.Forge, url)
	if err != nil {
		rs.fail(ctx, "clone", "git.clone.failed", "", err)
		return false, false
	}
	hadBare := exists(filepath.Join(rs.bare, "HEAD"))
	if hadBare && rs.git.Prepared(ctx, rs.bare) {
		return false, true
	}
	if !hadBare {
		if err := rs.clone(ctx, url); err != nil {
			rs.fail(ctx, "clone", "git.clone.failed", "", err)
			return false, false
		}
	}
	if _, err := rs.git.PrepareBare(ctx, rs.bare, rs.repo.DefaultBranch); err != nil {
		rs.fail(ctx, "clone", "git.clone.failed", "", err)
		return false, false
	}
	rs.res.Cloned = true
	rs.s.publish("git.clone.ok", events.Success, rs.run.name, rs.name, "cloned "+rs.run.name+"/"+rs.name, "")
	rs.s.audit(audit.Entry{Action: "git.clone", Target: url, Project: rs.run.name,
		Params: map[string]any{"repo": rs.name}})
	return true, true
}

// originURL prefers the URL the existing clone already uses over recomputing it,
// so a forge config change cannot silently repoint a repo.
func (rs *repoSync) originURL(ctx context.Context) (string, error) {
	if exists(filepath.Join(rs.bare, "HEAD")) {
		if u, err := (git.Client{}).RemoteURL(ctx, rs.bare); err == nil && u != "" {
			return u, nil
		}
	}
	return cloneURL(rs.s.App.Config.Forges[rs.run.manifest.Forge], rs.repo)
}

func (rs *repoSync) clone(ctx context.Context, url string) error {
	if err := os.MkdirAll(rs.dir, 0o755); err != nil {
		return err
	}
	if err := rs.git.CloneBare(ctx, url, rs.bare); err != nil {
		os.RemoveAll(rs.bare) //nolint:errcheck // a failed clone must not look like a repo on the next run
		return err
	}
	return nil
}

func (rs *repoSync) fetch(ctx context.Context) bool {
	if err := rs.git.Fetch(ctx, rs.bare); err != nil {
		rs.fail(ctx, "fetch", "git.fetch.failed", rs.name, err)
		return false
	}
	return true
}

func (rs *repoSync) ensureBaseline(ctx context.Context) (created, ok bool) {
	def, err := rs.git.DefaultBranch(ctx, rs.bare)
	if err != nil {
		rs.fail(ctx, "clone", "git.clone.failed", "", err)
		return false, false
	}
	rs.baseDir = filepath.Join(rs.dir, git.BranchDir(def))
	if exists(rs.baseDir) {
		return false, true
	}
	if err := rs.git.AddBaseline(ctx, rs.bare, rs.baseDir, def); err != nil {
		rs.fail(ctx, "clone", "git.clone.failed", rs.name+"/"+git.BranchDir(def), err)
		return false, false
	}
	return true, true
}

func (rs *repoSync) runHooks(ctx context.Context) {
	for _, cmd := range rs.repo.Hooks.PostClone {
		out, err := git.RunHook(ctx, rs.baseDir, cmd)
		e := audit.Entry{Action: "git.hook", Target: rs.baseDir, Project: rs.run.name,
			Params: map[string]any{"hook": "post_clone", "cmd": cmd}}
		if err != nil {
			e.Outcome, e.Error = "error", err.Error()
			rs.s.audit(e)
			rs.res.Failures = append(rs.res.Failures, Failure{Op: "hook", Reason: "hook failed", Detail: out})
			rs.s.publish("git.hook.failed", events.Error, rs.run.name, rs.name,
				fmt.Sprintf("post_clone hook failed in %s: %s", rs.name, cmd), err.Error()+"\n"+out)
			return
		}
		rs.s.audit(e)
	}
}

// pullAll fast-forwards the baseline and then the feature worktrees.
func (rs *repoSync) pullAll(ctx context.Context, baselineCreated bool) {
	if !baselineCreated {
		rs.pull(ctx, rs.baseDir, true)
	}
	trees, err := rs.git.Worktrees(ctx, rs.bare)
	if err != nil {
		rs.fail(ctx, "pull", "git.pull.failed", rs.name, err)
		return
	}
	write := rs.s.App.Config.Update.FeatureBranches != featureNotifyOnly
	for _, t := range trees {
		if t.Branch == "" || filepath.Clean(t.Path) == filepath.Clean(rs.baseDir) {
			continue
		}
		rs.pull(ctx, t.Path, write)
	}
}

// pull fast-forwards one tree when that is safe. Anything dirty, ahead or
// diverged is left alone and reported; ADE never merges, rebases or stashes.
func (rs *repoSync) pull(ctx context.Context, tree string, allowWrite bool) {
	subject := rs.name + "/" + filepath.Base(tree)
	ahead, behind, err := rs.git.AheadBehind(ctx, tree)
	switch {
	case errors.Is(err, git.ErrNoUpstream):
		return // an unpushed branch is normal; status shows it
	case err != nil:
		rs.fail(ctx, "pull", "git.pull.failed", subject, err)
		return
	case behind == 0:
		return
	}
	counts := fmt.Sprintf("↑%d ↓%d", ahead, behind)
	reason := ""
	switch {
	case !allowWrite:
		reason = featureNotifyOnly
	case ahead > 0:
		reason = "diverged"
	default:
		clean, err := rs.git.IsClean(ctx, tree)
		if err != nil {
			rs.fail(ctx, "pull", "git.pull.failed", subject, err)
			return
		}
		if !clean {
			reason = "dirty"
		}
	}
	if reason != "" {
		sev := events.Warn
		if reason == featureNotifyOnly {
			sev = events.Info
		}
		rs.res.Skipped = append(rs.res.Skipped, Skip{Tree: subject, Reason: reason})
		rs.s.publish("git.pull.skipped", sev, rs.run.name, subject,
			fmt.Sprintf("%s not updated: %s (%s)", subject, reason, counts), "")
		return
	}
	rs.fastForward(ctx, tree, subject, behind)
}

func (rs *repoSync) fastForward(ctx context.Context, tree, subject string, behind int) {
	from, _ := rs.git.Head(ctx, tree)
	if err := rs.git.FastForward(ctx, tree); err != nil {
		rs.fail(ctx, "pull", "git.pull.failed", subject, err)
		return
	}
	to, _ := rs.git.Head(ctx, tree)
	rs.res.Pulled = append(rs.res.Pulled, subject)
	rs.s.publish("git.pull.ok", events.Success, rs.run.name, subject,
		fmt.Sprintf("%s fast-forwarded (%d new)", subject, behind), "")
	rs.s.audit(audit.Entry{Action: "git.pull", Target: tree, Project: rs.run.name,
		Params: map[string]any{"from": from, "to": to, "commits": behind}})
}

// fail records a failure, publishes its event with git's stderr as detail, and
// audits it. subject narrows the event to a tree or URL when relevant.
func (rs *repoSync) fail(ctx context.Context, op, kind, subject string, err error) {
	reason := git.Reason(err)
	rs.res.Failures = append(rs.res.Failures, Failure{Op: op, Reason: reason, Detail: err.Error()})
	if subject == "" {
		subject = rs.name
	}
	rs.s.publish(kind, events.Error, rs.run.name, rs.name, fmt.Sprintf("%s %s failed: %s", op, subject, reason), err.Error())
	if ctx.Err() == nil {
		// Cancellation is the user's doing, not a repo fault worth auditing.
		rs.s.audit(audit.Entry{Action: "git." + op, Target: subject, Project: rs.run.name, Outcome: "error", Error: err.Error()})
	}
}
