package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// projectManifest is appManifest with two project-level services: db (http
// readiness, like the api) and cache, which api.build requires.
func projectManifest() string {
	m := strings.Replace(appManifest, "[groups]", `[services.db]
cmd   = "WS_HELPER=http {{HELPER}}"
port  = "auto"
env   = { PORT = "{port}" }
ready = { http = "http://127.0.0.1:{port}/healthz", timeout = "20s" }
[services.cache]
cmd   = "WS_HELPER=http {{HELPER}}"
port  = "auto"
env   = { PORT = "{port}" }
ready = { http = "http://127.0.0.1:{port}/healthz", timeout = "20s" }
[groups]
stack = ["services.db", "api.dev"]`, 1)
	return m
}

func projectLoc(t *testing.T, f *fixture) Location {
	t.Helper()
	loc, err := LocateProject(f.root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// projectUp is what `ade services up --detach` does.
func projectUp(t *testing.T, f *fixture, nodes ...string) []Result {
	t.Helper()
	loc := projectLoc(t, f)
	ctx := context.Background()
	plan, err := f.mgr.Plan(ctx, loc, PlanOptions{Nodes: nodes})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	res, err := f.mgr.Up(ctx, plan, UpOptions{Holder: loc.Holder()})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, r := range res {
		if r.Outcome != OutcomeReady {
			t.Fatalf("%s: %s %s", r.Node, r.Outcome, r.Detail)
		}
	}
	return res
}

func TestLocateProjectHasNoTreeAndHoldsAsTheProject(t *testing.T) {
	f := newFixture(t, projectManifest(), "api/main", "web/main")
	loc := projectLoc(t, f)
	if loc.Project != "demo" || loc.Tree != "" || loc.Repo != "" || loc.Holder() != "project:demo" {
		t.Fatalf("loc = %+v holder %q", loc, loc.Holder())
	}
	if got := f.loc("web/main").Holder(); got != filepath.Join(f.root, "demo", "web", "main") {
		t.Errorf("a worktree holds as itself, got %q", got)
	}
	for _, bad := range []string{"", "../demo", "a/b", ".hidden", "nope"} {
		if _, err := LocateProject(f.root, bad); err == nil {
			t.Errorf("LocateProject(%q) succeeded", bad)
		}
	}
	names, err := ProjectNames(f.root)
	if err != nil || !slices.Equal(names, []string{"demo"}) {
		t.Fatalf("ProjectNames = %v, %v", names, err)
	}
	if name, ok := ProjectOf(f.root, filepath.Join(f.root, "demo", "web", "main")); !ok || name != "demo" {
		t.Errorf("ProjectOf inside = %q %v", name, ok)
	}
	if _, ok := ProjectOf(f.root, t.TempDir()); ok {
		t.Error("ProjectOf outside the root said yes")
	}
}

func TestResolveNodesTakesIdsServiceNamesAndGroups(t *testing.T) {
	m := newFixture(t, projectManifest(), "api/main").loc("api/main").Manifest
	tests := []struct {
		names []string
		want  []string
	}{
		{nil, []string{"services.cache", "services.db"}}, // every service
		{[]string{"db"}, []string{"services.db"}},
		{[]string{"services.db", "api.dev"}, []string{"services.db", "api.dev"}},
		{[]string{"stack"}, []string{"services.db", "api.dev"}},
		{[]string{"db", "stack"}, []string{"services.db", "api.dev"}}, // no duplicates
	}
	for _, tt := range tests {
		got, err := ResolveNodes(m, tt.names)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("ResolveNodes(%v) = %v, %v; want %v", tt.names, got, err, tt.want)
		}
	}
	if _, err := ResolveNodes(m, []string{"ghost"}); err == nil || !strings.Contains(err.Error(), `no node or group "ghost"`) || !strings.Contains(err.Error(), "services.db") {
		t.Errorf("unknown name: %v", err)
	}
	bare := newFixture(t, strings.Replace(appManifest, "[groups]", "[groups]\nx = []", 1), "api/main").loc("api/main").Manifest
	if _, err := ResolveNodes(bare, nil); err == nil || !strings.Contains(err.Error(), "declares no [services.*]") {
		t.Errorf("no services: %v", err)
	}
}

func TestProjectUpRunsServicesOverBaselinesAndSharesWithWorkspaces(t *testing.T) {
	f := newFixture(t, projectManifest(), "api/main", "web/main")
	// The web workspace runs api.dev from the baseline, held by its worktree.
	_, wsRes := f.up("web/main", PlanOptions{})
	apiWS := byNode(wsRes, "api.dev")

	res := projectUp(t, f, "services.db", "api.dev")
	api := byNode(res, "api.dev")
	if !api.Shared || api.PID != apiWS.PID {
		t.Fatalf("project-level up started api.dev again: ws %+v, project %+v", apiWS, api)
	}
	if db := byNode(res, "services.db"); db.Shared || db.Tree != filepath.Join(f.root, "demo") {
		t.Fatalf("services.db: %+v", db)
	}
	if api.Tree != filepath.Join(f.root, "demo", "api", "main") {
		t.Errorf("api runs in %s, want the baseline", api.Tree)
	}

	// Status across the project: the services, and every node with an instance.
	loc := projectLoc(t, f)
	st, err := f.mgr.ProjectStatus(context.Background(), loc, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ServiceStatus{}
	for _, s := range st {
		got[s.Node] = s
	}
	var names []string
	for _, s := range st {
		names = append(names, s.Node)
	}
	if want := []string{"api.build", "api.dev", "services.cache", "services.db", "web.dev"}; !slices.Equal(names, want) {
		t.Fatalf("status lists %v, want %v", names, want)
	}
	if s := got["services.cache"]; s.State != "stopped" || len(s.Instances) != 0 || !s.Declared || s.Kind != "service" {
		t.Errorf("declared but not running: %+v", s)
	}
	db := got["services.db"]
	if db.State != StateReady || len(db.Instances) != 1 || !slices.Equal(db.Instances[0].Holders, []string{"project:demo"}) {
		t.Errorf("services.db: %+v", db)
	}
	ws := filepath.Join(f.root, "demo", "web", "main")
	if holders := got["api.dev"].Instances[0].Holders; !slices.Contains(holders, "project:demo") || !slices.Contains(holders, ws) {
		t.Errorf("api.dev holders = %v, want the project and the web worktree", holders)
	}
	if got["web.dev"].State != StateReady {
		t.Errorf("web.dev: %+v", got["web.dev"])
	}

	// Naming nodes narrows the report, running or not.
	st, err = f.mgr.ProjectStatus(context.Background(), loc, []string{"services.cache", "api.dev"})
	if err != nil || len(st) != 2 || st[0].Node != "api.dev" || st[1].Node != "services.cache" {
		t.Fatalf("named status: %+v %v", st, err)
	}
}

func TestProjectDownStopsOnlyWhatNoWorkspaceHoldsUnlessForced(t *testing.T) {
	f := newFixture(t, projectManifest(), "api/main", "web/main")
	f.up("web/main", PlanOptions{})
	res := projectUp(t, f, "services.db", "api.dev")
	dbPID, apiPID := byNode(res, "services.db").PID, byNode(res, "api.dev").PID
	ctx := context.Background()
	loc := projectLoc(t, f)

	// Another project's instance: never selected.
	other := Row{ID: "x.dev@/elsewhere", Node: "x.dev", Tree: "/elsewhere", Project: "other", Kind: "service", State: StateReady, Log: "/dev/null"}
	if err := f.mgr.put(other); err != nil {
		t.Fatal(err)
	}

	// Named: only that node; api.dev is still held by the web workspace.
	down, err := f.mgr.Down(ctx, "demo", DownOptions{Holder: loc.Holder(), Nodes: []string{"api.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(down.Stopped) != 0 || len(down.StillHeld) != 1 || down.StillHeld[0].Node != "api.dev" {
		t.Fatalf("down api.dev: %+v", down)
	}
	if want := filepath.Join(f.root, "demo", "web", "main"); !slices.Equal(down.StillHeld[0].Holders, []string{want}) {
		t.Errorf("still held by %v, want only %s: the project's hold is gone", down.StillHeld[0].Holders, want)
	}
	if !processAlive(apiPID, 0) || !processAlive(dbPID, 0) {
		t.Fatal("a named, still held node stopped, or an unnamed one did")
	}

	// Everything of the project: db stops, api.dev stays (held), the other project is untouched.
	down, err = f.mgr.Down(ctx, "demo", DownOptions{Holder: loc.Holder()})
	if err != nil {
		t.Fatal(err)
	}
	if len(down.Stopped) != 1 || down.Stopped[0].Node != "services.db" {
		t.Fatalf("down: %+v", down)
	}
	for _, s := range down.StillHeld {
		if s.Node == "services.db" || s.Node == "x.dev" {
			t.Errorf("%s reported as still held", s.Node)
		}
	}
	if processAlive(dbPID, 0) {
		t.Error("services.db is still running")
	}
	if !processAlive(apiPID, 0) {
		t.Error("api.dev stopped under the web workspace")
	}
	if r, _ := f.mgr.get(other.ID); r == nil {
		t.Error("another project's instance was dropped")
	}

	// Forced: the workspace's holds go too.
	down, err = f.mgr.Down(ctx, "demo", DownOptions{Holder: loc.Holder(), Force: true})
	if err != nil {
		t.Fatal(err)
	}
	var stopped []string
	for _, s := range down.Stopped {
		stopped = append(stopped, s.Node)
	}
	slices.Sort(stopped)
	if !slices.Equal(stopped, []string{"api.build", "api.dev", "web.dev"}) || len(down.StillHeld) != 0 {
		t.Fatalf("forced down: %+v", down)
	}
	if processAlive(apiPID, 0) {
		t.Error("api.dev survived a forced down")
	}
	if r, _ := f.mgr.get(other.ID); r == nil {
		t.Error("another project's instance was dropped by a forced down")
	}
}

func TestProjectDownStopsKeepServicesOnlyWhenForced(t *testing.T) {
	f := newFixture(t, strings.Replace(projectManifest(), "[services.cache]", "[services.cache]\nkeep = true", 1), "api/main", "web/main")
	res := projectUp(t, f, "services.cache")
	pid := byNode(res, "services.cache").PID
	loc := projectLoc(t, f)
	down, err := f.mgr.Down(context.Background(), "demo", DownOptions{Holder: loc.Holder()})
	if err != nil || len(down.Stopped) != 0 || !processAlive(pid, 0) {
		t.Fatalf("a keep service must survive down: %+v %v", down, err)
	}
	down, err = f.mgr.Down(context.Background(), "demo", DownOptions{Holder: loc.Holder(), Force: true})
	if err != nil || len(down.Stopped) != 1 || processAlive(pid, 0) {
		t.Fatalf("a forced down stops it: %+v %v", down, err)
	}
}

func TestProjectServicesSpecBuildsTheStandaloneTab(t *testing.T) {
	f := newFixture(t, strings.Replace(projectManifest(), "[repos.web.tasks.dev]", "[repos.web.hooks]\ninstall = [\"true\"]\n[repos.web.tasks.dev]", 1), "api/main", "web/main")
	loc := projectLoc(t, f)
	plan, err := f.mgr.Plan(context.Background(), loc, PlanOptions{Nodes: []string{"services.db", "web.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	trees := TreeOptions{Pins: map[string]string{"api": "feature"}}
	spec := ProjectServicesSpec(loc, plan, "/opt/ade", []string{"services.db", "web.dev"}, trees, true)
	if spec.ServicesTab() != "demo · services" || spec.Dir != loc.ProjectDir || !spec.HasServicesTab() {
		t.Fatalf("spec: %+v", spec)
	}
	var panes []string
	for _, p := range spec.Services {
		panes = append(panes, p.Name)
	}
	// api.build is a oneshot nobody asked for: it runs as part of api.dev's pane.
	if want := []string{"services.db", "api.dev", "web.dev"}; !slices.Equal(panes, want) {
		t.Fatalf("panes = %v, want %v", panes, want)
	}
	last := spec.Services[len(spec.Services)-1].Args
	if want := []string{"task", "run", "web.dev", "--project", "demo", "--with", "api=feature", "--after-install", "--wait-requires"}; !slices.Equal(last, want) {
		t.Errorf("pane args = %q, want %q", last, want)
	}
	if want := []string{"services", "install", "services.db", "web.dev", "--project", "demo", "--with", "api=feature"}; !slices.Equal(spec.Install.Args, want) {
		t.Errorf("install args = %q, want %q", spec.Install.Args, want)
	}
	kdl, err := BuildServicesLayout(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`tab name="demo · services" cwd="` + loc.ProjectDir + `" {`, `pane name="install"`, `pane name="web.dev"`} {
		if !strings.Contains(kdl, want) {
			t.Errorf("layout lacks %q:\n%s", want, kdl)
		}
	}
	if strings.Contains(kdl, "focus=true") || strings.Contains(kdl, "start_suspended") {
		t.Errorf("the tab runs unfocused and unsuspended:\n%s", kdl)
	}
}

func TestBuildTasksRunsInTheNodeDir(t *testing.T) {
	f, tree := dirFixture(t)
	plan, err := f.mgr.Plan(context.Background(), f.loc("web/main"), PlanOptions{Nodes: []string{"web.where"}})
	if err != nil {
		t.Fatal(err)
	}
	tasks := BuildTasks(plan, "/opt/ade", nil)
	if len(tasks) < 1 || tasks[0].Options == nil || tasks[0].Options.CWD != filepath.Join(tree, "packages", "app") {
		t.Fatalf("tasks: %+v", tasks)
	}
	if got := tasks[0].Args; !slices.Contains(got, tree) { // task run still names the worktree
		t.Errorf("args %q lack the worktree %s", got, tree)
	}
}

func TestOpenProjectTabCreatesOrFocusesTheServicesTab(t *testing.T) {
	f := newFixture(t, projectManifest(), "api/main", "web/main")
	a := newApp(t, f, "plain")
	loc := projectLoc(t, f)
	plan, err := f.mgr.Plan(context.Background(), loc, PlanOptions{Nodes: []string{"services.db"}})
	if err != nil {
		t.Fatal(err)
	}
	spec := ProjectServicesSpec(loc, plan, "/opt/ade", []string{"services.db"}, TreeOptions{}, false)
	res := OpenResult{Tab: spec.ServicesTab(), Layout: "services"}

	z := &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "home\n"}
	got, err := openProjectTab(context.Background(), z.run, a, loc, spec, "ade", res)
	if err != nil || !got.Created || got.Tab != "demo · services" || got.Session != "ade" {
		t.Fatalf("%+v %v", got, err)
	}
	if last := strings.Join(z.calls[len(z.calls)-1], " "); last != "--session ade action new-tab --layout "+got.LayoutFile+" --name demo · services" {
		t.Errorf("call: %s", last)
	}
	if kdl := readFile(t, got.LayoutFile); !strings.Contains(kdl, `args "task" "run" "services.db" "--project" "demo" "--after-install" "--wait-requires"`) {
		t.Errorf("layout:\n%s", kdl)
	}

	// The tab exists: it is focused, not created again.
	z = &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "home\ndemo · services\n"}
	got, err = openProjectTab(context.Background(), z.run, a, loc, spec, "ade", res)
	if err != nil || got.Created {
		t.Fatalf("%+v %v", got, err)
	}
	if last := strings.Join(z.calls[len(z.calls)-1], " "); last != "--session ade action go-to-tab-name demo · services" {
		t.Errorf("call: %s", last)
	}

	// Without a session the open says how to start one.
	z = &fakeZellij{}
	if _, err = openProjectTab(context.Background(), z.run, a, loc, spec, "ade", res); !errors.Is(err, ErrNoSession) {
		t.Errorf("err = %v", err)
	}
}
