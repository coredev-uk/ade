package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
)

const (
	cleanupWebProject = "ade-demo-env/web"
	cleanupWebBranch  = "2-fix-truncated-column-headers-in-export-preview"
)

// cleanupEnv is the gc layout with the web item cached (tracker#2, its MR
// web!2 merged in the fake), a baseline tree on web's main, and a newer commit
// on web's origin/main that the baseline has not seen.
func cleanupEnv(t *testing.T) (e *cliEnv, web, baseline string) {
	t.Helper()
	e = newCLIEnv(t)
	e.useFakeForge()
	_, web = e.gcLayout()
	tr := forge.Ref{Project: adoptTracker, IID: 2, Kind: forge.KindIssue}
	e.cacheDemoGL(domain.WorkItem{
		Project: "demo-gl", Ref: tr, Issue: forge.Issue{Ref: tr, Title: "Truncated column headers"},
		MRs: []forge.MR{{Ref: forge.Ref{Project: cleanupWebProject, IID: 2, Kind: forge.KindMR}, State: "merged", SourceBranch: cleanupWebBranch}},
	})
	repoDir := filepath.Join(e.app.Root, "demo-gl", "web")
	baseline = filepath.Join(repoDir, "main")
	if err := (git.Client{}).AddBaseline(context.Background(), filepath.Join(repoDir, ".bare"), baseline, "main"); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(filepath.Dir(e.app.Root), "web-work")
	gitAt(t, work, "commit", "--quiet", "--allow-empty", "-m", "merged change")
	gitAt(t, work, "push", "--quiet", "origin", "main")
	return e, web, baseline
}

func (e *cliEnv) cleanup(args ...string) (string, error) { return e.run(newCleanupCmd, args...) }

func TestCleanupDryRunTouchesNothing(t *testing.T) {
	e, web, baseline := cleanupEnv(t)
	before := gitOut(t, baseline, "rev-parse", "HEAD")

	out, err := e.cleanup(adoptTracker+"#2", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"remove  demo-gl/web/" + cleanupWebBranch, "would fast-forward demo-gl/web/main (1 new)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(web); err != nil {
		t.Error("--dry-run removed the worktree")
	}
	if gitOut(t, baseline, "rev-parse", "HEAD") != before {
		t.Error("--dry-run moved the baseline")
	}
	if got := e.audited("worktree.remove"); len(got) != 0 {
		t.Errorf("--dry-run audited a removal: %+v", got)
	}
}

func TestCleanupRemovesTreeFastForwardsBaselineAndDeletesBranch(t *testing.T) {
	e, web, baseline := cleanupEnv(t)
	if out, err := e.cleanup(adoptTracker + "#2"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("unattended without --yes: %v\n%s", err, out)
	}

	asJSON(t)
	out, err := e.cleanup(cleanupWebProject+"!2", "--yes") // the MR resolves to the same item
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got cleanupOut
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got.Item != adoptTracker+"#2" || len(got.Trees) != 1 || !got.Trees[0].Removed || got.Trees[0].BranchKept {
		t.Fatalf("result = %+v", got)
	}
	if len(got.Baselines) != 1 || got.Baselines[0].Action != "fast-forward" || !got.Baselines[0].Done {
		t.Fatalf("baselines = %+v", got.Baselines)
	}
	if _, err := os.Stat(web); err == nil {
		t.Error("the worktree is still there")
	}
	if subject := gitOut(t, baseline, "log", "-1", "--format=%s"); subject != "merged change" {
		t.Errorf("baseline at %q, want the merged change", subject)
	}
	bare := filepath.Join(e.app.Root, "demo-gl", "web", ".bare")
	if branches := gitOut(t, bare, "branch", "--list", cleanupWebBranch); branches != "" {
		t.Errorf("local branch survived: %q", branches)
	}
	for _, action := range []string{"worktree.remove", "git.branch.delete", "git.pull"} {
		if es := e.audited(action); len(es) != 1 || es[0].Outcome != "ok" {
			t.Errorf("audit %s = %+v", action, es)
		}
	}
}

func TestCleanupRefusesADirtyOrUnpushedTree(t *testing.T) {
	for name, dirty := range map[string]func(t *testing.T, web string){
		"dirty": func(t *testing.T, web string) {
			if err := os.WriteFile(filepath.Join(web, "wip.txt"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"unpushed": func(t *testing.T, web string) {
			gitAt(t, web, "commit", "--quiet", "--allow-empty", "-m", "local only")
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, web, baseline := cleanupEnv(t)
			dirty(t, web)
			before := gitOut(t, baseline, "rev-parse", "HEAD")

			out, err := e.cleanup(adoptTracker+"#2", "--yes")
			if err == nil || !isExit(err, 2) || !strings.Contains(out, "keep    demo-gl/web/"+cleanupWebBranch) {
				t.Fatalf("err %v\n%s", err, out)
			}
			if _, err := os.Stat(web); err != nil {
				t.Fatal("a refused cleanup removed the tree")
			}
			if gitOut(t, baseline, "rev-parse", "HEAD") != before {
				t.Error("a refused cleanup moved the baseline")
			}
			if branches := gitOut(t, filepath.Join(e.app.Root, "demo-gl", "web", ".bare"), "branch", "--list", cleanupWebBranch); branches == "" {
				t.Error("a refused cleanup deleted the branch")
			}
			if got := e.audited("worktree.remove"); len(got) != 0 {
				t.Errorf("audit = %+v", got)
			}
			if !strings.Contains(out, "uncommitted changes") && !strings.Contains(out, "not pushed") {
				t.Errorf("no reason given:\n%s", out)
			}
		})
	}
}

func TestCleanupNeedsACachedItemAndSaysWhenThereAreNoTrees(t *testing.T) {
	e, _, _ := cleanupEnv(t)
	if _, err := e.cleanup(adoptTracker+"#99", "--yes"); err == nil || !strings.Contains(err.Error(), "ade sync") {
		t.Errorf("uncached: %v", err)
	}
}

// headForge answers every MR with a fixed head commit, as a forge does for the
// real source branch (the fake's are synthetic).
type headForge struct {
	forge.Forge
	head *string
}

func (f headForge) ListMRs(ctx context.Context, q forge.MRQuery) ([]forge.MR, error) {
	mrs, err := f.Forge.ListMRs(ctx, q)
	for i := range mrs {
		mrs[i].HeadSHA = *f.head
	}
	return mrs, err
}

// squashedEnv is cleanupEnv with the web branch carrying a commit that was
// pushed and then squash-merged: main never gets the commit, so git does not
// see the branch as merged, but the merged MR's head is the branch tip.
func squashedEnv(t *testing.T) (e *cliEnv, web, bare string, mrHead *string) {
	t.Helper()
	e, web, _ = cleanupEnv(t)
	gitAt(t, web, "commit", "--quiet", "--allow-empty", "-m", "squashed away")
	gitAt(t, web, "push", "--quiet", "origin", "HEAD:refs/heads/"+cleanupWebBranch)
	gitAt(t, web, "branch", "--unset-upstream") // the forge deleted the merged branch: git judges against main
	head := gitOut(t, web, "rev-parse", "HEAD")
	mrHead = &head
	open := openGCForge
	openGCForge = func(a *app.App) func(context.Context, string, config.Forge) (forge.Forge, error) {
		inner := open(a)
		return func(ctx context.Context, name string, f config.Forge) (forge.Forge, error) {
			fg, err := inner(ctx, name, f)
			return headForge{Forge: fg, head: mrHead}, err
		}
	}
	t.Cleanup(func() { openGCForge = open })
	return e, web, filepath.Join(e.app.Root, "demo-gl", "web", ".bare"), mrHead
}

func TestCleanupDeletesASquashMergedBranch(t *testing.T) {
	e, web, bare, _ := squashedEnv(t)
	out, err := e.cleanup(adoptTracker+"#2", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "deleted branch " + cleanupWebBranch + " with git branch -D (its tip is web!2's head)"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if _, err := os.Stat(web); err == nil {
		t.Error("the worktree is still there")
	}
	if branches := gitOut(t, bare, "branch", "--list", cleanupWebBranch); branches != "" {
		t.Errorf("the squash-merged branch survived: %q", branches)
	}
	es := e.audited("git.branch.delete")
	if len(es) != 1 || es[0].Outcome != "ok" || es[0].Params["forced"] != true {
		t.Errorf("audit git.branch.delete = %+v", es)
	}
}

func TestCleanupDryRunSaysWhichBranchesNeedDashD(t *testing.T) {
	e, web, bare, _ := squashedEnv(t)
	out, err := e.cleanup(adoptTracker+"#2", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "would delete branch " + cleanupWebBranch + " (git branch -D unless git sees it merged by then: its tip is web!2's head)"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if _, err := os.Stat(web); err != nil {
		t.Error("--dry-run removed the worktree")
	}
	if gitOut(t, bare, "branch", "--list", cleanupWebBranch) == "" {
		t.Error("--dry-run deleted the branch")
	}
	if got := e.audited("git.branch.delete"); len(got) != 0 {
		t.Errorf("--dry-run audited a deletion: %+v", got)
	}
}

func TestCleanupKeepsABranchWithCommitsOutsideTheMergedMR(t *testing.T) {
	e, _, bare, mrHead := squashedEnv(t)
	// The MR was merged at an earlier head: the tree has one commit more, pushed.
	*mrHead = gitOut(t, bare, "rev-parse", cleanupWebBranch+"~1")
	out, err := e.cleanup(adoptTracker+"#2", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "branch " + cleanupWebBranch + " kept: local commits not in the merged MR web!2"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if gitOut(t, bare, "branch", "--list", cleanupWebBranch) == "" {
		t.Error("a branch with commits outside the merged MR was deleted")
	}
	if got := e.audited("git.branch.delete"); len(got) != 0 {
		t.Errorf("audit = %+v", got)
	}
}

func TestGCDryRunSaysWhichBranchesNeedDashD(t *testing.T) {
	e, web, bare, mrHead := squashedEnv(t)
	out, err := e.gc("--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "would delete branch " + cleanupWebBranch + " (git branch -D unless git sees it merged by then: its tip is web!2's head)"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if _, err := os.Stat(web); err != nil {
		t.Error("--dry-run removed the worktree")
	}
	if gitOut(t, bare, "branch", "--list", cleanupWebBranch) == "" {
		t.Error("--dry-run deleted the branch")
	}

	*mrHead = gitOut(t, bare, "rev-parse", cleanupWebBranch+"~1")
	out, err = e.gc("--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "would delete branch " + cleanupWebBranch + " only if git sees it merged (local commits not in the merged MR web!2)"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}
