package workspace

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/store"
)

// TestMain doubles as the service the tests start: a node whose command is
// "WS_HELPER=<mode> <this binary>" behaves as the mode says.
func TestMain(m *testing.M) {
	if mode := os.Getenv("WS_HELPER"); mode != "" {
		helper(mode)
		return
	}
	os.Exit(m.Run())
}

func helper(mode string) {
	port := os.Getenv("PORT")
	switch mode {
	case "http": // 200 on /healthz, like a real API
		ln, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			fmt.Println("listen:", err)
			os.Exit(2)
		}
		fmt.Println("api listening on :" + port)
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") })
		http.Serve(ln, mux) //nolint:errcheck
	case "web": // reports which API it was pointed at, then idles
		fmt.Println("web ready api=" + os.Getenv("API_URL") + " dir=" + os.Getenv("CODE_DIR"))
		time.Sleep(time.Hour)
	case "fail":
		fmt.Println("boom: cannot start")
		os.Exit(3)
	case "oneshot":
		fmt.Println("migrated")
	case "oneshot-fail":
		fmt.Println("migration failed")
		os.Exit(1)
	}
}

type fixture struct {
	t      *testing.T
	root   string
	mgr    *Manager
	helper string
}

// newFixture lays out <root>/demo/{api,web}/{main,...} with a project
// snapshot holding the given manifest body (the helper binary is {{HELPER}}).
func newFixture(t *testing.T, manifest string, trees ...string) *fixture {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	for _, tree := range trees {
		if err := os.MkdirAll(filepath.Join(root, "demo", tree), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest = strings.ReplaceAll(manifest, "{{HELPER}}", exe)
	snap := fmt.Sprintf("source = %q\nmanifest = '''\n%s'''\n", "test", manifest)
	if err := os.WriteFile(filepath.Join(root, "demo", ".project.toml"), []byte(snap), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mgr := NewManagerAt(st, filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "ws"))
	mgr.Grace = 2 * time.Second
	t.Cleanup(func() { // never leak a process whatever the test did
		rows, _ := mgr.Status(context.Background())
		for _, r := range rows {
			stopGroup(r.PID, r.pstart, time.Second)
		}
	})
	return &fixture{t: t, root: root, mgr: mgr, helper: exe}
}

func (f *fixture) loc(tree string) Location {
	f.t.Helper()
	loc, err := Locate(f.root, filepath.Join(f.root, "demo", tree))
	if err != nil {
		f.t.Fatal(err)
	}
	return loc
}

// appManifest is api (http readiness) <- web (log readiness), api having a
// oneshot build step and web a baseline api.
const appManifest = `
name = "demo"
[repos.api]
path = "demo/api"
default_branch = "main"
[repos.api.tasks.build]
cmd  = "WS_HELPER=oneshot {{HELPER}}"
kind = "oneshot"
[repos.api.tasks.dev]
cmd      = "WS_HELPER=http {{HELPER}}"
port     = "auto"
requires = ["api.build"]
env      = { PORT = "{port}" }
ready    = { http = "http://127.0.0.1:{port}/healthz", timeout = "20s" }
[repos.web]
path = "demo/web"
default_branch = "main"
requires = ["api"]
[repos.web.tasks.dev]
cmd      = "WS_HELPER=web {{HELPER}}"
port     = "auto"
requires = ["api.dev"]
env      = { API_URL = "http://127.0.0.1:{api.dev.port}", CODE_DIR = "{dir}", API_DIR = "{api.dir}" }
ready    = { log = 'web ready api=\S+', timeout = "20s" }
[groups]
dev = ["web.dev"]
[workspaces]
default = { layout = "code", run = "dev" }
`

func (f *fixture) up(tree string, o PlanOptions) (*Plan, []Result) {
	f.t.Helper()
	ctx := context.Background()
	plan, err := f.mgr.Plan(ctx, f.loc(tree), o)
	if err != nil {
		f.t.Fatalf("Plan(%s): %v", tree, err)
	}
	res, err := f.mgr.Up(ctx, plan, UpOptions{Holder: filepath.Join(f.root, "demo", tree)})
	if err != nil {
		f.t.Fatalf("Up(%s): %v", tree, err)
	}
	return plan, res
}

func byNode(rs []Result, node string) Result {
	for _, r := range rs {
		if r.Node == node {
			return r
		}
	}
	return Result{}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestUpStartsGraphInOrderWithInjectedEnv(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	plan, res := f.up("web/main", PlanOptions{})

	if got := len(res); got != 3 {
		t.Fatalf("results: %+v", res)
	}
	for _, r := range res {
		if r.Outcome != OutcomeReady {
			t.Fatalf("%s: %s %s", r.Node, r.Outcome, r.Detail)
		}
	}
	api := byNode(res, "api.dev")
	web := byNode(res, "web.dev")
	if api.Port == 0 || web.Port == 0 || api.Port == web.Port {
		t.Fatalf("ports api=%d web=%d", api.Port, web.Port)
	}
	if want := filepath.Join(f.root, "demo", "api", "main"); api.Tree != want {
		t.Fatalf("api runs in %s, want baseline %s", api.Tree, want)
	}
	// web was started only after api answered, and was told where api is.
	log := readFile(t, web.Log)
	if want := fmt.Sprintf("api=http://127.0.0.1:%d", api.Port); !strings.Contains(log, want) {
		t.Fatalf("web log %q lacks %q", log, want)
	}
	if want := "dir=" + filepath.Join(f.root, "demo", "web", "main"); !strings.Contains(log, want) {
		t.Fatalf("web log %q lacks %q", log, want)
	}
	if ok, _ := httpOK(fmt.Sprintf("http://127.0.0.1:%d/healthz", api.Port)); !ok {
		t.Fatal("api is not serving")
	}
	if plan.Trees["api"].Source != SourceBaseline {
		t.Fatalf("api tree source %q", plan.Trees["api"].Source)
	}
}

func httpOK(url string) (bool, error) {
	resp, err := http.Get(url) //nolint:gosec,noctx // loopback test server
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == 200, nil
}

func TestSecondWorkspaceSharesBaselineInstanceAndLastReleaseStopsIt(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main", "web/feature")
	_, first := f.up("web/main", PlanOptions{})
	_, second := f.up("web/feature", PlanOptions{})

	api1, api2 := byNode(first, "api.dev"), byNode(second, "api.dev")
	if api1.Shared || !api2.Shared {
		t.Fatalf("shared flags: first=%v second=%v", api1.Shared, api2.Shared)
	}
	if api1.PID != api2.PID || api1.Port != api2.Port {
		t.Fatalf("api started twice: %d:%d vs %d:%d", api1.PID, api1.Port, api2.PID, api2.Port)
	}
	web1, web2 := byNode(first, "web.dev"), byNode(second, "web.dev")
	if web1.Port == web2.Port || web1.PID == web2.PID {
		t.Fatal("each web worktree must have its own instance")
	}
	if !strings.Contains(readFile(t, web2.Log), fmt.Sprintf("api=http://127.0.0.1:%d", api1.Port)) {
		t.Fatal("second web is not pointed at the shared api")
	}
	// The oneshot is shared too: it did not run again for the second workspace.
	if !byNode(second, "api.build").Shared {
		t.Fatal("api.build re-ran while the first workspace still held it")
	}

	ctx := context.Background()
	stopped, err := f.mgr.Release(ctx, filepath.Join(f.root, "demo", "web", "main"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 1 || stopped[0].Node != "web.dev" {
		t.Fatalf("first release must stop only its web: %+v", stopped)
	}
	if !processAlive(api1.PID, 0) {
		t.Fatal("api stopped while the second workspace still needs it")
	}
	stopped, err = f.mgr.Release(ctx, filepath.Join(f.root, "demo", "web", "feature"))
	if err != nil {
		t.Fatal(err)
	}
	// Dependents stop before their requirements.
	var order []string
	for _, s := range stopped {
		order = append(order, s.Node)
	}
	if strings.Join(order, ",") != "web.dev,api.dev,api.build" {
		t.Fatalf("stop order %v", order)
	}
	if processAlive(api1.PID, 0) {
		t.Fatal("api still running after the last dependent stopped")
	}
	if rows, _ := f.mgr.Status(ctx); len(rows) != 0 {
		t.Fatalf("instances left behind: %+v", rows)
	}
}

func TestPortsAreStablePerTreeAcrossRestarts(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main", "web/feature")
	ctx := context.Background()
	plan1, err := f.mgr.Plan(ctx, f.loc("web/main"), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plan2, _ := f.mgr.Plan(ctx, f.loc("web/main"), PlanOptions{})
	other, _ := f.mgr.Plan(ctx, f.loc("web/feature"), PlanOptions{})
	web := func(p *Plan) *Instance { in, _ := p.Instance("web.dev"); return in }
	if web(plan1).Port != web(plan2).Port {
		t.Fatalf("port moved between plans: %d -> %d", web(plan1).Port, web(plan2).Port)
	}
	if web(plan1).Port == web(other).Port {
		t.Fatal("two worktrees share a web port")
	}
	api1, _ := plan1.Instance("api.dev")
	api2, _ := other.Instance("api.dev")
	if api1.ID != api2.ID || api1.Port != api2.Port {
		t.Fatal("both web worktrees must resolve to one api instance")
	}
}

func TestFeatureTreeGetsItsOwnInstanceAndPinsBeatBaseline(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "api/feature-api", "web/main", "web/feature")
	ctx := context.Background()
	baseline, err := f.mgr.Plan(ctx, f.loc("web/feature"), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.mgr.Plan(ctx, f.loc("web/feature"), PlanOptions{Trees: TreeOptions{ItemTrees: map[string]string{"api": filepath.Join(f.root, "demo", "api", "feature-api")}}})
	if err != nil {
		t.Fatal(err)
	}
	a0, _ := baseline.Instance("api.dev")
	a1, _ := item.Instance("api.dev")
	if a0.ID == a1.ID || a0.Port == a1.Port {
		t.Fatalf("feature api must be a separate instance: %s:%d vs %s:%d", a0.ID, a0.Port, a1.ID, a1.Port)
	}
	if item.Trees["api"].Source != SourceItem || !strings.HasSuffix(a1.Dir, "api/feature-api") {
		t.Fatalf("item tree not used: %+v", item.Trees["api"])
	}
	w1, _ := item.Instance("web.dev")
	if !strings.Contains(w1.Env["API_URL"], fmt.Sprint(a1.Port)) || w1.Env["API_DIR"] != a1.Dir {
		t.Fatalf("env follows the feature tree: %v", w1.Env)
	}

	pin, err := f.mgr.Plan(ctx, f.loc("web/feature"), PlanOptions{Trees: TreeOptions{
		Pins:      map[string]string{"api": filepath.Join(f.root, "demo", "api", "main")},
		ItemTrees: map[string]string{"api": filepath.Join(f.root, "demo", "api", "feature-api")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if pin.Trees["api"].Source != SourcePin || !strings.HasSuffix(pin.Trees["api"].Path, "api/main") {
		t.Fatalf("an explicit pin must win: %+v", pin.Trees["api"])
	}
}

func TestPinOutsideRepoIsRejected(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	_, err := f.mgr.Plan(context.Background(), f.loc("web/main"), PlanOptions{Trees: TreeOptions{Pins: map[string]string{"api": filepath.Join(f.root, "demo", "web", "main")}}})
	if err == nil || !strings.Contains(err.Error(), "not a worktree of repo api") {
		t.Fatalf("got %v", err)
	}
}

func TestMissingBaselineIsActionable(t *testing.T) {
	f := newFixture(t, appManifest, "web/main") // no api/main
	_, err := f.mgr.Plan(context.Background(), f.loc("web/main"), PlanOptions{})
	if err == nil || !strings.Contains(err.Error(), "baseline tree of api is missing") {
		t.Fatalf("got %v", err)
	}
}

func TestFailedRequirementBlocksDependentsOnly(t *testing.T) {
	manifest := strings.Replace(appManifest, `cmd      = "WS_HELPER=http {{HELPER}}"`, `cmd      = "WS_HELPER=fail {{HELPER}}"`, 1)
	manifest = strings.Replace(manifest, `ready    = { http = "http://127.0.0.1:{port}/healthz", timeout = "20s" }`, `ready    = { http = "http://127.0.0.1:{port}/healthz", timeout = "5s" }`, 1)
	manifest += "\n[repos.web.tasks.lint]\ncmd = \"WS_HELPER=oneshot {{HELPER}}\"\nkind = \"oneshot\"\n"
	f := newFixture(t, manifest, "api/main", "web/main")
	_, res := f.up("web/main", PlanOptions{Nodes: []string{"web.dev", "web.lint"}})

	api := byNode(res, "api.dev")
	if api.Outcome != OutcomeFailed || !strings.Contains(api.Detail, "boom: cannot start") {
		t.Fatalf("api: %+v", api)
	}
	web := byNode(res, "web.dev")
	if web.Outcome != OutcomeBlocked || !strings.Contains(web.Detail, "blocked: api.dev not ready") || !strings.Contains(web.Detail, "boom: cannot start") {
		t.Fatalf("web must say why it is blocked: %+v", web)
	}
	if lint := byNode(res, "web.lint"); lint.Outcome != OutcomeReady {
		t.Fatalf("an unrelated node still runs: %+v", lint)
	}
	rows, _ := f.mgr.Status(context.Background())
	for _, r := range rows {
		if r.Node == "web.dev" || r.Node == "api.dev" {
			t.Fatalf("failed or blocked node left a row: %+v", r)
		}
	}
}

func TestReadinessTimeoutStopsTheProcess(t *testing.T) {
	manifest := strings.Replace(appManifest, `cmd      = "WS_HELPER=http {{HELPER}}"`, `cmd      = "WS_HELPER=web {{HELPER}}"`, 1) // never serves
	manifest = strings.Replace(manifest, `timeout = "20s" }
[repos.web]`, `timeout = "1s" }
[repos.web]`, 1)
	f := newFixture(t, manifest, "api/main", "web/main")
	_, res := f.up("web/main", PlanOptions{})
	api := byNode(res, "api.dev")
	if api.Outcome != OutcomeFailed || !strings.Contains(api.Detail, "timeout 1s") {
		t.Fatalf("api: %+v", api)
	}
	if api.PID == 0 || processAlive(api.PID, 0) {
		t.Fatalf("timed-out process must be stopped (pid %d)", api.PID)
	}
}

func TestFailingOneshotBlocksDependents(t *testing.T) {
	manifest := strings.Replace(appManifest, `cmd  = "WS_HELPER=oneshot {{HELPER}}"`, `cmd  = "WS_HELPER=oneshot-fail {{HELPER}}"`, 1)
	f := newFixture(t, manifest, "api/main", "web/main")
	_, res := f.up("web/main", PlanOptions{})
	build := byNode(res, "api.build")
	if build.Outcome != OutcomeFailed || !strings.Contains(build.Detail, "exit status 1") || !strings.Contains(build.Detail, "migration failed") {
		t.Fatalf("build: %+v", build)
	}
	if byNode(res, "api.dev").Outcome != OutcomeBlocked {
		t.Fatalf("api must not start after a failed oneshot: %+v", byNode(res, "api.dev"))
	}
}

func TestKeepInstanceSurvivesLastRelease(t *testing.T) {
	manifest := strings.Replace(appManifest, `requires = ["api.build"]`, `requires = ["api.build"]
keep     = true`, 1)
	f := newFixture(t, manifest, "api/main", "web/main")
	_, res := f.up("web/main", PlanOptions{})
	api := byNode(res, "api.dev")
	stopped, err := f.mgr.Release(context.Background(), filepath.Join(f.root, "demo", "web", "main"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stopped {
		if s.Node == "api.dev" {
			t.Fatal("keep=true instance was stopped")
		}
	}
	if !processAlive(api.PID, 0) {
		t.Fatal("keep=true instance died")
	}
	// A later workspace reuses it instead of starting a second one.
	_, again := f.up("web/main", PlanOptions{})
	if r := byNode(again, "api.dev"); !r.Shared || r.PID != api.PID {
		t.Fatalf("kept instance not reused: %+v", r)
	}
}

func TestCrashedHolderDoesNotKeepInstanceAlive(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	ctx := context.Background()
	plan, err := f.mgr.Plan(ctx, f.loc("web/main"), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A holder process that has already died (a crashed `ade ws up`).
	dead := startSleeper(t)
	dead.Kill()
	dead.Wait()
	res, err := f.mgr.Up(ctx, plan, UpOptions{Holder: "ghost", HolderPID: dead.Pid})
	if err != nil {
		t.Fatal(err)
	}
	api := byNode(res, "api.dev")
	if !processAlive(api.PID, 0) {
		t.Fatal("api not running")
	}
	stopped, err := f.mgr.Reap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stopped) == 0 || processAlive(api.PID, 0) {
		t.Fatalf("instances held only by a dead process must be reaped: %+v", stopped)
	}
}

func TestStaleRowIsReplaced(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	_, res := f.up("web/main", PlanOptions{})
	api := byNode(res, "api.dev")
	stopGroup(api.PID, 0, time.Second) // killed behind ADE's back
	_, again := f.up("web/main", PlanOptions{})
	r := byNode(again, "api.dev")
	if r.Outcome != OutcomeReady || r.Shared || r.PID == api.PID {
		t.Fatalf("dead instance must be restarted, not reused: %+v", r)
	}
	if r.Port != api.Port {
		t.Fatalf("port must stay stable across the restart: %d -> %d", api.Port, r.Port)
	}
}

func TestPlanRejectsCycleWithPath(t *testing.T) {
	manifest := appManifest + `
[repos.api.tasks.a]
cmd = "true"
requires = ["api.b"]
[repos.api.tasks.b]
cmd = "true"
requires = ["api.a"]
`
	f := newFixture(t, manifest, "api/main", "web/main")
	_, err := f.mgr.Plan(context.Background(), f.loc("api/main"), PlanOptions{Nodes: []string{"api.a"}})
	var ce *CycleError
	if !errors.As(err, &ce) || strings.Join(ce.Path, ",") != "api.a,api.b,api.a" {
		t.Fatalf("got %v", err)
	}
}

func TestUnknownTemplateIsAnError(t *testing.T) {
	manifest := strings.Replace(appManifest, `CODE_DIR = "{dir}"`, `CODE_DIR = "{nope}"`, 1)
	f := newFixture(t, manifest, "api/main", "web/main")
	_, err := f.mgr.Plan(context.Background(), f.loc("web/main"), PlanOptions{})
	if err == nil || !strings.Contains(err.Error(), "unknown template variable {nope}") {
		t.Fatalf("got %v", err)
	}
}

func TestShellVariablesAreLeftAlone(t *testing.T) {
	manifest := strings.Replace(appManifest, `CODE_DIR = "{dir}"`, `CODE_DIR = "${HOME}/x"`, 1)
	f := newFixture(t, manifest, "api/main", "web/main")
	plan, err := f.mgr.Plan(context.Background(), f.loc("web/main"), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if in, _ := plan.Instance("web.dev"); in.Env["CODE_DIR"] != "${HOME}/x" {
		t.Fatalf("env: %v", in.Env)
	}
}

func TestLocateRejectsPathsOutsideWorktrees(t *testing.T) {
	f := newFixture(t, appManifest, "api/main")
	for _, p := range []string{f.root, filepath.Join(f.root, "demo"), filepath.Join(f.root, "demo", "api"), t.TempDir()} {
		if _, err := Locate(f.root, p); err == nil {
			t.Fatalf("Locate(%s) accepted a non-worktree", p)
		}
	}
	sub := filepath.Join(f.root, "demo", "api", "main", "pkg")
	os.MkdirAll(sub, 0o755) //nolint:errcheck
	loc, err := Locate(f.root, sub)
	if err != nil || loc.Tree != filepath.Join(f.root, "demo", "api", "main") || loc.Repo != "api" || loc.Project != "demo" {
		t.Fatalf("subdirectory must resolve to its worktree: %+v %v", loc, err)
	}
}

func TestWorkspaceWithoutRunGroupNeedsOne(t *testing.T) {
	f := newFixture(t, strings.Replace(appManifest, "[workspaces]\ndefault = { layout = \"code\", run = \"dev\" }", "", 1), "api/main", "web/main")
	_, err := f.mgr.Plan(context.Background(), f.loc("web/main"), PlanOptions{})
	if err == nil || !strings.Contains(err.Error(), "no [workspaces.default]") {
		t.Fatalf("got %v", err)
	}
}

// startSleeper starts a throwaway process so the test owns a pid.
func startSleeper(t *testing.T) *os.Process {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() }) //nolint:errcheck
	return cmd.Process
}

func TestReleaseAlsoCleansUpAfterDeadHolders(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main", "web/feature")
	ctx := context.Background()
	plan, err := f.mgr.Plan(ctx, f.loc("web/main"), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dead := startSleeper(t)
	dead.Kill()
	dead.Wait()
	res, err := f.mgr.Up(ctx, plan, UpOptions{Holder: "pane", HolderPID: dead.Pid}) // its process crashed
	if err != nil {
		t.Fatal(err)
	}
	api := byNode(res, "api.dev")
	// An unrelated workspace asking to stop sweeps up the orphans too (zellij killed the panes).
	if _, err := f.mgr.Release(ctx, "someone-else"); err != nil {
		t.Fatal(err)
	}
	if processAlive(api.PID, 0) {
		t.Fatal("api outlived the only (dead) holder")
	}
	// And starting a workspace never reuses an orphan.
	_, again := f.up("web/feature", PlanOptions{})
	if r := byNode(again, "api.dev"); r.Outcome != OutcomeReady || r.Shared {
		t.Fatalf("api.dev: %+v", r)
	}
}

func rowOf(t *testing.T, rows []Row, node string) Row {
	t.Helper()
	for _, r := range rows {
		if r.Node == node {
			return r
		}
	}
	t.Fatalf("no %s in %+v", node, rows)
	return Row{}
}

func TestKilledRequirementLeavesDependentBlocked(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	_, res := f.up("web/main", PlanOptions{})
	api, web := byNode(res, "api.dev"), byNode(res, "web.dev")
	ctx := context.Background()

	rows, err := f.mgr.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := rowOf(t, rows, "web.dev"); r.State != StateReady || r.BlockedBy != "" {
		t.Fatalf("healthy web must not be blocked: %+v", r)
	}

	got := make(chan Row, 4)
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	go f.mgr.WatchBlocked(wctx, []string{web.ID}, 20*time.Millisecond, func(r Row) { got <- r })

	stopGroup(api.PID, 0, time.Second) // api killed behind ADE's back; web keeps running
	select {
	case r := <-got:
		if r.Node != "web.dev" || r.BlockedBy != "api.dev" {
			t.Fatalf("event row %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no blocked notification after api was killed")
	}
	rows, err = f.mgr.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := rowOf(t, rows, "web.dev"); r.State != StateBlocked || r.BlockedBy != "api.dev" || !r.Alive {
		t.Fatalf("web must be running but blocked by api.dev: %+v", r)
	}
	if r := rowOf(t, rows, "api.dev"); r.State == StateBlocked || r.Alive {
		t.Fatalf("api is dead, not blocked: %+v", r)
	}
	select {
	case r := <-got:
		t.Fatalf("a blocked instance is reported once, got again: %+v", r)
	case <-time.After(150 * time.Millisecond):
	}
	e := BlockedEvent(rowOf(t, rows, "web.dev"))
	if e.Kind != KindTaskBlocked || !strings.Contains(e.Subject, "ade ws up") || e.Title != "web.dev is blocked by api.dev" {
		t.Fatalf("event %+v", e)
	}
}
