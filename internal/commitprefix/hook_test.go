package commitprefix

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hermetic keeps the machine's git config out of the tests.
func hermetic(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("HOME", t.TempDir())
}

// prefixedTree is bareWithTrees with GL-7 recorded in a (target main).
func prefixedTree(t *testing.T) (a, b, bare string) {
	t.Helper()
	hermetic(t)
	a, b = bareWithTrees(t)
	if err := WriteWorktreeConfig(context.Background(), a, 7, "GL-7", "main"); err != nil {
		t.Fatal(err)
	}
	return a, b, filepath.Join(filepath.Dir(a), ".bare")
}

func commit(t *testing.T, dir string, msg ...string) {
	t.Helper()
	args := []string{"commit", "-q", "--allow-empty"}
	for _, m := range msg {
		args = append(args, "-m", m)
	}
	sh(t, dir, args...)
}

func lastMessage(t *testing.T, dir string) string {
	t.Helper()
	return sh(t, dir, "log", "-1", "--format=%B")
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestHookPrefixesTheFirstCommitOnly(t *testing.T) {
	ctx := context.Background()
	a, b, _ := prefixedTree(t)
	plan, err := InstallHook(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if got := sh(t, a, "config", "--worktree", "--get", "core.hooksPath"); got != plan.Dir {
		t.Errorf("core.hooksPath = %q, want %q", got, plan.Dir)
	}
	if out, err := exec.Command("git", "-C", b, "config", "--get", "core.hooksPath").Output(); err == nil {
		t.Errorf("the hook path leaked into sibling worktree b: %q", out)
	}

	commit(t, a, "Fix the export", "body line")
	if got := lastMessage(t, a); got != "GL-7 Fix the export\n\nbody line" {
		t.Errorf("first commit = %q", got)
	}
	commit(t, a, "Second change")
	if got := lastMessage(t, a); got != "Second change" {
		t.Errorf("a later commit = %q: only the MR's first commit takes the prefix", got)
	}
	commit(t, b, "Unrelated work")
	if got := lastMessage(t, b); got != "Unrelated work" {
		t.Errorf("a worktree without the hook = %q", got)
	}
}

func TestHookLeavesPrefixedMergeAndAutosquashMessagesAlone(t *testing.T) {
	ctx := context.Background()
	for name, msg := range map[string]string{
		"already prefixed": "GL-7 Done",
		"colon":            "GL-7: Done",
		"merge":            "Merge branch 'x' into y",
		"squashed merge":   "Squashed commit of the following:",
		"fixup":            "fixup! something",
		"squash":           "squash! something",
		"revert":           `Revert "something"`,
	} {
		t.Run(name, func(t *testing.T) {
			a, _, _ := prefixedTree(t)
			if _, err := InstallHook(ctx, a); err != nil {
				t.Fatal(err)
			}
			commit(t, a, msg)
			if got := lastMessage(t, a); got != msg {
				t.Errorf("message = %q, want it unchanged", got)
			}
		})
	}
	t.Run("empty message stays empty", func(t *testing.T) {
		a, _, _ := prefixedTree(t)
		if _, err := InstallHook(ctx, a); err != nil {
			t.Fatal(err)
		}
		sh(t, a, "commit", "-q", "--allow-empty", "--allow-empty-message", "-m", "")
		if got := lastMessage(t, a); got != "" {
			t.Errorf("message = %q, want it empty", got)
		}
	})
	t.Run("a prefix with glob characters is matched literally", func(t *testing.T) {
		a, _, _ := prefixedTree(t)
		if err := WriteWorktreeConfig(ctx, a, 7, "[GL-*]", "main"); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallHook(ctx, a); err != nil {
			t.Fatal(err)
		}
		commit(t, a, "[GL-*] already")
		commit(t, a, "ignored: not first")
		if got := lastMessage(t, a); got != "ignored: not first" {
			t.Errorf("message = %q", got)
		}
		sh(t, a, "reset", "-q", "--hard", "main")
		commit(t, a, "[GL-x] other")
		if got := lastMessage(t, a); got != "[GL-*] [GL-x] other" {
			t.Errorf("message = %q", got)
		}
	})
}

func TestInstallHookIsIdempotent(t *testing.T) {
	ctx := context.Background()
	a, _, _ := prefixedTree(t)
	first, err := InstallHook(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := PlanHook(ctx, a); err != nil || !again.Installed || again.Stale {
		t.Fatalf("plan after install = %+v, %v", again, err)
	}
	second, err := InstallHook(ctx, a)
	if err != nil || second.Dir != first.Dir || !second.Installed {
		t.Fatalf("reinstall = %+v, %v", second, err)
	}
	if n := strings.Count(sh(t, a, "config", "--worktree", "--get-all", "core.hooksPath"), "\n"); n != 0 {
		t.Errorf("core.hooksPath written more than once")
	}
	entries, _ := os.ReadDir(first.Dir)
	if len(entries) != 1 || entries[0].Name() != "commit-msg" {
		t.Errorf("hooks dir = %v, want just commit-msg", entries)
	}
	commit(t, a, "Once")
	if got := lastMessage(t, a); got != "GL-7 Once" {
		t.Errorf("message = %q", got)
	}

	// An older version of ADE's hook is rewritten.
	if err := os.WriteFile(filepath.Join(first.Dir, "commit-msg"), []byte("#!/bin/sh\n"+marker+" old\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if stale, _ := PlanHook(ctx, a); !stale.Stale || stale.Installed {
		t.Errorf("an old hook = %+v, want stale", stale)
	}
	if _, err := InstallHook(ctx, a); err != nil {
		t.Fatal(err)
	}
	if now, _ := PlanHook(ctx, a); !now.Installed {
		t.Errorf("after reinstall = %+v", now)
	}
}

func TestInstallHookNeverReplacesAUsersHook(t *testing.T) {
	ctx := context.Background()
	const mine = "#!/bin/sh\necho mine\n"
	t.Run("in the repo's hooks directory", func(t *testing.T) {
		a, _, bare := prefixedTree(t)
		user := filepath.Join(bare, "hooks", "commit-msg")
		writeExec(t, user, mine)
		plan, err := InstallHook(ctx, a)
		var foreign *ForeignHookError
		if !errors.As(err, &foreign) || foreign.Path != user || plan.Foreign != user {
			t.Fatalf("err = %v, plan = %+v", err, plan)
		}
		if body, _ := os.ReadFile(user); string(body) != mine {
			t.Errorf("the user's hook was changed: %q", body)
		}
		if out, err := exec.Command("git", "-C", a, "config", "--get", "core.hooksPath").Output(); err == nil {
			t.Errorf("core.hooksPath set to %q although the install was refused", out)
		}
		if _, err := os.Stat(plan.Dir); err == nil {
			t.Error("ADE's directory was created although the install was refused")
		}
	})
	t.Run("in a shared core.hooksPath", func(t *testing.T) {
		a, b, _ := prefixedTree(t)
		shared := filepath.Join(t.TempDir(), "hooks")
		user := filepath.Join(shared, "commit-msg")
		writeExec(t, user, mine)
		sh(t, a, "config", "--local", "core.hooksPath", shared)
		if _, err := InstallHook(ctx, a); err == nil {
			t.Fatal("installed over a user's hook in the shared hooksPath")
		}
		if got := sh(t, a, "config", "--get", "core.hooksPath"); got != shared {
			t.Errorf("core.hooksPath = %q, want the shared one untouched", got)
		}
		_ = b
	})
	t.Run("a file of the user's in ADE's own directory", func(t *testing.T) {
		a, _, _ := prefixedTree(t)
		plan, _ := PlanHook(ctx, a)
		writeExec(t, filepath.Join(plan.Dir, "commit-msg"), mine)
		if _, err := InstallHook(ctx, a); err == nil {
			t.Fatal("overwrote the user's file")
		}
		if body, _ := os.ReadFile(filepath.Join(plan.Dir, "commit-msg")); string(body) != mine {
			t.Errorf("file = %q", body)
		}
	})
}

// A shared core.hooksPath that holds other hooks keeps running them: the
// worktree's own hooksPath would otherwise hide them.
func TestInstallHookKeepsTheOtherHooksOfASharedHooksPath(t *testing.T) {
	ctx := context.Background()
	a, b, _ := prefixedTree(t)
	shared := filepath.Join(t.TempDir(), "my hooks") // a space: the stub quotes the path
	ran := filepath.Join(t.TempDir(), "ran")
	writeExec(t, filepath.Join(shared, "pre-commit"), "#!/bin/sh\necho pre-commit >> '"+ran+"'\n")
	writeExec(t, filepath.Join(shared, "pre-commit.sample"), "#!/bin/sh\nexit 1\n")
	sh(t, a, "config", "--local", "core.hooksPath", shared)

	plan, err := InstallHook(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Forward) != 1 || plan.Forward[0] != "pre-commit" || plan.Prev != shared {
		t.Fatalf("plan = %+v", plan)
	}
	commit(t, a, "Through the stub")
	if got := lastMessage(t, a); got != "GL-7 Through the stub" {
		t.Errorf("message = %q", got)
	}
	if body, _ := os.ReadFile(ran); string(body) != "pre-commit\n" {
		t.Errorf("the shared pre-commit hook ran %q", body)
	}
	commit(t, b, "Sibling")
	if body, _ := os.ReadFile(ran); string(body) != "pre-commit\npre-commit\n" {
		t.Errorf("the sibling worktree no longer uses the shared hooks: %q", body)
	}

	// A reinstall finds the shared directory again and drops a stub whose hook is gone.
	if err := os.Remove(filepath.Join(shared, "pre-commit")); err != nil {
		t.Fatal(err)
	}
	again, err := InstallHook(ctx, a)
	if err != nil || again.Prev != shared || len(again.Forward) != 0 {
		t.Fatalf("reinstall = %+v, %v", again, err)
	}
	if _, err := os.Stat(filepath.Join(plan.Dir, "pre-commit")); err == nil {
		t.Error("a stale forwarding stub stayed")
	}
}

func TestInstallHookNeedsAPrefix(t *testing.T) {
	hermetic(t)
	a, _ := bareWithTrees(t)
	plan, err := InstallHook(context.Background(), a)
	if !errors.Is(err, ErrNoPrefix) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(plan.Dir); err == nil {
		t.Error("installed without a prefix")
	}
}

func TestRemoveHook(t *testing.T) {
	ctx := context.Background()
	a, _, bare := prefixedTree(t)
	shared := filepath.Join(t.TempDir(), "hooks")
	writeExec(t, filepath.Join(shared, "pre-commit"), "#!/bin/sh\nexit 0\n")
	sh(t, a, "config", "--local", "core.hooksPath", shared)
	plan, err := InstallHook(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(plan.Dir, "notes.txt")
	writeExec(t, own, "mine")

	if _, changed, err := RemoveHook(ctx, a); err != nil || !changed {
		t.Fatalf("remove = %v, %v", changed, err)
	}
	if got := sh(t, a, "config", "--get", "core.hooksPath"); got != shared {
		t.Errorf("core.hooksPath = %q, want the shared one back", got)
	}
	if entries, _ := os.ReadDir(plan.Dir); len(entries) != 1 || entries[0].Name() != "notes.txt" {
		t.Errorf("hooks dir = %v, want only the user's file", entries)
	}
	commit(t, a, "After removal")
	if got := lastMessage(t, a); got != "After removal" {
		t.Errorf("message = %q: the hook still runs", got)
	}
	if _, changed, err := RemoveHook(ctx, a); err != nil || changed {
		t.Errorf("second remove = %v, %v; want a no-op", changed, err)
	}
	_ = bare
}

// In a bare clone the local target branch is often behind origin/<target>:
// a branch cut from origin/main still has only its own commits to count.
func TestHookAndFirstCommitIgnoreACommitOriginsTargetHasAndTheLocalOneLacks(t *testing.T) {
	ctx := context.Background()
	a, _, _ := prefixedTree(t)
	commit(t, a, "Landed upstream, not in the local main") // no hook yet
	sh(t, a, "update-ref", "refs/remotes/origin/main", "HEAD")
	if !FirstCommit(ctx, a) {
		t.Error("FirstCommit = false for a branch at origin/main with a stale local main")
	}
	if _, err := InstallHook(ctx, a); err != nil {
		t.Fatal(err)
	}
	commit(t, a, "My first")
	if got := lastMessage(t, a); got != "GL-7 My first" {
		t.Errorf("first commit = %q", got)
	}
	if FirstCommit(ctx, a) {
		t.Error("FirstCommit = true after the first commit")
	}
	commit(t, a, "My second")
	if got := lastMessage(t, a); got != "My second" {
		t.Errorf("second commit = %q", got)
	}
}

func TestRemoveHookPutsBackAWorktreeLevelHooksPath(t *testing.T) {
	ctx := context.Background()
	a, _, _ := prefixedTree(t)
	mine := filepath.Join(t.TempDir(), "my-hooks")
	ran := filepath.Join(t.TempDir(), "ran")
	writeExec(t, filepath.Join(mine, "pre-commit"), "#!/bin/sh\necho x >> '"+ran+"'\n")
	sh(t, a, "config", "--worktree", "core.hooksPath", mine)

	plan, err := InstallHook(ctx, a)
	if err != nil || plan.Restore != mine || plan.Prev != mine {
		t.Fatalf("install = %+v, %v", plan, err)
	}
	if again, err := InstallHook(ctx, a); err != nil || again.Restore != mine || again.Prev != mine {
		t.Fatalf("reinstall = %+v, %v", again, err)
	}
	commit(t, a, "Through both")
	if got := lastMessage(t, a); got != "GL-7 Through both" {
		t.Errorf("message = %q", got)
	}
	if body, _ := os.ReadFile(ran); string(body) != "x\n" {
		t.Errorf("the user's pre-commit hook ran %q", body)
	}

	if _, changed, err := RemoveHook(ctx, a); err != nil || !changed {
		t.Fatalf("remove = %v, %v", changed, err)
	}
	if got := sh(t, a, "config", "--worktree", "--get", "core.hooksPath"); got != mine {
		t.Errorf("core.hooksPath = %q, want the user's %q back", got, mine)
	}
	if out, err := exec.Command("git", "-C", a, "config", "--get", "ade.prevHooksPath").Output(); err == nil {
		t.Errorf("the backup key stayed: %q", out)
	}
}
