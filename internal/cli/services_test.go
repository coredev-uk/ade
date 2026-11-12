package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const servicesManifest = `
name = "demo"
[repos.web]
path = "demo/web"
default_branch = "main"
[repos.web.tasks.build]
cmd  = "echo built >> built.txt"
kind = "oneshot"
dir  = "app"
prepare = "mkdir -p node_modules && echo installed >> node_modules/count"
prepare_if_missing = "node_modules"
[services.db]
cmd  = "sleep 60"
port = "auto"
[services.cache]
cmd  = "sleep 60"
port = "auto"
[groups]
stack = ["services.db", "services.cache"]
[workspaces]
default = { run = "stack" }
`

type svcRow struct {
	Node      string `json:"node"`
	Declared  bool   `json:"declared"`
	State     string `json:"state"`
	Instances []struct {
		Node    string   `json:"node"`
		Tree    string   `json:"tree"`
		Holders []string `json:"holders"`
		Alive   bool     `json:"alive"`
	} `json:"instances"`
}

// servicesStatus runs `services status --json`, with no process left behind
// by the test.
func (e *wsEnv) servicesStatus(args ...string) map[string]svcRow {
	e.t.Helper()
	jsonOut = true
	defer func() { jsonOut = false }()
	out, err := e.run(newServicesCmd, append([]string{"status"}, args...)...)
	if err != nil {
		e.t.Fatalf("status: %v\n%s", err, out)
	}
	var projects []struct {
		Project  string   `json:"project"`
		Services []svcRow `json:"services"`
	}
	if err := json.Unmarshal([]byte(out), &projects); err != nil {
		e.t.Fatalf("status is not JSON: %v\n%s", err, out)
	}
	if len(projects) != 1 || projects[0].Project != "demo" {
		e.t.Fatalf("projects: %s", out)
	}
	rows := map[string]svcRow{}
	for _, s := range projects[0].Services {
		rows[s.Node] = s
	}
	return rows
}

func TestServicesUpStatusDownAcrossTheProject(t *testing.T) {
	e := newWsEnvWith(t, servicesManifest)
	t.Cleanup(func() { e.run(newServicesCmd, "down", "--force") }) //nolint:errcheck // never leave a service behind

	if st := e.servicesStatus(); st["services.db"].State != "stopped" || st["services.cache"].State != "stopped" || !st["services.db"].Declared || len(st) != 2 {
		t.Fatalf("before up: %+v", st)
	}

	out, err := e.run(newServicesCmd, "up", "db", "--detach")
	if err != nil || !strings.Contains(out, "project demo: starting 1 node(s)") || !strings.Contains(out, "left running; stop with: ade services down --project demo") {
		t.Fatalf("up: %v\n%s", err, out)
	}
	st := e.servicesStatus()
	if db := st["services.db"]; db.State != "ready" || len(db.Instances) != 1 || !db.Instances[0].Alive || !slices.Equal(db.Instances[0].Holders, []string{"project:demo"}) {
		t.Fatalf("db after up: %+v", db)
	}
	if st["services.cache"].State != "stopped" {
		t.Fatalf("up db started cache: %+v", st["services.cache"])
	}
	// Text output names the worktree-relative tree and who holds it.
	out, err = e.run(newServicesCmd, "status")
	if err != nil || !strings.Contains(out, "services.db") || !strings.Contains(out, "ready") || !strings.Contains(out, "held by project") || !strings.Contains(out, "services.cache") {
		t.Fatalf("status text: %v\n%s", err, out)
	}

	// A group starts its members; the running one is shared.
	if out, err = e.run(newServicesCmd, "up", "stack", "--detach"); err != nil || !strings.Contains(out, "(shared)") {
		t.Fatalf("up stack: %v\n%s", err, out)
	}
	if st = e.servicesStatus(); st["services.db"].State != "ready" || st["services.cache"].State != "ready" {
		t.Fatalf("after up stack: %+v", st)
	}

	// Down by name stops that one only.
	if out, err = e.run(newServicesCmd, "down", "cache"); err != nil || !strings.Contains(out, "stopped services.cache") {
		t.Fatalf("down cache: %v\n%s", err, out)
	}
	if st = e.servicesStatus(); st["services.cache"].State != "stopped" || st["services.db"].State != "ready" {
		t.Fatalf("after down cache: %+v", st)
	}
	// Down with no names stops what is left.
	if out, err = e.run(newServicesCmd, "down"); err != nil || !strings.Contains(out, "stopped services.db") {
		t.Fatalf("down: %v\n%s", err, out)
	}
	if st = e.servicesStatus(); st["services.db"].State != "stopped" {
		t.Fatalf("after down: %+v", st)
	}
	if out, err = e.run(newServicesCmd, "down"); err != nil || !strings.Contains(out, "nothing running") {
		t.Fatalf("down again: %v\n%s", err, out)
	}
}

func TestServicesDownLeavesWhatAWorkspaceHoldsUntilForced(t *testing.T) {
	e := newWsEnvWith(t, servicesManifest)
	t.Cleanup(func() { e.run(newServicesCmd, "down", "--force") }) //nolint:errcheck
	// A workspace pane holds db from a worktree.
	if out, err := e.run(newTaskRunCmd, "services.db", "--worktree", e.webTree, "--no-follow"); err != nil {
		t.Fatalf("task run: %v\n%s", err, out)
	}
	if out, err := e.run(newServicesCmd, "up", "db", "--detach"); err != nil || !strings.Contains(out, "(shared)") {
		t.Fatalf("up: %v\n%s", err, out)
	}
	out, err := e.run(newServicesCmd, "down")
	if err != nil || !strings.Contains(out, "still running: services.db") || !strings.Contains(out, "held by web/main") || !strings.Contains(out, "--force") {
		t.Fatalf("down: %v\n%s", err, out)
	}
	if st := e.servicesStatus(); st["services.db"].State != "ready" {
		t.Fatalf("a workspace's service stopped: %+v", st["services.db"])
	}
	if out, err = e.run(newServicesCmd, "down", "--force"); err != nil || !strings.Contains(out, "stopped services.db") {
		t.Fatalf("down --force: %v\n%s", err, out)
	}
	if st := e.servicesStatus(); st["services.db"].State != "stopped" {
		t.Fatalf("after --force: %+v", st["services.db"])
	}
}

func TestServicesRejectUnknownNamesAndProjects(t *testing.T) {
	e := newWsEnvWith(t, servicesManifest)
	if _, err := e.run(newServicesCmd, "up", "ghost", "--detach"); err == nil || !strings.Contains(err.Error(), `no node or group "ghost"`) {
		t.Errorf("unknown node: %v", err)
	}
	if _, err := e.run(newServicesCmd, "status", "--project", "nope"); err == nil || !strings.Contains(err.Error(), "project nope") {
		t.Errorf("unknown project: %v", err)
	}
	if _, err := e.run(newServicesCmd, "up", "db", "--with", "web"); err == nil || !strings.Contains(err.Error(), "want repo=value") {
		t.Errorf("bad pin: %v", err)
	}
}

func TestServicesOpenOutsideZellijSaysWhatToRun(t *testing.T) {
	e := newWsEnvWith(t, servicesManifest)
	e.app.Config.Host.Mode = "plain"
	out, err := e.run(newServicesCmd, "open")
	if err != nil || !strings.Contains(out, "no zellij here; run: ade services up --project demo") {
		t.Fatalf("open: %v\n%s", err, out)
	}
}

func TestTaskRunForTheProjectAuditsPrepareAndRunsInDir(t *testing.T) {
	e := newWsEnvWith(t, servicesManifest)
	tree := e.webTree
	if err := os.MkdirAll(filepath.Join(tree, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := e.run(newTaskRunCmd, "web.build", "--project", "demo")
	if err != nil || !strings.Contains(out, "ade: ready web.build") {
		t.Fatalf("task run: %v\n%s", err, out)
	}
	if got := readTestFile(t, filepath.Join(tree, "app", "built.txt")); got != "built\n" {
		t.Errorf("built.txt = %q: the node must run in its dir", got)
	}
	var prepares int
	for _, en := range e.installAudit() {
		if en.Params["hook"] == "prepare" {
			prepares++
			if en.Outcome != "ok" || en.Params["node"] != "web.build" || en.Target != filepath.Join(tree, "app") || en.Project != "demo" {
				t.Errorf("audit entry: %+v", en)
			}
		}
	}
	if prepares != 1 {
		t.Fatalf("%d prepare audit entries, want 1", prepares)
	}
	// node_modules exists now: a second run neither prepares nor audits.
	if _, err = e.run(newTaskRunCmd, "web.build", "--project", "demo"); err != nil {
		t.Fatal(err)
	}
	for _, en := range e.installAudit() {
		if en.Params["hook"] == "prepare" {
			prepares--
		}
	}
	if prepares != 0 {
		t.Errorf("a second run audited %d more prepares", -prepares)
	}
	if _, err := e.run(newTaskRunCmd, "web.build", "--project", "demo", "--worktree", tree); err == nil {
		t.Error("--project with --worktree accepted")
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
