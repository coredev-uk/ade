package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/commitprefix"
)

func (e *cliEnv) wsHook(args ...string) (string, error) { return e.run(newWsHookCmd, args...) }

// hookEnv is the gc layout with GL-7 recorded in the web worktree.
func hookEnv(t *testing.T) (e *cliEnv, api, web string) {
	t.Helper()
	e = newCLIEnv(t)
	api, web = e.gcLayout()
	if err := commitprefix.WriteWorktreeConfig(t.Context(), web, 7, "GL-7", "main"); err != nil {
		t.Fatal(err)
	}
	return e, api, web
}

func TestWsHookInstallPrefixesYourFirstCommitAndRemoveUndoesIt(t *testing.T) {
	e, _, web := hookEnv(t)

	out, err := e.wsHook("install", web, "--dry-run")
	if err != nil || !strings.Contains(out, "would be installed") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if got, err := exec.Command("git", "-C", web, "config", "--worktree", "--get", "core.hooksPath").Output(); err == nil {
		t.Errorf("--dry-run wrote core.hooksPath: %q", got)
	}
	if got := e.audited("git.hook.install"); len(got) != 0 {
		t.Errorf("--dry-run audited: %+v", got)
	}

	if out, err = e.wsHook("install", web); err != nil || !strings.Contains(out, "commit-prefix hook installed in "+web) {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if out, err = e.wsHook("install", web); err != nil || !strings.Contains(out, "already installed") {
		t.Fatalf("second install: %v\n%s", err, out)
	}
	if es := e.audited("git.hook.install"); len(es) != 1 || es[0].Outcome != "ok" || es[0].Target != web {
		t.Errorf("audit = %+v, want one entry for the real install", es)
	}
	gitAt(t, web, "commit", "--quiet", "--allow-empty", "-m", "Fix export")
	if got := gitOut(t, web, "log", "-1", "--format=%s"); got != "GL-7 Fix export" {
		t.Errorf("commit subject = %q", got)
	}

	if out, err = e.wsHook("remove", web); err != nil || !strings.Contains(out, "removed from "+web) {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	gitAt(t, web, "commit", "--quiet", "--allow-empty", "-m", "Plain")
	if got := gitOut(t, web, "log", "-1", "--format=%s"); got != "Plain" {
		t.Errorf("commit subject after remove = %q", got)
	}
	if out, err = e.wsHook("remove", web); err != nil || !strings.Contains(out, "no commit-prefix hook of ade's") {
		t.Errorf("second remove: %v\n%s", err, out)
	}
	if es := e.audited("git.hook.remove"); len(es) != 1 {
		t.Errorf("remove audit = %+v", es)
	}
}

func TestWsHookInstallRefusesAUsersHookAndAWorktreeWithoutPrefix(t *testing.T) {
	e, api, web := hookEnv(t)
	user := filepath.Join(e.app.Root, "demo-gl", "web", ".bare", "hooks", "commit-msg")
	if err := os.WriteFile(user, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	asJSON(t)
	out, err := e.wsHook("install", web)
	if err == nil || !isExit(err, 2) || !strings.Contains(err.Error(), user) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	var got hookOut
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil || got.Outcome != "refused" || got.Foreign != user {
		t.Errorf("json = %+v, %v\n%s", got, jerr, out)
	}
	if body, _ := os.ReadFile(user); string(body) != "#!/bin/sh\nexit 0\n" {
		t.Errorf("the user's hook changed: %q", body)
	}

	if _, err := e.wsHook("install", api); err == nil || !strings.Contains(err.Error(), "ade.commitPrefix") {
		t.Errorf("a worktree without a prefix: %v", err)
	}
}
