package workspace

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRestartBringsBackTheBlockedSubtree(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	plan, res := f.up("web/main", PlanOptions{})
	oldAPI, oldWeb := byNode(res, "api.dev"), byNode(res, "web.dev")
	holder := filepath.Join(f.root, "demo", "web", "main")
	ctx := context.Background()

	stopGroup(oldAPI.PID, 0, 0) // api dies behind ADE's back: web is blocked by it
	rows, err := f.mgr.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blocked := rowOf(t, rows, "web.dev")
	if blocked.State != StateBlocked || blocked.RestartNode() != "api.dev" {
		t.Fatalf("setup: %+v", blocked)
	}

	got, err := f.mgr.Restart(ctx, plan, blocked.RestartNode(), UpOptions{Holder: holder})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Node != "api.dev" || got[1].Node != "web.dev" {
		t.Fatalf("restarted %+v, want api.dev then web.dev (the oneshot is not part of the subtree)", got)
	}
	for _, r := range got {
		if r.Outcome != OutcomeReady || r.Shared {
			t.Fatalf("%s: %s %s shared=%v", r.Node, r.Outcome, r.Detail, r.Shared)
		}
	}
	api, web := got[0], got[1]
	if api.PID == oldAPI.PID || web.PID == oldWeb.PID {
		t.Fatalf("pids unchanged: api %d->%d web %d->%d", oldAPI.PID, api.PID, oldWeb.PID, web.PID)
	}
	if processAlive(oldWeb.PID, 0) {
		t.Fatal("the old dependent is still running next to its replacement")
	}
	if ok, _ := httpOK("http://127.0.0.1:" + itoa(api.Port) + "/healthz"); !ok {
		t.Fatal("restarted api is not serving")
	}
	// The fresh web was started against the fresh api, on the same port as before.
	if api.Port != oldAPI.Port || web.Port != oldWeb.Port {
		t.Fatalf("ports moved: api %d->%d web %d->%d", oldAPI.Port, api.Port, oldWeb.Port, web.Port)
	}
	if !strings.Contains(readFile(t, web.Log), "api=http://127.0.0.1:"+itoa(api.Port)) {
		t.Fatalf("web log: %s", readFile(t, web.Log))
	}

	rows, err = f.mgr.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"api.dev", "web.dev"} {
		r := rowOf(t, rows, node)
		if r.State != StateReady || r.BlockedBy != "" || !r.Alive || len(r.Holders) != 1 || r.Holders[0] != holder {
			t.Fatalf("%s after restart: %+v", node, r)
		}
	}

	// Releasing the one holder still stops everything: no hold leaked or lost.
	stopped, err := f.mgr.Release(ctx, holder)
	if err != nil || len(stopped) != 3 {
		t.Fatalf("release: %+v %v", stopped, err)
	}
}

func TestRestartRowFindsThePlanFromTheHolder(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	_, res := f.up("web/main", PlanOptions{})
	ctx := context.Background()
	stopGroup(byNode(res, "api.dev").PID, 0, 0)
	rows, _ := f.mgr.Status(ctx)

	got, err := f.mgr.RestartRow(ctx, f.root, rowOf(t, rows, "web.dev"), UpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Outcome != OutcomeReady || got[1].Outcome != OutcomeReady {
		t.Fatalf("%+v", got)
	}
}

func TestRestartLeavesDeadRequirementOutsideTheSubtreeBlocking(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	plan, res := f.up("web/main", PlanOptions{})
	ctx := context.Background()
	stopGroup(byNode(res, "api.dev").PID, 0, 0)

	// Restarting only web while api is dead must not pretend api is fine.
	got, err := f.mgr.Restart(ctx, plan, "web.dev", UpOptions{Holder: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Outcome != OutcomeBlocked || !strings.Contains(got[0].Detail, "api.dev") {
		t.Fatalf("%+v", got)
	}
}

func TestRestartUnknownNodeListsTheNodes(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	plan, _ := f.up("web/main", PlanOptions{})
	_, err := f.mgr.Restart(context.Background(), plan, "nope.dev", UpOptions{Holder: "h"})
	if err == nil || !strings.Contains(err.Error(), "api.dev") {
		t.Fatalf("err = %v", err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
