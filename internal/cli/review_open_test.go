package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/startwork/swtest"
	"github.com/coredev-uk/ade/internal/workspace"
)

// reviewOpenEnv is the fake forge plus a cloned api repo laid out like
// `ade project add` leaves it, whose remote has the MR's source branch.
func reviewOpenEnv(t *testing.T) (e *cliEnv, root string) {
	t.Helper()
	swtest.GitEnv(t)
	e = newCLIEnv(t)
	root = t.TempDir()
	e.app.Root = root
	manifest := []byte(`name = "demo-gl"
forge = "sandbox-gitlab"

[tracker]
project = "ade-demo-env/tracker"

[repos.api]
path = "ade-demo-env/api"
app  = ["api"]
`)
	mpath := filepath.Join(t.TempDir(), "demo.ade.toml")
	if err := os.WriteFile(mpath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.Config.Forges["sandbox-gitlab"] = config.Forge{Kind: "gitlab", URL: e.srv.URL}
	e.app.Config.Projects = []config.ProjectRef{{Source: mpath}}

	// The snapshot `ade project add` writes: workspace.Locate reads it.
	var snap strings.Builder
	if err := toml.NewEncoder(&snap).Encode(map[string]string{"source": mpath, "manifest": string(manifest)}); err != nil {
		t.Fatal(err)
	}
	pdir := filepath.Join(root, "demo-gl")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, ".project.toml"), []byte(snap.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	remote := swtest.NewRemote(t, t.TempDir(), "api")
	remote.Branch("rename-bucket-to-token-bucket")
	swtest.Clone(t, root, "demo-gl", "api", remote)
	return e, root
}

func TestReviewOpenNoZellijChecksOutTheSourceBranchAndPrintsTheDiffCommand(t *testing.T) {
	e, root := reviewOpenEnv(t)
	tree := filepath.Join(root, "demo-gl", "api", "rename-bucket-to-token-bucket")

	out, err := e.review(renameMR, "--no-zellij", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dry-run: would create the worktree") || !strings.Contains(out, tree) {
		t.Fatalf("dry run:\n%s", out)
	}
	if _, err := os.Stat(tree); err == nil {
		t.Fatal("--dry-run made the worktree")
	}

	out, err = e.review(renameMR, "--no-zellij")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"created worktree", tree, "branch rename-bucket-to-token-bucket", "base origin/" + "1-rate-limit-the-export-endpoint", "cd '" + tree + "' && git diff 'origin/1-rate-limit-the-export-endpoint'...HEAD"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	branch, err := exec.Command("git", "-C", tree, "branch", "--show-current").Output()
	if err != nil || strings.TrimSpace(string(branch)) != "rename-bucket-to-token-bucket" {
		t.Fatalf("branch = %q, %v", branch, err)
	}
	if es := e.audited("git.worktree.add"); len(es) != 1 || es[0].Params["for"] != "review" {
		t.Fatalf("audit: %+v", es)
	}

	// A second run finds it; --json says so.
	jsonOut = true
	t.Cleanup(func() { jsonOut = false })
	out, err = e.review(renameMR, "--no-zellij")
	if err != nil {
		t.Fatal(err)
	}
	var got reviewOpened
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got.Created || got.Worktree != tree || got.Repo != "api" || got.Project != "demo-gl" || got.Ref.IID != 2 || got.Tab != "" {
		t.Fatalf("%+v", got)
	}
	if len(e.audited("git.worktree.add")) != 1 {
		t.Error("the second run made another worktree")
	}
}

func TestReviewOpenOpensTheReviewLayoutTab(t *testing.T) {
	e, _ := reviewOpenEnv(t)
	bin := t.TempDir()
	log := filepath.Join(bin, "zellij.log")
	// A zellij that has the session and no tabs, and records what it is told.
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do case \"$a\" in list-sessions) echo 'ade-test-review [Created 1s ago]'; exit 0;; query-tab-names) exit 0;; esac; done\n" +
		"printf '%s\\n' \"$*\" >> '" + log + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "zellij"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e.app.Config.Host.Mode = "zellij"
	e.app.Paths.CacheDir = t.TempDir()

	out, err := e.review(renameMR, "--session", "ade-test-review")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "opened tab api/review-2 in session ade-test-review (layout review)") {
		t.Fatalf("output:\n%s", out)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "--session ade-test-review action new-tab --layout") || !strings.Contains(string(calls), "--name api/review-2") {
		t.Fatalf("zellij calls:\n%s", calls)
	}
	files, _ := filepath.Glob(filepath.Join(e.app.Paths.CacheDir, "ws", "layouts", "*.kdl"))
	if len(files) != 1 {
		t.Fatalf("layout files: %v", files)
	}
	layout, _ := os.ReadFile(files[0])
	if !strings.Contains(string(layout), "git diff 'origin/1-rate-limit-the-export-endpoint'...HEAD") || !strings.Contains(string(layout), `name="diff"`) {
		t.Errorf("not the review layout:\n%s", layout)
	}
}

func TestReviewOpenNeedsAClonedRepoOfARegisteredProject(t *testing.T) {
	e, _ := reviewOpenEnv(t)
	if _, err := e.review(fake.GroupPath+"/elsewhere!1", "--no-zellij", "--forge", "sandbox-gitlab"); err == nil {
		t.Fatal("an MR of an unknown project opened")
	}
	e.app.Config.Projects = nil
	_, err := e.review(renameMR, "--no-zellij", "--forge", "sandbox-gitlab")
	if err == nil || !strings.Contains(err.Error(), "ade project add") {
		t.Fatalf("err = %v", err)
	}
}

func TestReviewSubcommandsStillWork(t *testing.T) {
	e := newCLIEnv(t)
	out, err := e.review("list", renameMR)
	if err != nil || !strings.Contains(out, "no pending review") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// forkMR adds a fork MR to the fake (api!3) and serves its head as
// refs/merge-requests/3/head in the clone's remote, which is where GitLab keeps
// it; the fork's branch is not a branch of the remote.
func forkMR(t *testing.T, e *cliEnv, root string) (ref, head string) {
	t.Helper()
	r, err := e.srv.AddForkMR("ade-demo-env/api", "contrib/api", "fix-typo", "Fix a typo")
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(root, "demo-gl", "api", ".bare")
	origin := strings.TrimPrefix(gitOut(t, bare, "remote", "get-url", "origin"), "file://")
	head = gitOut(t, origin, "commit-tree", "main^{tree}", "-p", "main", "-m", "fork work")
	gitAt(t, origin, "update-ref", "refs/merge-requests/3/head", head)
	return r.String(), head
}

func TestReviewOpenForkMRIsADetachedTreeAtTheHeadRef(t *testing.T) {
	e, root := reviewOpenEnv(t)
	ref, head := forkMR(t, e, root)
	tree := filepath.Join(root, "demo-gl", "api", "mr-3")

	out, err := e.review(ref, "--no-zellij", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dry-run: would create the worktree") || !strings.Contains(out, tree) || !strings.Contains(out, "detached at the MR head") {
		t.Fatalf("dry run:\n%s", out)
	}
	if _, err := os.Stat(tree); err == nil {
		t.Fatal("--dry-run made the worktree")
	}

	out, err = e.review(ref, "--no-zellij")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"created worktree", tree, "detached at the MR head", "fork MR from contrib/api", "fix-typo", "ADE offers no push", "git diff 'origin/main'...HEAD"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if err := exec.Command("git", "-C", tree, "symbolic-ref", "-q", "HEAD").Run(); err == nil {
		t.Fatal("the tree is on a branch, want detached")
	}
	if got := gitOut(t, tree, "rev-parse", "HEAD"); got != head {
		t.Fatalf("tree at %s, head ref %s", got, head)
	}
	if es := e.audited("git.worktree.add"); len(es) != 1 || es[0].Params["for"] != "mr checkout" {
		t.Fatalf("audit: %+v", es)
	}

	asJSON(t)
	out, err = e.review(ref, "--no-zellij")
	if err != nil {
		t.Fatal(err)
	}
	var got reviewOpened
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got.Created || !got.Detached || !got.Fork || got.SourceProject != "contrib/api" || got.Worktree != tree {
		t.Fatalf("second run = %+v", got)
	}
}

func TestReviewOpenForkMROpensTheReviewTab(t *testing.T) {
	e, root := reviewOpenEnv(t)
	ref, _ := forkMR(t, e, root)
	bin := t.TempDir()
	log := filepath.Join(bin, "zellij.log")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do case \"$a\" in list-sessions) echo 'ade-test-review [Created 1s ago]'; exit 0;; query-tab-names) exit 0;; esac; done\n" +
		"printf '%s\\n' \"$*\" >> '" + log + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "zellij"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e.app.Config.Host.Mode = "zellij"
	e.app.Paths.CacheDir = t.TempDir()

	out, err := e.review(ref, "--session", "ade-test-review")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "opened tab api/review-3 in session ade-test-review (layout review)") {
		t.Fatalf("output:\n%s", out)
	}
	files, _ := filepath.Glob(filepath.Join(e.app.Paths.CacheDir, "ws", "layouts", "*.kdl"))
	if len(files) != 1 {
		t.Fatalf("layout files: %v", files)
	}
	layout, _ := os.ReadFile(files[0])
	if !strings.Contains(string(layout), "git diff 'origin/main'...HEAD") || !strings.Contains(string(layout), "mr-3") {
		t.Errorf("not the review layout of the detached tree:\n%s", layout)
	}
}

// A same-project MR whose source branch is gone from origin falls back the same way.
func TestReviewOpenMissingSourceBranchFallsBackToTheHeadRef(t *testing.T) {
	e, root := reviewOpenEnv(t)
	bare := filepath.Join(root, "demo-gl", "api", ".bare")
	origin := strings.TrimPrefix(gitOut(t, bare, "remote", "get-url", "origin"), "file://")
	gitAt(t, origin, "branch", "-D", "rename-bucket-to-token-bucket")
	head := gitOut(t, origin, "commit-tree", "main^{tree}", "-p", "main", "-m", "head of !2")
	gitAt(t, origin, "update-ref", "refs/merge-requests/2/head", head)

	out, err := e.review(renameMR, "--no-zellij")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	tree := filepath.Join(root, "demo-gl", "api", "mr-2")
	if !strings.Contains(out, tree) || !strings.Contains(out, "is not in origin") || gitOut(t, tree, "rev-parse", "HEAD") != head {
		t.Fatalf("output:\n%s", out)
	}
}

// `w` on the MR page opens a fork MR's detached tree through TargetForForkMR:
// the review layout against the target branch, no branch to push.
func TestTargetForForkMROpensTheDetachedTreeWithTheReviewLayout(t *testing.T) {
	e, root := reviewOpenEnv(t)
	ref, head := forkMR(t, e, root)
	fref := forge.Ref{Project: "ade-demo-env/api", IID: 3, Kind: forge.KindMR}
	if ref != fref.String() {
		t.Fatalf("fork MR is %s", ref)
	}
	mr, err := gitlab.New(e.srv.URL, fake.Token).MR(context.Background(), fref)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := findReviewRepo(context.Background(), e.app, fref)
	if err != nil {
		t.Fatal(err)
	}
	got, err := workspace.TargetForForkMR(context.Background(), e.app, tg.manifest, mr)
	if err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(root, "demo-gl", "api", "mr-3")
	if got.Worktree != tree || got.Title != "review-3" || got.Layout != workspace.LayoutReview || got.BaseRef != "origin/main" || got.Repo != "api" {
		t.Fatalf("target = %+v", got)
	}
	if gitOut(t, tree, "rev-parse", "HEAD") != head {
		t.Fatal("the tree is not at the MR head")
	}
}

func TestMRShowSaysFork(t *testing.T) {
	e, root := reviewOpenEnv(t)
	ref, _ := forkMR(t, e, root)
	out := e.mustMR("show", ref)
	if !strings.Contains(out, "fork: from contrib/api") {
		t.Fatalf("mr show:\n%s", out)
	}
	if plain := e.mustMR("show", renameMR); strings.Contains(plain, "fork:") {
		t.Fatalf("same-project MR says fork:\n%s", plain)
	}
}

// A fork's code must not run on a key press: its tree gets no post_worktree
// hook (an install script is the contributor's code), and the output says how
// to run them. A same-project MR's tree still gets them.
func TestForkMRTreeRunsNoPostWorktreeHook(t *testing.T) {
	e, root := reviewOpenEnv(t)
	mpath := e.app.Config.Projects[0].Source
	f, err := os.OpenFile(mpath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n[repos.api.hooks]\npost_worktree = [\"touch hooked\"]\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	ref, _ := forkMR(t, e, root)

	out := e.mustMR("checkout", ref)
	tree := filepath.Join(root, "demo-gl", "api", "mr-3")
	if _, err := os.Stat(filepath.Join(tree, "hooked")); err == nil {
		t.Fatal("the post_worktree hook ran in a fork MR's tree")
	}
	if !strings.Contains(out, "post_worktree hooks not run") || !strings.Contains(out, "touch hooked") {
		t.Fatalf("no notice:\n%s", out)
	}
	if es := e.audited("git.hook"); len(es) != 0 {
		t.Fatalf("hook audited as run: %+v", es)
	}
	if es := e.audited("git.hook.skipped"); len(es) != 1 {
		t.Fatalf("skip not audited: %+v", es)
	}

	if _, err := e.review(renameMR, "--no-zellij"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "demo-gl", "api", "rename-bucket-to-token-bucket", "hooked")); err != nil {
		t.Fatalf("a same-project MR's tree lost its hook: %v", err)
	}
}

func TestReviewOpenDryRunPreviewsTheDetachedTreeWhenOriginLacksTheBranch(t *testing.T) {
	e, root := reviewOpenEnv(t)
	bare := filepath.Join(root, "demo-gl", "api", ".bare")
	origin := strings.TrimPrefix(gitOut(t, bare, "remote", "get-url", "origin"), "file://")
	gitAt(t, origin, "branch", "-D", "rename-bucket-to-token-bucket")
	out, err := e.review(renameMR, "--no-zellij", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, filepath.Join(root, "demo-gl", "api", "mr-2")) || !strings.Contains(out, "detached") {
		t.Fatalf("dry run:\n%s", out)
	}
}
