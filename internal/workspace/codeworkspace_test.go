package workspace

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

const codeManifest = `
name = "demo"
[repos.api]
path = "grp/api"
default_branch = "main"
app = ["api"]
[repos.api.tasks.build]
cmd  = "true"
kind = "oneshot"
[repos.api.tasks.dev]
cmd      = "sleep 60"
port     = "auto"
requires = ["api.build"]
ready    = { http = "http://127.0.0.1:{port}/healthz", timeout = "20s" }
[repos.web]
path = "grp/web"
default_branch = "main"
app = ["web"]
requires = ["api"]
[repos.web.tasks.dev]
cmd      = "sleep 60"
port     = "auto"
requires = ["api.dev"]
env      = { API_URL = "http://127.0.0.1:{api.dev.port}" }
ready    = { tcp = "127.0.0.1:{port}", timeout = "20s" }
[groups]
dev = ["web.dev"]
[workspaces]
default = { layout = "code", run = "dev" }
`

type codeFixture struct {
	*fixture
	dir string
}

func newCodeFixture(t *testing.T, apiBranches, webBranches []string) *codeFixture {
	t.Helper()
	f := newFixture(t, codeManifest)
	gitRepo(t, filepath.Join(f.root, "demo", "api"), apiBranches...)
	gitRepo(t, filepath.Join(f.root, "demo", "web"), webBranches...)
	return &codeFixture{fixture: f, dir: t.TempDir()}
}

func (f *codeFixture) input(t *testing.T, item *ItemRef, loc *Location) CodeInput {
	t.Helper()
	anchor, err := Locate(f.root, filepath.Join(f.root, "demo", "web", "main"))
	if err != nil {
		t.Fatal(err)
	}
	return CodeInput{Root: f.root, Manifest: anchor.Manifest, Item: item, Loc: loc, Dir: f.dir, Ade: "/bin/ade"}
}

func itemWith(title string, children []forge.Ref) *ItemRef {
	wi := workItem([]string{"web", "api"}, children)
	wi.Issue.Title = title
	return &ItemRef{Project: "demo", Forge: "f", Item: wi}
}

func readCode(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, data)
	}
	return m
}

func codeTasks(t *testing.T, m map[string]json.RawMessage) map[string]VSTask {
	t.Helper()
	var tf struct {
		Version string   `json:"version"`
		Tasks   []VSTask `json:"tasks"`
	}
	if err := json.Unmarshal(m["tasks"], &tf); err != nil {
		t.Fatal(err)
	}
	if tf.Version != "2.0.0" {
		t.Errorf("tasks version = %q", tf.Version)
	}
	out := map[string]VSTask{}
	for _, task := range tf.Tasks {
		out[task.Label] = task
	}
	return out
}

func TestCodeWorkspaceMixesItemWorktreesAndBaselines(t *testing.T) {
	f := newCodeFixture(t, []string{"3-add-limit-to-the-exports-endpoint-now"}, []string{"12-paginate"})
	item := itemWith("Rate-limit the exports API", []forge.Ref{issueRef("grp/api", 3), issueRef("grp/web", 12)})
	item.Item.Ref.IID = 7

	res, err := f.mgr.CodeWorkspace(context.Background(), f.input(t, item, nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(f.dir, "7-rate-limit-the-exports-api.code-workspace"); res.Path != want {
		t.Fatalf("path = %s, want %s", res.Path, want)
	}
	if len(res.Folders) != 2 {
		t.Fatalf("folders = %+v", res.Folders)
	}
	// The item's primary repo (the first routed repo with a worktree) comes first.
	web, api := res.Folders[0], res.Folders[1]
	if web.Repo != "web" || web.Source != FolderItem || web.Name != "web (12-paginate)" ||
		web.Path != filepath.Join(f.root, "demo", "web", "12-paginate") {
		t.Errorf("web = %+v", web)
	}
	if api.Repo != "api" || api.Source != FolderItem || api.Name != "api (3-add-limit-to-the-expo…)" || len([]rune(api.Name)) != len("api ()")+24 ||
		api.Path != filepath.Join(f.root, "demo", "api", "3-add-limit-to-the-exports-endpoint-now") {
		t.Errorf("api = %+v", api)
	}

	doc := readCode(t, res.Path)
	var folders []map[string]string
	if err := json.Unmarshal(doc["folders"], &folders); err != nil || len(folders) != 2 || folders[0]["path"] != web.Path || folders[1]["name"] != api.Name {
		t.Errorf("folders json = %s (%v)", doc["folders"], err)
	}
	var settings map[string]any
	if err := json.Unmarshal(doc["settings"], &settings); err != nil {
		t.Fatal(err)
	}
	if settings["git.openRepositoryInParentFolders"] != "never" || settings["files.exclude"].(map[string]any)["**/.bare"] != true {
		t.Errorf("settings = %v", settings)
	}
	if _, ok := settings["terminal.integrated.commandsToSkipShell"].([]any); !ok {
		t.Errorf("commandsToSkipShell missing: %v", settings)
	}

	tasks := codeTasks(t, doc)
	for _, label := range []string{"ade: api.build", "ade: api.dev", "ade: web.dev", "ade: up"} {
		if _, ok := tasks[label]; !ok {
			t.Fatalf("task %q missing: %v", label, tasks)
		}
	}
	if got := tasks["ade: web.dev"]; strings.Join(got.DependsOn, ",") != "ade: api.dev" || got.Options.CWD != web.Path {
		t.Errorf("web.dev = %+v", got)
	}
	if got := tasks["ade: api.dev"]; strings.Join(got.DependsOn, ",") != "ade: api.build" || got.Options.CWD != api.Path {
		t.Errorf("api.dev = %+v", got)
	}
	if got := tasks["ade: up"]; strings.Join(got.DependsOn, ",") != "ade: web.dev" {
		t.Errorf("up = %+v", got)
	}
	// The item's api tree is pinned for every task run.
	args := strings.Join(tasks["ade: web.dev"].Args, " ")
	if !strings.Contains(args, "--item-tree api="+api.Path) || !strings.Contains(args, "--worktree "+web.Path) {
		t.Errorf("args = %s", args)
	}
	if res.Tasks != len(tasks) {
		t.Errorf("Tasks = %d, file has %d", res.Tasks, len(tasks))
	}
}

func TestCodeWorkspaceFallsBackToTheBaselineWherever_TheItemHasNoTree(t *testing.T) {
	f := newCodeFixture(t, nil, []string{"12-paginate"})
	item := itemWith("Paginate", []forge.Ref{issueRef("grp/web", 12)})
	item.Item.Ref.IID = 5

	res, err := f.mgr.CodeWorkspace(context.Background(), f.input(t, item, nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Folders[1].Repo != "api" || res.Folders[1].Source != FolderBaseline || res.Folders[1].Name != "api (main)" ||
		res.Folders[1].Path != filepath.Join(f.root, "demo", "api", "main") {
		t.Errorf("api = %+v", res.Folders[1])
	}
	doc := readCode(t, res.Path)
	tasks := codeTasks(t, doc)
	// web consumes the shared baseline api; the plan's API dir is not the item's.
	if got := tasks["ade: api.dev"]; got.Options.CWD != res.Folders[1].Path {
		t.Errorf("api.dev cwd = %s", got.Options.CWD)
	}
	if args := strings.Join(tasks["ade: web.dev"].Args, " "); strings.Contains(args, "--item-tree api=") {
		t.Errorf("web pins an api item tree it does not have: %s", args)
	}
}

func TestCodeWorkspaceWithoutAnItemUsesBaselinesAndProjectBranchName(t *testing.T) {
	f := newCodeFixture(t, []string{"3-limit"}, []string{"feat-x"})
	loc, err := Locate(f.root, filepath.Join(f.root, "demo", "api", "main"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.mgr.CodeWorkspace(context.Background(), f.input(t, nil, &loc))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(res.Path) != "demo-main.code-workspace" {
		t.Errorf("name = %s", filepath.Base(res.Path))
	}
	if len(res.Folders) != 2 || res.Folders[0].Repo != "api" {
		t.Fatalf("folders = %+v", res.Folders)
	}
	for _, fo := range res.Folders {
		if fo.Source != FolderBaseline || !strings.HasSuffix(fo.Name, " (main)") {
			t.Errorf("%+v is not a baseline", fo)
		}
	}

	// A feature worktree without an item is its own folder; the rest stay baselines.
	feat, err := Locate(f.root, filepath.Join(f.root, "demo", "web", "feat-x"))
	if err != nil {
		t.Fatal(err)
	}
	res, err = f.mgr.CodeWorkspace(context.Background(), f.input(t, nil, &feat))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(res.Path) != "demo-feat-x.code-workspace" || res.Folders[0].Source != FolderItem || res.Folders[1].Source != FolderBaseline {
		t.Errorf("%s %+v", filepath.Base(res.Path), res.Folders)
	}
}

func TestCodeWorkspaceKeepsUnknownKeysSettingsAndForeignTasks(t *testing.T) {
	f := newCodeFixture(t, nil, nil)
	item := itemWith("Paginate", nil)
	item.Item.Ref.IID = 5
	in := f.input(t, item, nil)
	existing := `{
  "extensions": {"recommendations": ["golang.go"]},
  "launch": {"version": "0.2.0"},
  "folders": [{"path": "/stale"}],
  "settings": {"editor.tabSize": 2, "git.openRepositoryInParentFolders": "always"},
  "tasks": {"version": "2.0.0", "tasks": [
    {"label": "mine", "type": "shell", "command": "echo"},
    {"label": "ade: stale", "type": "shell", "command": "x"}
  ]}
}`
	path := filepath.Join(f.dir, "5-paginate.code-workspace")
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mgr.CodeWorkspace(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	doc := readCode(t, path)
	if !strings.Contains(string(doc["extensions"]), "golang.go") || !strings.Contains(string(doc["launch"]), "0.2.0") {
		t.Errorf("unknown keys lost: %s", mustMarshal(t, doc))
	}
	if strings.Contains(string(doc["folders"]), "/stale") {
		t.Errorf("folders not regenerated: %s", doc["folders"])
	}
	var settings map[string]any
	if err := json.Unmarshal(doc["settings"], &settings); err != nil {
		t.Fatal(err)
	}
	if settings["editor.tabSize"] != float64(2) || settings["git.openRepositoryInParentFolders"] != "never" {
		t.Errorf("settings = %v", settings)
	}
	tasks := codeTasks(t, doc)
	if _, ok := tasks["mine"]; !ok {
		t.Error("foreign task dropped")
	}
	if _, ok := tasks["ade: stale"]; ok {
		t.Error("stale ADE task kept")
	}
	if _, ok := tasks["ade: web.dev"]; !ok {
		t.Error("ADE tasks missing")
	}

	// Comments make the file unparseable: refuse rather than overwrite it.
	if err := os.WriteFile(path, []byte("{ // hi\n}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mgr.CodeWorkspace(context.Background(), in); err == nil || !strings.Contains(err.Error(), "not plain JSON") {
		t.Errorf("err = %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "{ // hi\n}" {
		t.Errorf("file overwritten: %s", b)
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCodeWorkspaceNeedsAnItemOrWorktree(t *testing.T) {
	f := newCodeFixture(t, nil, nil)
	if _, err := f.mgr.CodeWorkspace(context.Background(), f.input(t, nil, nil)); err == nil {
		t.Error("accepted neither item nor worktree")
	}
}

func TestFileSlugAndTruncateName(t *testing.T) {
	for in, want := range map[string]string{
		"Rate-limit the exports API!": "rate-limit-the-exports-api",
		"  ÄÖ  weird__chars  ":        "weird-chars",
		"":                            "",
		"a very long title that goes on and on and on": "a-very-long-title-that-goes-on-and-on",
	} {
		if got := fileSlug(in, 40); got != want {
			t.Errorf("fileSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindItemMatchesTrackerChildAndMR(t *testing.T) {
	mr := forge.MR{Ref: forge.Ref{Project: "grp/api", IID: 4, Kind: forge.KindMR}}
	wi := workItem([]string{"api"}, []forge.Ref{issueRef("grp/api", 3)}, mr)
	items := []ItemRef{
		{Project: "demo", Item: domain.WorkItem{Ref: issueRef("grp/tracker", 99)}},
		{Project: "demo", Item: wi},
	}
	for name, ref := range map[string]forge.Ref{
		"tracker": wi.Ref, "child": issueRef("GRP/api", 3), "mr": mr.Ref,
	} {
		if got, ok := FindItem(items, ref); !ok || got.Item.Ref != wi.Ref {
			t.Errorf("%s: %+v %v", name, got, ok)
		}
	}
	if _, ok := FindItem(items, issueRef("grp/api", 4)); ok {
		t.Error("an issue matched an MR with the same iid")
	}
}

func TestOpenCodeWorkspaceRunsCodeOrHints(t *testing.T) {
	file := "/records/7-x.code-workspace"
	t.Setenv("PATH", t.TempDir())
	opened, hint, err := OpenCodeWorkspace(context.Background(), file)
	if err != nil || opened || !strings.Contains(hint, file) {
		t.Fatalf("without code: %v %q %v", opened, hint, err)
	}

	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + log + "\n"
	if err := os.WriteFile(filepath.Join(bin, "code"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	opened, hint, err = OpenCodeWorkspace(context.Background(), file)
	if err != nil || !opened || hint != "" {
		t.Fatalf("with code: %v %q %v", opened, hint, err)
	}
	if got, _ := os.ReadFile(log); strings.TrimSpace(string(got)) != file {
		t.Errorf("code args = %q", got)
	}
}
