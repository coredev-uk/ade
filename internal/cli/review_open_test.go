package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/startwork/swtest"
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
