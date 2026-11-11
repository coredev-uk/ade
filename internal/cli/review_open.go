package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/workspace"
)

// `ade review <ref>` (docs/PLAN.md 6.9): open the MR in a review workspace
// tab: the MR's source branch checked out as a worktree (made when it does not
// exist), then the `review` layout, which shows the diff against the target
// branch through delta beside lazygit and a shell. --no-zellij stops short of
// the tab and prints where the worktree is and the command that shows the diff.

// reviewOpened is what `ade review <ref>` reports.
type reviewOpened struct {
	Ref      forge.Ref `json:"ref"`
	Project  string    `json:"project"`
	Repo     string    `json:"repo"`
	Branch   string    `json:"branch"`
	Base     string    `json:"base"`
	Worktree string    `json:"worktree"`
	// Created is true when this run made the worktree.
	Created bool `json:"created"`
	DryRun  bool `json:"dry_run,omitempty"`
	// The tab, when one was opened; TabCreated is false when an existing tab was focused.
	Host       string `json:"host,omitempty"`
	Session    string `json:"session,omitempty"`
	Tab        string `json:"tab,omitempty"`
	Layout     string `json:"layout,omitempty"`
	TabCreated bool   `json:"tab_created"`
	// Hint is what to run by hand when no tab was opened.
	Hint string `json:"hint,omitempty"`
}

// reviewTarget is the checkout an MR belongs to.
type reviewTarget struct {
	manifest *config.Manifest
	repo     string
	bare     string
}

// findReviewRepo finds the registered project whose manifest has a repo at the
// MR's forge project.
func findReviewRepo(ctx context.Context, a *app.App, ref forge.Ref) (reviewTarget, error) {
	var hits []reviewTarget
	for _, pr := range a.Config.Projects {
		m, err := project.FetchManifest(ctx, a, pr)
		if err != nil {
			continue // an unreachable manifest must not block the others
		}
		for _, name := range slices.Sorted(mapKeys(m.Repos)) {
			if m.Repos[name].Path == ref.Project {
				hits = append(hits, reviewTarget{manifest: m, repo: name, bare: filepath.Join(a.Root, m.Name, name, ".bare")})
			}
		}
	}
	switch len(hits) {
	case 0:
		return reviewTarget{}, fmt.Errorf("no registered project has a repo at %s: `ade project add <manifest>` first", ref.Project)
	case 1:
		return hits[0], nil
	}
	var names []string
	for _, h := range hits {
		names = append(names, h.manifest.Name+"/"+h.repo)
	}
	return reviewTarget{}, fmt.Errorf("%s is a repo of several projects (%s): the review workspace needs exactly one", ref.Project, strings.Join(names, ", "))
}

func mapKeys[M ~map[string]V, V any](m M) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// reviewWorktree returns the worktree of the MR's source branch, making it
// (fetch, then a tracking worktree, then the repo's post_worktree hooks) when
// it does not exist yet. An existing tree is only brought up to date when that
// is a plain fast-forward of a clean tree: someone's work in it is never touched.
func reviewWorktree(ctx context.Context, a *app.App, t reviewTarget, mr forge.MR, dry bool) (path string, created bool, err error) {
	gc, err := project.New(a, "user").GitClient(ctx, t.manifest.Forge, t.bare)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(filepath.Join(t.bare, "HEAD")); err != nil {
		return "", false, fmt.Errorf("%s is not cloned: run `ade project add` for %s first", t.repo, t.manifest.Name)
	}
	branch := mr.SourceBranch
	if branch == "" {
		return "", false, errors.New("the merge request has no source branch to check out")
	}
	if err := gc.PruneWorktrees(ctx, t.bare); err != nil {
		return "", false, err
	}
	trees, err := gc.Worktrees(ctx, t.bare)
	if err != nil {
		return "", false, err
	}
	dir := filepath.Join(a.Root, t.manifest.Name, t.repo, git.BranchDir(branch))
	if i := slices.IndexFunc(trees, func(w git.Worktree) bool { return w.Branch == branch }); i >= 0 {
		dir = trees[i].Path
		if !dry {
			refreshReviewTree(ctx, gc, t.bare, dir)
		}
		return dir, false, nil
	}
	if _, err := os.Stat(dir); err == nil {
		return "", false, fmt.Errorf("%s exists but is not a worktree of %s", dir, branch)
	}
	if dry {
		return dir, true, nil
	}
	err = gc.Fetch(ctx, t.bare)
	logWorktree(a, "git.fetch", t.bare, t.manifest.Name, mr.Ref, nil, err)
	if err != nil {
		return "", false, err
	}
	err = gc.AddTracking(ctx, t.bare, dir, branch)
	logWorktree(a, "git.worktree.add", dir, t.manifest.Name, mr.Ref, map[string]any{"branch": branch, "repo": t.repo, "for": "review"}, err)
	if err != nil {
		return "", false, fmt.Errorf("check out %s: %w", branch, err)
	}
	for _, hook := range t.manifest.Repos[t.repo].Hooks.PostWorktree {
		_, herr := git.RunHook(ctx, dir, hook)
		logWorktree(a, "git.hook", dir, t.manifest.Name, mr.Ref, map[string]any{"hook": "post_worktree", "cmd": hook}, herr)
	}
	return dir, true, nil
}

// refreshReviewTree fetches and fast-forwards a clean tree that is behind its
// upstream, so the review shows the MR's current head. Failures are not fatal:
// the tree is still there to look at.
func refreshReviewTree(ctx context.Context, gc git.Client, bare, dir string) {
	if err := gc.Fetch(ctx, bare); err != nil {
		return
	}
	if clean, err := gc.IsClean(ctx, dir); err != nil || !clean {
		return
	}
	if ahead, behind, err := gc.AheadBehind(ctx, dir); err == nil && ahead == 0 && behind > 0 {
		_ = gc.FastForward(ctx, dir)
	}
}

func logWorktree(a *app.App, action, target, project string, ref forge.Ref, params map[string]any, err error) {
	e := audit.Entry{Action: action, Target: target, Item: ref.String(), Project: project, Params: params, Outcome: "ok"}
	if err != nil {
		e.Outcome, e.Error = "error", err.Error()
	}
	_ = a.Audit.Append(e)
}

// reviewDiffCommand is what shows the review diff in a terminal.
func reviewDiffCommand(worktree, base string) string {
	diff := "git diff " + shQuote(base) + "...HEAD"
	if _, err := exec.LookPath("delta"); err == nil {
		diff += " | delta"
	}
	return "cd " + shQuote(worktree) + " && " + diff
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func runReviewOpen(cmd *cobra.Command, arg string, noZellij bool, session string) error {
	c, err := openMR(cmd, arg)
	if err != nil {
		return err
	}
	ctx, a := cmd.Context(), appFrom(cmd)
	mr, err := c.be.MR(ctx, c.ref)
	if err != nil {
		return fmt.Errorf("read %s: %w", c.ref, err)
	}
	t, err := findReviewRepo(ctx, a, c.ref)
	if err != nil {
		return err
	}
	tree, created, err := reviewWorktree(ctx, a, t, mr, c.dry)
	if err != nil {
		return err
	}
	res := reviewOpened{
		Ref: c.ref, Project: t.manifest.Name, Repo: t.repo, Branch: mr.SourceBranch,
		Base: "origin/" + mr.TargetBranch, Worktree: tree, Created: created, DryRun: c.dry,
	}
	w := cmd.OutOrStdout()
	if c.dry || noZellij {
		res.Hint = reviewDiffCommand(tree, res.Base)
		return printReviewOpened(w, res, c.dry)
	}
	opts := []workspace.OpenOption{
		workspace.WithLayout(workspace.LayoutReview), workspace.WithBaseRef(res.Base),
		workspace.WithTitle(fmt.Sprintf("review-%d", c.ref.IID)),
	}
	if session != "" {
		opts = append(opts, workspace.WithSession(session))
	}
	open, err := workspace.Open(ctx, a, t.manifest.Name, t.repo, tree, opts...)
	res.Host, res.Session, res.Tab, res.Layout, res.TabCreated = open.Host, open.Session, open.Tab, open.Layout, open.Created
	if open.Hint != "" { // a host without tabs: the diff command is the useful hint, not the workspace one
		res.Hint = reviewDiffCommand(tree, res.Base)
	}
	if err != nil {
		if jsonOut {
			_ = printJSON(w, res)
		}
		return err
	}
	return printReviewOpened(w, res, false)
}

func printReviewOpened(w interface{ Write([]byte) (int, error) }, r reviewOpened, dry bool) error {
	if jsonOut {
		return printJSON(w, r)
	}
	made := "worktree"
	switch {
	case dry && r.Created:
		made = "dry-run: would create the worktree"
	case dry:
		made = "dry-run: worktree exists"
	case r.Created:
		made = "created worktree"
	}
	fmt.Fprintf(w, "%s %s (%s, branch %s, base %s)\n", r.Ref, made, r.Worktree, r.Branch, r.Base)
	switch {
	case r.Host == workspace.HostZellij && !r.TabCreated:
		fmt.Fprintf(w, "focused tab %s in session %s\n", r.Tab, r.Session)
	case r.Host == workspace.HostZellij:
		fmt.Fprintf(w, "opened tab %s in session %s (layout %s)\n", r.Tab, r.Session, r.Layout)
	case r.Host == workspace.HostVSCode:
		fmt.Fprintf(w, "opened %s in VS Code\n", r.Worktree)
	case r.Hint != "":
		fmt.Fprintln(w, r.Hint)
	}
	return nil
}
