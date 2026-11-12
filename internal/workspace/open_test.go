package workspace

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/commitprefix"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/devshell"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/paths"
)

func newApp(t *testing.T, f *fixture, mode string) *app.App {
	t.Helper()
	dir := t.TempDir()
	al, err := audit.Open(filepath.Join(dir, "records"), "user")
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{
		Paths:  paths.Paths{CacheDir: filepath.Join(dir, "cache"), DataDir: filepath.Join(dir, "data")},
		Config: &config.Config{Host: config.Host{Mode: mode}},
		Root:   f.root, Audit: al, Bus: events.NewBus(),
	}
	t.Cleanup(func() { a.Close() })
	// No `code` or `zellij` may be found: Open must never launch or touch a real host from a test.
	t.Setenv("PATH", t.TempDir())
	return a
}

func TestOpenPlainHostGivesAHint(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	a := newApp(t, f, "plain")
	res, err := Open(context.Background(), a, "demo", "web", filepath.Join(f.root, "demo", "web", "main"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Host != HostPlain || !strings.Contains(res.Hint, "ade ws up --no-zellij") || res.Tab != "web/main" || res.Layout != LayoutCode {
		t.Fatalf("%+v", res)
	}
	// The open is audited.
	entries, err := a.Audit.Query(audit.Filter{Action: "workspace.open"})
	if err != nil || len(entries) != 1 || entries[0].Params["host"] != HostPlain {
		t.Fatalf("audit: %v %+v", err, entries)
	}
}

func TestOpenRejectsWrongRepoOrLayout(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	a := newApp(t, f, "plain")
	tree := filepath.Join(f.root, "demo", "web", "main")
	if _, err := Open(context.Background(), a, "demo", "api", tree); err == nil || !strings.Contains(err.Error(), "belongs to demo/web") {
		t.Fatalf("got %v", err)
	}
	if _, err := Open(context.Background(), a, "demo", "web", tree, WithLayout("grid")); err == nil || !strings.Contains(err.Error(), `unknown layout "grid"`) {
		t.Fatalf("got %v", err)
	}
}

func TestOpenVSCodeWritesTasksAndHintsWhenCodeIsMissing(t *testing.T) {
	// A desktop session (no web tty): the hint is about the missing launcher.
	t.Setenv("DISPLAY", ":0")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("VSCODE_IPC_HOOK_CLI", "")
	f := newFixture(t, appManifest, "api/main", "web/main")
	a := newApp(t, f, "vscode")
	tree := filepath.Join(f.root, "demo", "web", "main")
	res, err := Open(context.Background(), a, "demo", "web", tree)
	if err != nil {
		t.Fatal(err)
	}
	if res.Host != HostVSCode || res.TasksFile != filepath.Join(tree, ".vscode", "tasks.json") || !strings.Contains(res.Hint, "code is not on PATH") {
		t.Fatalf("%+v", res)
	}
	data, err := os.ReadFile(res.TasksFile)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Tasks []struct{ Label string } }
	if err := json.Unmarshal(data, &doc); err != nil || len(doc.Tasks) != 4 {
		t.Fatalf("tasks.json: %v %s", err, data)
	}
}

func TestOpenWithoutWorkspaceStillLayoutsTheTab(t *testing.T) {
	m := strings.Replace(appManifest, "[workspaces]\ndefault = { layout = \"code\", run = \"dev\" }", "", 1)
	f := newFixture(t, m, "api/main", "web/main")
	a := newApp(t, f, "plain")
	res, err := Open(context.Background(), a, "demo", "web", filepath.Join(f.root, "demo", "web", "main"), WithTitle("418-paginate"), WithLayout(LayoutManual))
	if err != nil {
		t.Fatal(err)
	}
	if res.Tab != "web/418-paginate" || res.Layout != LayoutManual {
		t.Fatalf("%+v", res)
	}
	kdl, err := RenderLayout(context.Background(), a, "demo", "web", filepath.Join(f.root, "demo", "web", "main"))
	if err != nil || strings.Contains(kdl, "task") {
		t.Fatalf("a repo with no workspace runs nothing: %v\n%s", err, kdl)
	}
}

func TestRenderLayoutPutsServicesInTheBackgroundTab(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	a := newApp(t, f, "plain")
	tree := filepath.Join(f.root, "demo", "web", "main")
	kdl, err := RenderLayout(context.Background(), a, "demo", "web", tree)
	if err != nil {
		t.Fatal(err)
	}
	// The main tab keeps the workspace's own task, suspended, and no services or Up pane.
	if !strings.Contains(kdl, `name="web.dev" start_suspended=true`) || strings.Contains(kdl, `"api.dev"`) || strings.Contains(kdl, `"ws" "up"`) {
		t.Fatalf("main tab:\n%s", kdl)
	}
	svc, ok, err := RenderServicesLayout(context.Background(), a, "demo", "web", tree)
	if err != nil || !ok {
		t.Fatalf("services tab: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(svc, `tab name="web/main · services"`) || strings.Contains(svc, "focus=true") ||
		!strings.Contains(svc, `name="api.dev"`) || !strings.Contains(svc, `"task" "run" "api.dev" "--worktree" "`+tree+`" "--after-install" "--wait-requires"`) {
		t.Fatalf("services tab:\n%s", svc)
	}
	if strings.Contains(svc, "start_suspended") || strings.Contains(svc, `"api.build"`) || strings.Contains(svc, `"web.dev"`) {
		t.Fatalf("services tab holds only the services, none suspended, oneshots run with their dependents:\n%s", svc)
	}
}

func TestRenderLayoutManualServicesKeepTheSuspendedStrip(t *testing.T) {
	m := strings.Replace(appManifest, `default = { layout = "code", run = "dev" }`, `default = { layout = "code", run = "dev", services = "manual" }`, 1)
	f := newFixture(t, m, "api/main", "web/main")
	a := newApp(t, f, "plain")
	tree := filepath.Join(f.root, "demo", "web", "main")
	kdl, err := RenderLayout(context.Background(), a, "demo", "web", tree)
	if err != nil {
		t.Fatal(err)
	}
	_, strip, ok := strings.Cut(kdl, `size="30%" split_direction="vertical"`)
	if !ok || !strings.Contains(strip, `name="api.dev" start_suspended=true`) || !strings.Contains(strip, `"ws" "up"`) {
		t.Fatalf("manual keeps today's strip:\n%s", kdl)
	}
	if strings.Contains(kdl, "--after-install") || strings.Contains(kdl, "--wait-requires") {
		t.Fatalf("manual panes start by hand with no waits:\n%s", kdl)
	}
	if _, ok, err := RenderServicesLayout(context.Background(), a, "demo", "web", tree); err != nil || ok {
		t.Fatalf("manual opens no services tab: ok=%v err=%v", ok, err)
	}
}

func TestRenderServicesLayoutRunsTheInstallFirst(t *testing.T) {
	m := strings.Replace(appManifest, "[repos.web.tasks.dev]", "[repos.web.hooks]\ninstall = [\"npm ci\"]\n[repos.web.tasks.dev]", 1)
	f := newFixture(t, m, "api/main", "web/main")
	a := newApp(t, f, "plain")
	tree := filepath.Join(f.root, "demo", "web", "main")
	svc, ok, err := RenderServicesLayout(context.Background(), a, "demo", "web", tree)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	install := strings.Index(svc, `name="install"`)
	if install < 0 || install > strings.Index(svc, `name="api.dev"`) || !strings.Contains(svc, `"ws" "install" "`+tree+`" "--workspace" "default"`) {
		t.Fatalf("install pane first:\n%s", svc)
	}
}

// A review tab is for reading a diff: it carries no service or task panes even
// when the manifest's workspace has a run group.
func TestRenderLayoutOfAReviewTabRunsNothing(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	a := newApp(t, f, "plain")
	tree := filepath.Join(f.root, "demo", "web", "main")
	kdl, err := RenderLayout(context.Background(), a, "demo", "web", tree, WithLayout(LayoutReview), WithBaseRef("origin/main"), WithTitle("review-7"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(kdl, `"task"`) || strings.Contains(kdl, "overview") || !strings.Contains(kdl, `name="diff"`) || !strings.Contains(kdl, `tab name="web/review-7"`) {
		t.Fatalf("a review tab shows the diff and starts nothing:\n%s", kdl)
	}
}

// onlyGitOnPath leaves git, and nothing else (no `code`, no `zellij`), on PATH.
func onlyGitOnPath(t *testing.T) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	bin := t.TempDir()
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

// An item with no child issue in the repo gets {child} from the branch it is
// on: Open writes ade.commitPrefix into a worktree on a 57-... branch.
func TestOpenWritesTheBranchNumberCommitPrefix(t *testing.T) {
	f := newFixture(t, gitManifest+"[conventions]\ncommit_prefix = \"GL-{child}\"\n")
	gitRepo(t, filepath.Join(f.root, "demo", "api"), "57-x")
	gitRepo(t, filepath.Join(f.root, "demo", "web"))
	a := newApp(t, f, "plain")
	onlyGitOnPath(t)
	tree := filepath.Join(f.root, "demo", "api", "57-x")
	item := ItemRef{Project: "demo", Forge: "sandbox", Item: workItem([]string{"api"}, nil)}
	res, err := Open(context.Background(), a, "demo", "api", tree, WithItem(item))
	if err != nil || len(res.Warnings) != 0 {
		t.Fatalf("open: %v %+v", err, res)
	}
	if got := commitprefix.Read(context.Background(), tree); got != "GL-57" {
		t.Errorf("ade.commitPrefix = %q, want GL-57 (not the tracker iid 7)", got)
	}
}

// With project.packages and a generated .ade/shell, the tab's panes start with
// the script, and the implement skill carries the project's AGENTS.md.
func TestOpenWrapsPanesInTheProjectShellAndBriefsTheAgentWithItsAgents(t *testing.T) {
	m := strings.Replace(gitManifest, `name = "demo"`, "name = \"demo\"\n[project]\npackages = [\"go\"]", 1)
	f := newFixture(t, m)
	gitRepo(t, filepath.Join(f.root, "demo", "api"), "7-x")
	gitRepo(t, filepath.Join(f.root, "demo", "web"))
	projectDir := filepath.Join(f.root, "demo")
	script := devshell.ProjectShellPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(devshell.ProjectScript("", []string{"go"})), 0o755); err != nil {
		t.Fatal(err)
	}
	agents := "<!-- Generated by ADE from project.agents of the manifest; edit AGENTS.src.md, not this file. -->\n\nEvery service logs JSON.\n"
	if err := os.WriteFile(filepath.Join(projectDir, "AGENTS.md"), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newApp(t, f, "plain")
	onlyGitOnPath(t)
	tree := filepath.Join(projectDir, "api", "7-x")
	item := ItemRef{Project: "demo", Forge: "sandbox", Item: workItem([]string{"api"}, nil)}
	finder := devshell.Finder{LookPath: func(string) (string, error) { return "/bin/tool", nil }}
	p, err := prepare(context.Background(), a, "demo", "api", tree, []OpenOption{WithLayout(LayoutAgent), WithItem(item), WithDevshellFinder(finder)})
	if err != nil {
		t.Fatal(err)
	}
	kdl, err := BuildLayout(layoutSpec(context.Background(), p))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(kdl, `command="`+script+`"`) {
		t.Errorf("panes do not start with the project shell %s:\n%s", script, kdl)
	}
	if p.agent == nil || !strings.Contains(RenderSkill(p.agent.input), "Every service logs JSON.") || strings.Contains(RenderSkill(p.agent.input), "Generated by ADE") {
		t.Errorf("skill lacks the project guidance (or carries ADE's header): %+v", p.agent)
	}
}
