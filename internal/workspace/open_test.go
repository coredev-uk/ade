package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
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

func TestRenderLayoutGivesEveryServiceItsOwnPane(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	a := newApp(t, f, "plain")
	kdl, err := RenderLayout(context.Background(), a, "demo", "web", filepath.Join(f.root, "demo", "web", "main"))
	if err != nil {
		t.Fatal(err)
	}
	_, strip, ok := strings.Cut(kdl, `size="30%" split_direction="vertical"`)
	if !ok || !strings.Contains(strip, `name="api.dev"`) || !strings.Contains(strip, `"task" "run" "api.dev"`) {
		t.Fatalf("api.dev belongs in the services strip:\n%s", kdl)
	}
	if strings.Index(kdl, `"task" "run" "web.dev"`) < strings.Index(kdl, `"task" "run" "api.dev"`) {
		t.Fatalf("web.dev is the workspace's own task, listed after the services:\n%s", kdl)
	}
	if strings.Contains(kdl, `"api.build"`) {
		t.Fatalf("oneshots run as part of their dependents, no pane:\n%s", kdl)
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
