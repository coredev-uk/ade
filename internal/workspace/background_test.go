package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/readiness"
)

func servicesSpec() LayoutSpec {
	s := baseSpec(LayoutCode)
	s.BackgroundServices = true
	s.Services = []Pane{
		{Name: "services.db", Args: []string{"task", "run", "services.db", "--after-install", "--wait-requires"}},
		{Name: "api.dev", Args: []string{"task", "run", "api.dev", "--after-install", "--wait-requires"}},
	}
	s.Install = Pane{Name: "install", Args: []string{"ws", "install", "/projects/acme/web/418-paginate"}}
	return s
}

func TestBuildServicesLayoutRunsInstallThenEveryServiceUnsuspendedAndUnfocused(t *testing.T) {
	got, err := BuildServicesLayout(servicesSpec())
	if err != nil {
		t.Fatal(err)
	}
	want := `layout {
    default_tab_template {
        pane size=1 borderless=true {
            plugin location="zellij:tab-bar"
        }
        children
        pane size=2 borderless=true {
            plugin location="zellij:status-bar"
        }
    }
    tab name="web/418-paginate · services" cwd="/projects/acme/web/418-paginate" {
        pane name="install" size="25%" command="/opt/ade/bin/ade" {
            args "ws" "install" "/projects/acme/web/418-paginate"
        }
        pane split_direction="vertical" {
            pane name="services.db" command="/opt/ade/bin/ade" {
                args "task" "run" "services.db" "--after-install" "--wait-requires"
            }
            pane name="api.dev" command="/opt/ade/bin/ade" {
                args "task" "run" "api.dev" "--after-install" "--wait-requires"
            }
        }
    }
}
`
	if got != want {
		t.Fatalf("services tab mismatch\n--- got\n%s--- want\n%s", got, want)
	}
	if strings.Contains(got, "start_suspended") || strings.Contains(got, "focus=true") {
		t.Fatalf("no pane waits for Enter and the tab is not focused:\n%s", got)
	}
}

func TestBuildServicesLayoutWithOnlyInstallOrOneService(t *testing.T) {
	s := servicesSpec()
	s.Services = nil
	got, err := BuildServicesLayout(s)
	if err != nil || strings.Contains(got, `size="`) || strings.Contains(got, "split_direction") || !strings.Contains(got, `name="install"`) {
		t.Fatalf("install alone fills the tab: %v\n%s", err, got)
	}
	s = servicesSpec()
	s.Install, s.Services = Pane{}, s.Services[:1]
	got, err = BuildServicesLayout(s)
	if err != nil || strings.Contains(got, `name="install"`) || strings.Contains(got, "split_direction") || !strings.Contains(got, `name="services.db"`) {
		t.Fatalf("one service, no install: %v\n%s", err, got)
	}
}

func TestBuildServicesLayoutNeedsAServicesTab(t *testing.T) {
	s := servicesSpec()
	s.BackgroundServices = false
	if _, err := BuildServicesLayout(s); err == nil {
		t.Fatal("manual services have no services tab")
	}
	s = servicesSpec()
	s.Install, s.Services = Pane{}, nil
	if s.HasServicesTab() {
		t.Fatal("nothing to install or start: no services tab")
	}
}

func TestBuildLayoutWithServicesTabKeepsOnlyTasksInTheStrip(t *testing.T) {
	s := baseSpec(LayoutCode)
	s.BackgroundServices = true
	got, err := BuildLayout(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, `"api.dev"`) || strings.Contains(got, `"ws" "up"`) || !strings.Contains(got, `name="web.dev" start_suspended=true`) {
		t.Fatalf("strip:\n%s", got)
	}
	// Without services or install the Up pane stays: there is no services tab to hold it.
	s.Services, s.Tasks = nil, nil
	s.Up = Pane{}
	got, _ = BuildLayout(s)
	if strings.Contains(got, "30%") {
		t.Fatalf("no strip without panes:\n%s", got)
	}
}

func TestOpenTabCreatesTheServicesTabFirstSoTheMainTabEndsFocused(t *testing.T) {
	z := &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "home\n"}
	bg := tabLayout{name: "web/418 · services", file: "/tmp/s.kdl"}
	created, bgErr, err := openTab(context.Background(), z.run, "ade", "web/418", "/tmp/x.kdl", bg)
	if err != nil || bgErr != nil || !created {
		t.Fatalf("created=%v bgErr=%v err=%v", created, bgErr, err)
	}
	var news []string
	for _, c := range z.calls {
		if len(c) > 3 && c[3] == "new-tab" {
			news = append(news, strings.Join(c[4:], " "))
		}
	}
	want := []string{"--layout /tmp/s.kdl --name web/418 · services", "--layout /tmp/x.kdl --name web/418"}
	if strings.Join(news, "|") != strings.Join(want, "|") {
		t.Fatalf("new-tab calls: %v", news)
	}
}

func TestOpenTabLeavesAnExistingTabAndAnExistingServicesTabAlone(t *testing.T) {
	bg := tabLayout{name: "web/418 · services", file: "/tmp/s.kdl"}
	z := &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "home\nweb/418\n"}
	created, _, err := openTab(context.Background(), z.run, "ade", "web/418", "/tmp/x.kdl", bg)
	if err != nil || created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	for _, c := range z.calls {
		if len(c) > 3 && c[3] == "new-tab" {
			t.Fatalf("an existing workspace tab is only focused: %v", c)
		}
	}
	z = &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "home\nweb/418 · services\n"}
	created, _, err = openTab(context.Background(), z.run, "ade", "web/418", "/tmp/x.kdl", bg)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	news := 0
	for _, c := range z.calls {
		if len(c) > 3 && c[3] == "new-tab" {
			news++
		}
	}
	if news != 1 {
		t.Fatalf("the services tab exists already, only the main tab is new: %v", z.calls)
	}
}

const gateManifest = `
name = "demo"
[repos.api]
path = "demo/api"
default_branch = "main"
[repos.api.tasks.dev]
cmd  = "true"
port = "auto"
[repos.api.tasks.explicit]
cmd   = "true"
port  = "auto"
ready = { log = "up" }
[repos.api.tasks.plain]
cmd = "true"
[repos.web]
path = "demo/web"
default_branch = "main"
[repos.web.tasks.dev]
cmd      = "true"
port     = "auto"
requires = ["api.dev", "api.explicit", "api.plain"]
[groups]
dev = ["web.dev"]
[workspaces]
default = { run = "dev" }
`

func TestPlanGatesRequiredPortsOnTCPUnlessReadyIsExplicit(t *testing.T) {
	f := newFixture(t, gateManifest, "api/main", "web/main")
	plan, err := f.mgr.Plan(context.Background(), f.loc("web/main"), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	api, _ := plan.Instance("api.dev")
	if api.Ready == nil || api.Ready.Kind != readiness.TCP || api.Ready.Target != "localhost:"+strconv.Itoa(api.Port) || api.Port == 0 {
		t.Fatalf("a required service with a port is gated on it: %+v", api.Ready)
	}
	if ex, _ := plan.Instance("api.explicit"); ex.Ready == nil || ex.Ready.Kind != readiness.Log {
		t.Fatalf("an explicit ready wins: %+v", ex.Ready)
	}
	if pl, _ := plan.Instance("api.plain"); pl.Ready != nil {
		t.Fatalf("no port, no gate: %+v", pl.Ready)
	}
	if web, _ := plan.Instance("web.dev"); web.Ready != nil {
		t.Fatalf("nothing requires web.dev, nothing to gate: %+v", web.Ready)
	}
}

func TestUpWaitingStartsOnlyTheNodeAfterItsRequirementIsReady(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	ctx := context.Background()
	webTree := filepath.Join(f.root, "demo", "web", "main")
	plan, err := f.mgr.Plan(ctx, f.loc("web/main"), PlanOptions{Nodes: []string{"web.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	var waited atomic.Value
	done := make(chan []Result, 1)
	go func() {
		res, err := f.mgr.UpWaiting(ctx, plan, UpOptions{Holder: webTree + "#task:web.dev"}, WaitOptions{
			Poll: 20 * time.Millisecond, Waiting: func(n string) { waited.Store(n) },
		})
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	select {
	case <-done:
		t.Fatal("web.dev started although api.dev is not running")
	case <-time.After(300 * time.Millisecond):
	}
	if waited.Load() != "api.dev" {
		t.Fatalf("waiting reported for %v", waited.Load())
	}
	if rows, _ := f.mgr.Status(ctx); len(rows) != 0 {
		t.Fatalf("UpWaiting must not start the requirement: %+v", rows)
	}
	// api.dev is started by its own pane (here: a plain Up of it).
	apiPlan, err := f.mgr.Plan(ctx, f.loc("web/main"), PlanOptions{Nodes: []string{"api.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.mgr.Up(ctx, apiPlan, UpOptions{Holder: webTree + "#task:api.dev"}); err != nil {
		t.Fatal(err)
	}
	var res []Result
	select {
	case res = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("web.dev never started")
	}
	api, web := byNode(res, "api.dev"), byNode(res, "web.dev")
	if !api.Shared || api.Outcome != OutcomeReady || web.Outcome != OutcomeReady || web.Shared {
		t.Fatalf("api=%+v web=%+v", api, web)
	}
}

func installFixture(t *testing.T, commands string) (*fixture, InstallTarget, string) {
	t.Helper()
	m := strings.Replace(appManifest, "[repos.web.tasks.dev]", "[repos.web.hooks]\ninstall = "+commands+"\n[repos.web.tasks.dev]", 1)
	f := newFixture(t, m, "api/main", "web/main")
	loc := f.loc("web/main")
	plan, err := f.mgr.Plan(context.Background(), loc, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	targets := f.mgr.InstallTargets(loc, plan, true)
	if len(targets) != 1 || targets[0].Repo != "web" || targets[0].Tree != loc.Tree {
		t.Fatalf("targets: %+v", targets)
	}
	return f, targets[0], loc.Tree
}

func TestInstallRunsOnceAndAgainOnlyWhenForcedOrTheCommandsChange(t *testing.T) {
	f, target, tree := installFixture(t, `["echo run >> count"]`)
	ctx := context.Background()
	if f.mgr.InstallDone(target) {
		t.Fatal("nothing installed yet")
	}
	var audited []string
	opts := InstallOptions{Audit: func(_ InstallTarget, c string, err error) {
		if err != nil {
			t.Errorf("audit %q: %v", c, err)
		}
		audited = append(audited, c)
	}}
	if r := f.mgr.Install(ctx, target, opts); r.Outcome != InstallRan {
		t.Fatalf("%+v", r)
	}
	if r := f.mgr.Install(ctx, target, opts); r.Outcome != InstallUpToDate {
		t.Fatalf("second run: %+v", r)
	}
	opts.Force = true
	if r := f.mgr.Install(ctx, target, opts); r.Outcome != InstallRan {
		t.Fatalf("forced: %+v", r)
	}
	if got := readFile(t, filepath.Join(tree, "count")); got != "run\nrun\n" {
		t.Fatalf("ran %q", got)
	}
	if len(audited) != 2 {
		t.Fatalf("audited %v", audited)
	}
	changed := target
	changed.Commands = []string{"echo other >> count"}
	if f.mgr.InstallDone(changed) {
		t.Fatal("new commands need a new install")
	}
	if r := f.mgr.Install(ctx, changed, InstallOptions{}); r.Outcome != InstallRan {
		t.Fatalf("changed: %+v", r)
	}
}

func TestInstallFailureStopsTheRunAndLeavesNoMarker(t *testing.T) {
	f, target, tree := installFixture(t, `["echo first >> count", "exit 3", "echo never >> count"]`)
	var out strings.Builder
	r := f.mgr.Install(context.Background(), target, InstallOptions{Out: &out})
	if r.Outcome != InstallFailed || !strings.Contains(r.Detail, "exit 3") {
		t.Fatalf("%+v", r)
	}
	if f.mgr.InstallDone(target) {
		t.Fatal("a failed install must not be remembered")
	}
	if got := readFile(t, filepath.Join(tree, "count")); got != "first\n" {
		t.Fatalf("commands after the failure ran: %q", got)
	}
	if !strings.Contains(out.String(), "$ exit 3") {
		t.Fatalf("output: %q", out.String())
	}
}

func TestInstallOfAFailedMarkerIsClearedByARetry(t *testing.T) {
	f, target, tree := installFixture(t, `["test -f ok"]`)
	ctx := context.Background()
	if r := f.mgr.Install(ctx, target, InstallOptions{}); r.Outcome != InstallFailed {
		t.Fatalf("%+v", r)
	}
	if err := os.WriteFile(filepath.Join(tree, "ok"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := f.mgr.Install(ctx, target, InstallOptions{}); r.Outcome != InstallRan || !f.mgr.InstallDone(target) {
		t.Fatalf("%+v", r)
	}
}

func TestInstallTargetsSkipReposWithoutHooks(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	loc := f.loc("web/main")
	plan, err := f.mgr.Plan(context.Background(), loc, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.mgr.InstallTargets(loc, plan, true); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if !f.mgr.InstallDone(InstallTarget{}) {
		t.Fatal("no commands: nothing to wait for")
	}
}

func TestWaitInstalledReturnsWhenTheMarkerAppears(t *testing.T) {
	f, target, _ := installFixture(t, `["true"]`)
	ctx := context.Background()
	var announced atomic.Int32
	errc := make(chan error, 1)
	go func() {
		errc <- f.mgr.WaitInstalled(ctx, []InstallTarget{target}, 10*time.Millisecond, func(p []InstallTarget) {
			if len(p) == 1 {
				announced.Add(1)
			}
		})
	}()
	select {
	case err := <-errc:
		t.Fatalf("returned before the install: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if r := f.mgr.Install(ctx, target, InstallOptions{}); r.Outcome != InstallRan {
		t.Fatalf("%+v", r)
	}
	if err := <-errc; err != nil || announced.Load() != 1 {
		t.Fatalf("err=%v announced=%d", err, announced.Load())
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	other := target
	other.Commands = []string{"different"}
	if err := f.mgr.WaitInstalled(cctx, []InstallTarget{other}, time.Millisecond, nil); err == nil {
		t.Fatal("a cancelled wait must return its error")
	}
}
