package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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
			Poll: 20 * time.Millisecond, Waiting: func(n, _ string, _ time.Duration) { waited.Store(n) },
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
		errc <- f.mgr.WaitInstalled(ctx, []InstallTarget{target}, InstallWait{Poll: 10 * time.Millisecond, Waiting: func(p []InstallTarget, elapsed time.Duration) {
			if len(p) == 1 && elapsed == 0 {
				announced.Add(1)
			}
		}})
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
	if err := f.mgr.WaitInstalled(cctx, []InstallTarget{other}, InstallWait{Poll: time.Millisecond}); err == nil {
		t.Fatal("a cancelled wait must return its error")
	}
}

func TestPlanOfARequiredServiceAloneAppliesTheSamePortGate(t *testing.T) {
	f := newFixture(t, gateManifest, "api/main", "web/main")
	plan, err := f.mgr.Plan(context.Background(), f.loc("web/main"), PlanOptions{Nodes: []string{"api.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	api, _ := plan.Instance("api.dev")
	if api.Ready == nil || api.Ready.Kind != readiness.TCP || api.Ready.Target != "localhost:"+strconv.Itoa(api.Port) {
		t.Fatalf("the service's own pane must wait for its port like its dependent's plan: %+v", api.Ready)
	}
}

func TestUpWaitingReportsPeriodicallyAndGivesUpAtTheTimeout(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	plan, err := f.mgr.Plan(context.Background(), f.loc("web/main"), PlanOptions{Nodes: []string{"web.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	var elapsed []time.Duration
	var probe string
	_, err = f.mgr.UpWaiting(context.Background(), plan, UpOptions{Holder: "h"}, WaitOptions{
		Poll: 10 * time.Millisecond, Every: 40 * time.Millisecond, Timeout: 250 * time.Millisecond,
		Waiting: func(_, p string, e time.Duration) { probe = p; elapsed = append(elapsed, e) },
	})
	if err == nil || !strings.Contains(err.Error(), "api.dev was not ready (http http://127.0.0.1:") || !strings.Contains(err.Error(), "250ms") {
		t.Fatalf("err = %v", err)
	}
	if len(elapsed) < 3 || elapsed[0] != 0 || elapsed[1] <= 0 || elapsed[2] <= elapsed[1] || !strings.HasPrefix(probe, "http ") {
		t.Fatalf("progress lines: %v probe %q", elapsed, probe)
	}
}

const crashManifest = `
name = "demo"
[repos.api]
path = "demo/api"
default_branch = "main"
[repos.api.tasks.dev]
cmd  = "WS_HELPER=fail {{HELPER}}"
port = "auto"
[repos.web]
path = "demo/web"
default_branch = "main"
[repos.web.tasks.dev]
cmd      = "true"
requires = ["api.dev"]
[groups]
dev = ["web.dev"]
[workspaces]
default = { run = "dev" }
`

func TestUpWaitingFailsWithTheReasonWhenTheRequiredPaneFailed(t *testing.T) {
	f := newFixture(t, crashManifest, "api/main", "web/main")
	ctx := context.Background()
	webTree := filepath.Join(f.root, "demo", "web", "main")
	webPlan, err := f.mgr.Plan(ctx, f.loc("web/main"), PlanOptions{Nodes: []string{"web.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() {
		_, err := f.mgr.UpWaiting(ctx, webPlan, UpOptions{Holder: webTree + "#task:web.dev"}, WaitOptions{Poll: 10 * time.Millisecond})
		errc <- err
	}()
	select {
	case err := <-errc:
		t.Fatalf("returned before api.dev ran: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	// api.dev's own pane: crashes before its port (the implicit gate) opens.
	apiPlan, err := f.mgr.Plan(ctx, f.loc("web/main"), PlanOptions{Nodes: []string{"api.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.mgr.Up(ctx, apiPlan, UpOptions{Holder: webTree + "#task:api.dev"})
	if err != nil || res[0].Outcome != OutcomeFailed {
		t.Fatalf("api.dev: %+v %v", res, err)
	}
	select {
	case err = <-errc:
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting pane never gave up")
	}
	if err == nil || !strings.Contains(err.Error(), "api.dev failed to come up") || !strings.Contains(err.Error(), "boom: cannot start") {
		t.Fatalf("err = %v", err)
	}
	// The failure travels on: web.dev's pane records its own, so a pane waiting only for it fails too.
	web, _ := webPlan.Instance("web.dev")
	if reason, ok := f.mgr.failureSince(web.ID, time.Time{}); !ok || !strings.Contains(reason, "api.dev failed to come up") {
		t.Fatalf("web.dev's recorded failure: %q %v", reason, ok)
	}
}

func TestUpWaitingIgnoresAFailureOfAnEarlierRunAndStartingAgainClearsIt(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	ctx := context.Background()
	plan, err := f.mgr.Plan(ctx, f.loc("web/main"), PlanOptions{Nodes: []string{"web.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	api, _ := plan.Instance("api.dev")
	f.mgr.recordFailure(api.ID, "yesterday")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(f.mgr.failurePath(api.ID), old, old); err != nil {
		t.Fatal(err)
	}
	_, err = f.mgr.UpWaiting(ctx, plan, UpOptions{Holder: "h"}, WaitOptions{Poll: 10 * time.Millisecond, Timeout: 100 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "was not ready") {
		t.Fatalf("a stale failure must not decide: %v", err)
	}
	f.mgr.recordFailure(api.ID, "recent")
	apiPlan, err := f.mgr.Plan(ctx, f.loc("web/main"), PlanOptions{Nodes: []string{"api.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.mgr.Up(ctx, apiPlan, UpOptions{Holder: "api"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.mgr.failureSince(api.ID, time.Time{}); ok {
		t.Fatal("starting an instance clears its recorded failure")
	}
}

func TestUpWaitingRunsAOneshotRequirementItselfAndWaitsOnlyForServices(t *testing.T) {
	m := strings.Replace(appManifest, `requires = ["api.dev"]
env      = { API_URL`, `requires = ["api.build", "api.dev"]
env      = { API_URL`, 1)
	f := newFixture(t, m, "api/main", "web/main")
	ctx := context.Background()
	webTree := filepath.Join(f.root, "demo", "web", "main")
	plan, err := f.mgr.Plan(ctx, f.loc("web/main"), PlanOptions{Nodes: []string{"web.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	var waiting sync.Map
	done := make(chan []Result, 1)
	go func() {
		res, err := f.mgr.UpWaiting(ctx, plan, UpOptions{Holder: webTree + "#task:web.dev"}, WaitOptions{
			Poll: 20 * time.Millisecond, Waiting: func(n, _ string, _ time.Duration) { waiting.Store(n, true) },
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
	if _, ok := waiting.Load("api.build"); ok {
		t.Fatal("a oneshot is run, not waited for")
	}
	if _, ok := waiting.Load("api.dev"); !ok {
		t.Fatal("api.dev is waited for")
	}
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
	if b := byNode(res, "api.build"); b.Outcome != OutcomeReady || byNode(res, "web.dev").Outcome != OutcomeReady {
		t.Fatalf("results: %+v", res)
	}
}

func TestConcurrentInstallsOfOneTreeRunTheCommandsOnce(t *testing.T) {
	f, target, tree := installFixture(t, `["echo run >> count; sleep 0.3"]`)
	ctx := context.Background()
	outcomes := make(chan InstallOutcome, 2)
	for range 2 {
		go func() { outcomes <- f.mgr.Install(ctx, target, InstallOptions{}).Outcome }()
	}
	got := []InstallOutcome{<-outcomes, <-outcomes}
	slices.Sort(got)
	if got[0] != InstallRan || got[1] != InstallUpToDate {
		t.Fatalf("outcomes %v: the second runner re-checks the marker under the lock", got)
	}
	if c := readFile(t, filepath.Join(tree, "count")); c != "run\n" {
		t.Fatalf("ran %q", c)
	}
}

func TestInstallFailureIsReportedAtOnceToWaitersAndClearedBySuccess(t *testing.T) {
	f, target, tree := installFixture(t, `["test -f ok"]`)
	ctx := context.Background()
	if r := f.mgr.Install(ctx, target, InstallOptions{}); r.Outcome != InstallFailed {
		t.Fatalf("%+v", r)
	}
	start := time.Now()
	err := f.mgr.WaitInstalled(ctx, []InstallTarget{target}, InstallWait{Poll: time.Hour})
	if err == nil || !strings.Contains(err.Error(), "install of web") || !strings.Contains(err.Error(), "failed") || !strings.Contains(err.Error(), "test -f ok") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("a recorded failure must end the wait immediately")
	}
	// A failure of other commands is not this target's.
	other := target
	other.Commands = []string{"different"}
	if _, ok := f.mgr.installFailure(other); ok {
		t.Fatal("the failure belongs to the commands that failed")
	}
	if err := os.WriteFile(filepath.Join(tree, "ok"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := f.mgr.Install(ctx, target, InstallOptions{}); r.Outcome != InstallRan {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(f.mgr.installFailedMarker(target)); !os.IsNotExist(err) {
		t.Fatalf("the failure marker survived a success: %v", err)
	}
	if err := f.mgr.WaitInstalled(ctx, []InstallTarget{target}, InstallWait{Poll: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
}

func TestWaitInstalledReportsPeriodicallyAndTimesOut(t *testing.T) {
	f, target, _ := installFixture(t, `["true"]`)
	var elapsed []time.Duration
	err := f.mgr.WaitInstalled(context.Background(), []InstallTarget{target}, InstallWait{
		Poll: 10 * time.Millisecond, Every: 40 * time.Millisecond, Timeout: 250 * time.Millisecond,
		Waiting: func(_ []InstallTarget, e time.Duration) { elapsed = append(elapsed, e) },
	})
	if err == nil || !strings.Contains(err.Error(), "did not finish within 250ms") {
		t.Fatalf("err = %v", err)
	}
	if len(elapsed) < 3 || elapsed[0] != 0 || elapsed[1] <= 0 || elapsed[2] <= elapsed[1] {
		t.Fatalf("progress lines: %v", elapsed)
	}
}

func TestInstallKeyFollowsTheLockfiles(t *testing.T) {
	f, target, tree := installFixture(t, `["true"]`)
	ctx := context.Background()
	if r := f.mgr.Install(ctx, target, InstallOptions{}); r.Outcome != InstallRan {
		t.Fatalf("%+v", r)
	}
	lock := filepath.Join(tree, "package-lock.json")
	if err := os.WriteFile(lock, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f.mgr.InstallDone(target) {
		t.Fatal("a new lockfile needs a new install")
	}
	if r := f.mgr.Install(ctx, target, InstallOptions{}); r.Outcome != InstallRan {
		t.Fatalf("%+v", r)
	}
	if err := os.WriteFile(lock, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f.mgr.InstallDone(target) {
		t.Fatal("a changed lockfile needs a new install")
	}
	if r := f.mgr.Install(ctx, target, InstallOptions{}); r.Outcome != InstallRan {
		t.Fatalf("%+v", r)
	}
	if !f.mgr.InstallDone(target) {
		t.Fatal("unchanged lockfile, unchanged key")
	}
}

func TestInstallTargetsPutTheOwnTreeLast(t *testing.T) {
	m := strings.Replace(appManifest, "[repos.web.tasks.dev]", "[repos.web.hooks]\ninstall = [\"true\"]\n[repos.web.tasks.dev]", 1)
	m = strings.Replace(m, "[repos.api.tasks.build]", "[repos.api.hooks]\ninstall = [\"true\"]\n[repos.api.tasks.build]", 1)
	f := newFixture(t, m, "api/main", "web/main")
	loc := f.loc("web/main")
	plan, err := f.mgr.Plan(context.Background(), loc, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := f.mgr.InstallTargets(loc, plan, true)
	if len(got) != 2 || got[0].Repo != "api" || got[1].Repo != "web" || got[1].Tree != loc.Tree {
		t.Fatalf("requirements first, the workspace's own tree last: %+v", got)
	}
	if got = f.mgr.InstallTargets(loc, plan, false); len(got) != 2 {
		t.Fatalf("a task pane's targets: %+v", got)
	}
}

func TestOpenTabKeepsTheMainTabWhenTheServicesTabFails(t *testing.T) {
	z := &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "home\n", failOn: "--layout /tmp/s.kdl"}
	bg := tabLayout{name: "web/418 · services", file: "/tmp/s.kdl"}
	created, bgErr, err := openTab(context.Background(), z.run, "ade", "web/418", "/tmp/x.kdl", bg)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if bgErr == nil || !strings.Contains(bgErr.Error(), "/tmp/s.kdl") {
		t.Fatalf("the services tab failure is reported as a warning: %v", bgErr)
	}
	if last := z.calls[len(z.calls)-1]; strings.Join(last, " ") != "--session ade action new-tab --layout /tmp/x.kdl --name web/418" {
		t.Fatalf("the main tab is still opened: %v", last)
	}
}

const (
	deadHomePane  = `[{"is_plugin":true,"exited":false,"tab_id":0,"tab_name":"home"},{"is_plugin":false,"exited":true,"tab_id":7,"tab_name":"home"},{"is_plugin":false,"exited":true,"tab_id":8,"tab_name":"web/418"}]`
	aliveHomePane = `[{"is_plugin":false,"exited":false,"tab_id":7,"tab_name":"home"},{"is_plugin":false,"exited":true,"tab_id":8,"tab_name":"web/418"}]`
)

func actions(z *fakeZellij) []string {
	var out []string
	for _, c := range z.calls {
		if len(c) > 3 && c[3] != "query-tab-names" && c[3] != "list-panes" {
			out = append(out, strings.Join(c[3:], " "))
		}
	}
	return out
}

func TestFocusTabReplacesATabWhoseCommandHasExited(t *testing.T) {
	z := &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "home\nweb/418\n", panes: deadHomePane}
	created, err := focusTab(context.Background(), z.run, "ade", "home", "/tmp/home.kdl")
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	want := "new-tab --layout /tmp/home.kdl --name home|close-tab-by-id 7"
	if got := strings.Join(actions(z), "|"); got != want {
		t.Fatalf("actions %q, want %q: the new tab first, then the dead one closed by id", got, want)
	}
}

func TestFocusTabJustFocusesALiveOrUnlistedTab(t *testing.T) {
	for name, panes := range map[string]string{"alive": aliveHomePane, "no json": "not json", "none": ""} {
		z := &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "home\nweb/418\n", panes: panes}
		created, err := focusTab(context.Background(), z.run, "ade", "home", "/tmp/home.kdl")
		if err != nil || created {
			t.Fatalf("%s: created=%v err=%v", name, created, err)
		}
		if got := strings.Join(actions(z), "|"); got != "go-to-tab-name home" {
			t.Fatalf("%s: actions %q", name, got)
		}
	}
	z := &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "web/418\n"}
	if created, err := focusTab(context.Background(), z.run, "ade", "home", "/tmp/home.kdl"); err != nil || !created {
		t.Fatalf("a missing tab is created: %v %v", created, err)
	}
	z = &fakeZellij{}
	if _, err := focusTab(context.Background(), z.run, "ade", "home", "/tmp/home.kdl"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("err = %v", err)
	}
}
