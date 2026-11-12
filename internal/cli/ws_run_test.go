package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/paths"
)

const wsRunManifest = `
name = "demo"
[repos.api]
path = "demo/api"
default_branch = "main"
[repos.api.tasks.dev]
cmd  = "sleep 60"
port = "auto"
[repos.web]
path = "demo/web"
default_branch = "main"
[repos.web.hooks]
install = ["echo installed >> installs", "test -f ok"]
[repos.web.tasks.check]
cmd  = "true"
kind = "oneshot"
[repos.web.tasks.dev]
cmd      = "sleep 60"
requires = ["api.dev"]
[workspaces]
default = { run = "web.dev" }
`

// wsEnv is an app over a projects root holding demo/{api,web}/main.
type wsEnv struct {
	t       *testing.T
	app     *app.App
	webTree string
}

func newWsEnv(t *testing.T) *wsEnv {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	webTree := filepath.Join(root, "demo", "web", "main")
	for _, d := range []string{filepath.Join(root, "demo", "api", "main"), webTree} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	snap := fmt.Sprintf("source = \"test\"\nmanifest = '''\n%s'''\n", wsRunManifest)
	if err := os.WriteFile(filepath.Join(root, "demo", ".project.toml"), []byte(snap), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log, err := audit.Open(dir, "user")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	a := &app.App{
		Paths:  paths.Paths{CacheDir: filepath.Join(dir, "cache"), DataDir: filepath.Join(dir, "data")},
		Config: &cfg, Root: root, Audit: log, Bus: events.NewBus(),
	}
	t.Cleanup(func() { a.Close() })
	return &wsEnv{t: t, app: a, webTree: webTree}
}

func (e *wsEnv) run(newCmd func() *cobra.Command, args ...string) (string, error) {
	e.t.Helper()
	cmd := newCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, e.app))
	return out.String(), err
}

func exitCode(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	if err != nil {
		return -1
	}
	return 0
}

func (e *wsEnv) installAudit() []audit.Entry {
	e.t.Helper()
	entries, err := e.app.Audit.Query(audit.Filter{Action: "git.hook"})
	if err != nil {
		e.t.Fatal(err)
	}
	return entries
}

func TestWsInstallFailsWithExitCodeOneAndAuditsEveryCommand(t *testing.T) {
	e := newWsEnv(t)
	out, err := e.run(newWsInstallCmd, e.webTree)
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "install failed in "+e.webTree) {
		t.Fatalf("err = %v (code %d)\n%s", err, exitCode(err), out)
	}
	if !strings.Contains(out, "$ test -f ok") || !strings.Contains(out, "failed:") {
		t.Fatalf("output:\n%s", out)
	}
	entries := e.installAudit()
	if len(entries) != 2 {
		t.Fatalf("audit: %+v", entries)
	}
	first, second := entries[0], entries[1]
	if first.Outcome != "ok" || first.Params["cmd"] != "echo installed >> installs" || first.Params["hook"] != "install" || first.Target != e.webTree || first.Project != "demo" {
		t.Errorf("first entry: %+v", first)
	}
	if second.Outcome != "error" || second.Params["cmd"] != "test -f ok" || second.Error == "" {
		t.Errorf("second entry: %+v", second)
	}

	// Fixed: the next run succeeds, and a third finds nothing to do.
	if err := os.WriteFile(filepath.Join(e.webTree, "ok"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err = e.run(newWsInstallCmd, e.webTree); err != nil || !strings.Contains(out, "installed") {
		t.Fatalf("retry: %v\n%s", err, out)
	}
	if out, err = e.run(newWsInstallCmd, e.webTree); err != nil || !strings.Contains(out, "up to date") {
		t.Fatalf("again: %v\n%s", err, out)
	}
	if n := len(e.installAudit()); n != 4 {
		t.Errorf("%d audit lines: an up-to-date run runs nothing", n)
	}
}

func TestTaskRunAfterInstallWaitsReportsAndTimesOut(t *testing.T) {
	e := newWsEnv(t)
	out, err := e.run(newTaskRunCmd, "web.check", "--worktree", e.webTree, "--after-install", "--wait-timeout", "300ms")
	if err == nil || !strings.Contains(err.Error(), "did not finish within 300ms") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !strings.Contains(out, "ade: waiting for the install of web ("+e.webTree+")") || !strings.Contains(out, "ade: starting web.check") {
		t.Fatalf("output:\n%s", out)
	}
	if strings.Contains(out, "ade: ready") {
		t.Fatalf("the node must not start before the install:\n%s", out)
	}
}

func TestTaskRunAfterInstallStopsWithTheReasonWhenTheInstallFailed(t *testing.T) {
	e := newWsEnv(t)
	if _, err := e.run(newWsInstallCmd, e.webTree); exitCode(err) != 1 {
		t.Fatalf("install: %v", err)
	}
	out, err := e.run(newTaskRunCmd, "web.check", "--worktree", e.webTree, "--after-install")
	if err == nil || !strings.Contains(err.Error(), "failed") || !strings.Contains(err.Error(), "test -f ok") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	// Once the install works, the same pane runs the oneshot.
	if err := os.WriteFile(filepath.Join(e.webTree, "ok"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err = e.run(newWsInstallCmd, e.webTree); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if out, err = e.run(newTaskRunCmd, "web.check", "--worktree", e.webTree, "--after-install"); err != nil || !strings.Contains(out, "ade: ready web.check") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestTaskRunWaitRequiresWaitsForTheServiceAndTimesOut(t *testing.T) {
	e := newWsEnv(t)
	out, err := e.run(newTaskRunCmd, "web.dev", "--worktree", e.webTree, "--wait-requires", "--wait-timeout", "300ms")
	if err == nil || !strings.Contains(err.Error(), "api.dev was not ready (tcp localhost:") || !strings.Contains(err.Error(), ") after 300ms") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !strings.Contains(out, "ade: waiting for api.dev (tcp localhost:") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestTaskRunRejectsANonPositiveWaitTimeout(t *testing.T) {
	e := newWsEnv(t)
	_, err := e.run(newTaskRunCmd, "web.check", "--worktree", e.webTree, "--wait-timeout", "0s")
	if err == nil || !strings.Contains(err.Error(), "--wait-timeout") {
		t.Fatalf("err = %v", err)
	}
}
