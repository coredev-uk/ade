package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/paths"
)

// codeEnv is a project "demo" on disk (api with a 3-add-limit worktree, web
// with only its baseline) and a cache holding one work item routed to api.
type codeEnv struct {
	t    *testing.T
	app  *app.App
	root string
	rec  string
}

func newCodeEnv(t *testing.T) *codeEnv {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for repo, branches := range map[string][]string{"api": {"3-add-limit"}, "web": nil} {
		seed := t.TempDir()
		git(seed, "init", "-q", "-b", "main")
		git(seed, "commit", "-q", "--allow-empty", "-m", "init")
		dir := filepath.Join(root, "demo", repo)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		git(dir, "clone", "-q", "--bare", seed, ".bare")
		git(filepath.Join(dir, ".bare"), "worktree", "add", "-q", filepath.Join(dir, "main"), "main")
		for _, b := range branches {
			git(filepath.Join(dir, ".bare"), "worktree", "add", "-q", "-b", b, filepath.Join(dir, b), "main")
		}
	}
	manifest := "name = \"demo\"\n[repos.api]\npath = \"g/api\"\ndefault_branch = \"main\"\n[repos.web]\npath = \"g/web\"\ndefault_branch = \"main\"\n"
	snap := "source = \"x\"\nmanifest = '''\n" + manifest + "'''\n"
	if err := os.WriteFile(filepath.Join(root, "demo", ".project.toml"), []byte(snap), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	log, err := audit.Open(dir, "user")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	rec := filepath.Join(t.TempDir(), "records")
	a := &app.App{Paths: paths.Paths{CacheDir: dir}, Config: &cfg, Root: root, RecordsDir: rec, Audit: log, Bus: events.NewBus()}
	t.Cleanup(func() { a.Close() })

	st, err := a.Cache()
	if err != nil {
		t.Fatal(err)
	}
	wi := domain.WorkItem{
		Ref:      forge.Ref{Project: "g/tracker", IID: 15, Kind: forge.KindIssue},
		Repos:    []string{"api"},
		Issue:    forge.Issue{Title: "Rate-limit exports"},
		Children: []forge.Issue{{Ref: forge.Ref{Project: "g/api", IID: 3, Kind: forge.KindIssue}}},
	}
	state, _ := json.Marshal(forgesync.Result{Project: "demo", Forge: "sandbox"})
	itemJSON, _ := json.Marshal(wi)
	if _, err := st.DB.Exec(`INSERT INTO sync_state (project, json) VALUES ('demo', ?)`, string(state)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO work_items (project, ref, json) VALUES ('demo', ?, ?)`, wi.Ref.String(), string(itemJSON)); err != nil {
		t.Fatal(err)
	}
	return &codeEnv{t: t, app: a, root: root, rec: rec}
}

func (e *codeEnv) run(args ...string) (string, error) {
	e.t.Helper()
	cmd := newVSCodeCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"workspace"}, args...))
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, e.app))
	return out.String(), err
}

func TestVSCodeWorkspaceForAChildIssueRefWritesIntoTheItemRecordsDir(t *testing.T) {
	e := newCodeEnv(t)
	// The child issue's ref resolves to the tracker item.
	out, err := e.run("g/api#3")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := filepath.Join(e.rec, "items", "sandbox", "g", "tracker", "15", "15-rate-limit-exports.code-workspace")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != want {
		t.Fatalf("path = %q, want %q\n%s", lines[0], want, out)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 || !strings.Contains(lines[1], "item") || !strings.Contains(lines[1], "api (3-add-limit)") ||
		!strings.Contains(lines[2], "baseline") || !strings.Contains(lines[2], "web (main)") {
		t.Errorf("folders:\n%s", out)
	}
}

func TestVSCodeWorkspaceJSONFromAWorktree(t *testing.T) {
	e := newCodeEnv(t)
	jsonOut = true
	t.Cleanup(func() { jsonOut = false })
	out, err := e.run(filepath.Join(e.root, "demo", "api", "3-add-limit"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got codeJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if filepath.Base(got.Path) != "15-rate-limit-exports.code-workspace" || got.Opened || len(got.Folders) != 2 {
		t.Fatalf("%+v", got)
	}
	api, web := got.Folders[0], got.Folders[1]
	if api.Source != "item" || api.Path != filepath.Join(e.root, "demo", "api", "3-add-limit") ||
		web.Source != "baseline" || web.Path != filepath.Join(e.root, "demo", "web", "main") || web.Name != "web (main)" {
		t.Errorf("folders = %+v", got.Folders)
	}
}

func TestVSCodeWorkspaceOfABaselineTreeHasNoItem(t *testing.T) {
	e := newCodeEnv(t)
	out, err := e.run(filepath.Join(e.root, "demo", "web", "main"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := filepath.Join(e.rec, "workspaces", "demo", "demo-main.code-workspace"); !strings.HasPrefix(out, want+"\n") {
		t.Errorf("out = %q, want path %s", out, want)
	}
	if strings.Contains(out, "item ") {
		t.Errorf("a baseline tree has no item folders:\n%s", out)
	}
}

func TestVSCodeWorkspaceOpenRunsCodeOrHints(t *testing.T) {
	e := newCodeEnv(t)
	// A desktop session (no web tty): without `code` the hint names the launcher.
	t.Setenv("DISPLAY", ":0")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("VSCODE_IPC_HOOK_CLI", "")
	t.Setenv("PATH", t.TempDir())
	out, err := e.run("g/tracker#15", "--open")
	if err != nil || !strings.Contains(out, "code is not on PATH") {
		t.Fatalf("without code: %v\n%s", err, out)
	}

	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "args")
	if err := os.WriteFile(filepath.Join(bin, "code"), []byte("#!/bin/sh\necho \"$@\" > "+log+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err = e.run("g/tracker#15", "--open"); err != nil || strings.Contains(out, "not on PATH") {
		t.Fatalf("with code: %v\n%s", err, out)
	}
	got, _ := os.ReadFile(log)
	if !strings.HasSuffix(strings.TrimSpace(string(got)), "15-rate-limit-exports.code-workspace") {
		t.Errorf("code args = %q", got)
	}
}

func TestVSCodeWorkspaceUnknownRef(t *testing.T) {
	e := newCodeEnv(t)
	if _, err := e.run("g/tracker#99"); err == nil || !strings.Contains(err.Error(), "ade sync") {
		t.Errorf("err = %v", err)
	}
	if _, err := e.run("g/tracker#x"); err == nil {
		t.Error("malformed ref accepted")
	}
}
