package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pushOnBranch adds a commit to branch on the repo's remote, the way the MR's
// author pushing again does.
func pushOnBranch(t *testing.T, bare, branch string) string {
	t.Helper()
	origin := gitOut(t, bare, "remote", "get-url", "origin")
	work := filepath.Join(t.TempDir(), "author")
	gitAt(t, filepath.Dir(work), "clone", "--quiet", "-b", branch, origin, work)
	gitAt(t, work, "commit", "--quiet", "--allow-empty", "-m", "author pushes again")
	gitAt(t, work, "push", "--quiet", "origin", branch)
	return gitOut(t, work, "rev-parse", "HEAD")
}

func TestMRCheckoutMakesADetachedTreeAndFollowsTheMR(t *testing.T) {
	e, root := reviewOpenEnv(t)
	tree := filepath.Join(root, "demo-gl", "api", "mr-2")
	bare := filepath.Join(root, "demo-gl", "api", ".bare")

	out := e.mustMR("checkout", renameMR, "--dry-run")
	if !strings.Contains(out, "dry-run: would create "+tree) {
		t.Fatalf("dry run:\n%s", out)
	}
	if _, err := os.Stat(tree); err == nil {
		t.Fatal("--dry-run made the worktree")
	}

	out = e.mustMR("checkout", renameMR)
	if !strings.Contains(out, "created "+tree) {
		t.Fatalf("output:\n%s", out)
	}
	if err := exec.Command("git", "-C", tree, "symbolic-ref", "-q", "HEAD").Run(); err == nil {
		t.Fatal("the tree is on a branch, want detached")
	}
	head := gitOut(t, tree, "rev-parse", "HEAD")
	if want := gitOut(t, bare, "rev-parse", "refs/remotes/origin/rename-bucket-to-token-bucket"); head != want {
		t.Fatalf("tree at %s, branch head %s", head, want)
	}
	if es := e.audited("git.worktree.add"); len(es) != 1 || es[0].Params["for"] != "mr checkout" {
		t.Fatalf("audit: %+v", es)
	}

	asJSON(t)
	var got mrCheckedOut
	if err := json.Unmarshal([]byte(e.mustMR("checkout", renameMR)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Action != "keep" || got.Worktree != tree || got.Repo != "api" || got.Ref.IID != 2 {
		t.Fatalf("second run = %+v", got)
	}

	// The author pushes again: a clean tree follows, a dirty one stays put.
	newHead := pushOnBranch(t, bare, "rename-bucket-to-token-bucket")
	if err := os.WriteFile(filepath.Join(tree, "scratch.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(e.mustMR("checkout", renameMR)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Action != "keep" || !strings.Contains(got.Reason, "uncommitted") || gitOut(t, tree, "rev-parse", "HEAD") != head {
		t.Fatalf("dirty tree: %+v", got)
	}
	if err := os.Remove(filepath.Join(tree, "scratch.txt")); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(e.mustMR("checkout", renameMR)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Action != "update" || gitOut(t, tree, "rev-parse", "HEAD") != newHead {
		t.Fatalf("clean tree: %+v (want %s)", got, newHead)
	}
}

func TestMRCheckoutNeedsAClonedRepo(t *testing.T) {
	e, _ := reviewOpenEnv(t)
	e.app.Config.Projects = nil
	if _, err := e.mr("checkout", renameMR, "--forge", "sandbox-gitlab"); err == nil || !strings.Contains(err.Error(), "ade project add") {
		t.Fatalf("err = %v", err)
	}
}
