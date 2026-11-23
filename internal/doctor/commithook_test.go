package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/commitprefix"
	"github.com/coredev-uk/ade/internal/config"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// commitHookLayout is <root>/demo/api with a bare clone and the worktrees
// "story" (commit prefix GL-7) and "plain" (none).
func commitHookLayout(t *testing.T) (root, story, plain string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	root = filepath.Join(tmp, "root")
	src := filepath.Join(tmp, "src")
	git(t, tmp, "init", "-q", "-b", "main", src)
	git(t, src, "commit", "-q", "--allow-empty", "-m", "init")
	repo := filepath.Join(root, "demo", "api")
	bare := filepath.Join(repo, ".bare")
	git(t, tmp, "clone", "-q", "--bare", src, bare)
	story, plain = filepath.Join(repo, "story"), filepath.Join(repo, "plain")
	git(t, bare, "worktree", "add", "-q", "-b", "story", story)
	git(t, bare, "worktree", "add", "-q", "-b", "plain", plain)
	manifest := "name = \"demo\"\nforge = \"sandbox\"\n[tracker]\nproject = \"g/tracker\"\n[repos.api]\npath = \"g/api\"\n"
	snap := "source = \"x\"\nfetched_at = 2026-11-11T12:00:00Z\nlast_update = 2026-11-11T12:00:00Z\nmanifest = '''" + manifest + "'''\n"
	if err := os.WriteFile(filepath.Join(root, "demo", ".project.toml"), []byte(snap), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := commitprefix.WriteWorktreeConfig(context.Background(), story, 7, "GL-7", "main"); err != nil {
		t.Fatal(err)
	}
	return root, story, plain
}

func hookChecks(t *testing.T, root string) []Check {
	t.Helper()
	return commitHookChecks(context.Background(), Options{Config: &config.Config{}, Root: root, Timeout: 10 * time.Second})
}

func TestCommitHookChecksWarnUntilTheHookIsInstalled(t *testing.T) {
	root, story, _ := commitHookLayout(t)

	got := hookChecks(t, root)
	if len(got) != 1 || got[0].Name != "demo/api/story" || got[0].Group != "commit-prefix" {
		t.Fatalf("checks = %+v, want only the worktree with a prefix", got)
	}
	if got[0].Status != Warn || !strings.Contains(got[0].Detail, "GL-7") || !strings.Contains(got[0].Fix, "ade ws hook install "+story) {
		t.Errorf("without the hook: %+v", got[0])
	}

	if _, err := commitprefix.InstallHook(context.Background(), story); err != nil {
		t.Fatal(err)
	}
	if got := hookChecks(t, root); len(got) != 1 || got[0].Status != OK || got[0].Fix != "" {
		t.Errorf("with the hook: %+v", got)
	}
}

func TestCommitHookChecksAcceptAUsersOwnHook(t *testing.T) {
	root, story, _ := commitHookLayout(t)
	hooks := filepath.Join(filepath.Dir(story), ".bare", "hooks")
	if err := os.WriteFile(filepath.Join(hooks, "commit-msg"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := hookChecks(t, root)
	if len(got) != 1 || got[0].Status != OK || !strings.Contains(got[0].Detail, "your own commit-msg hook") {
		t.Errorf("checks = %+v", got)
	}
}
