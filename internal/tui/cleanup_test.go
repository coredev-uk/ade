package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/startwork/swtest"
	"github.com/coredev-uk/ade/internal/workspace"
)

const (
	webMergedBranch = "2-fix-truncated-column-headers-in-export-preview" // web!2, merged in the fake
	apiOpenBranch   = "1-rate-limit-the-export-endpoint"                 // api!1, open in the fake
)

// cleanupEnv is Home with tracker#2 (repo web) whose MR web!2 is merged and
// whose branch sits on origin/main as the merge left it, and a worktree of
// api on the branch of the still open api!1.
func cleanupEnv(t *testing.T) (*swEnvT, string, string) {
	t.Helper()
	e := newSWEnv(t)
	e.m.svc = project.New(e.app, "user")
	src, err := os.ReadFile("../../scripts/sandbox/manifests/gitlab.ade.toml")
	if err != nil {
		t.Fatal(err)
	}
	snap := "source = \"x\"\nmanifest = '''\n" + string(src) + "'''\n"
	if err := os.WriteFile(filepath.Join(e.root, "demo-gl", ".project.toml"), []byte(snap), 0o600); err != nil {
		t.Fatal(err)
	}
	web := filepath.Join(e.root, "demo-gl", "web", webMergedBranch)
	swtest.Git(t, filepath.Join(e.root, "demo-gl", "web", ".bare"), "worktree", "add", "--quiet", "-b", webMergedBranch, web, "origin/main")
	// api!1's branch exists on the remote with a commit, and is checked out.
	api := filepath.Join(e.root, "demo-gl", "api", apiOpenBranch)
	r := e.remote["api"]
	work := filepath.Join(t.TempDir(), "w")
	swtest.Git(t, filepath.Dir(work), "clone", "--quiet", r.Bare, work)
	swtest.Git(t, work, "checkout", "--quiet", "-b", apiOpenBranch)
	swtest.Git(t, work, "commit", "--quiet", "--allow-empty", "-m", "change")
	swtest.Git(t, work, "push", "--quiet", "origin", apiOpenBranch)
	apiBare := filepath.Join(e.root, "demo-gl", "api", ".bare")
	swtest.Git(t, apiBare, "fetch", "--quiet", "origin")
	swtest.Git(t, apiBare, "worktree", "add", "--quiet", "--track", "-b", apiOpenBranch, api, "origin/"+apiOpenBranch)

	trees := map[string]string{"2": web, "1": api}
	old := wsItemWorktrees
	wsItemWorktrees = func(_ context.Context, _ string, _ *config.Manifest, wi domain.WorkItem) []workspace.ItemWorktree {
		switch wi.Ref.IID {
		case 2:
			return []workspace.ItemWorktree{{Repo: "web", Path: trees["2"], Branch: webMergedBranch, Primary: true}}
		case 1:
			return []workspace.ItemWorktree{{Repo: "api", Path: trees["1"], Branch: apiOpenBranch, Primary: true}}
		}
		return nil
	}
	t.Cleanup(func() { wsItemWorktrees = old })
	e.m.results[0].Items = append(e.m.results[0].Items, e.item(2, "Fix truncated headers", "web"), e.item(1, "Rate-limit exports", "api"))
	e.m.rebuildHome("")
	return e, web, api
}

func TestHomeCleanupListsThenRemovesMergedWorkAndItsBranch(t *testing.T) {
	e, web, _ := cleanupEnv(t)
	e.selectRow(trk(2).String())
	e.press("x")
	if e.m.act == nil || e.m.act.stage != adConfirm {
		t.Fatalf("dialog = %+v err %v", e.m.act, e.m.act)
	}
	e.want("Clean up · "+trk(2).String(), "remove  demo-gl/web/"+webMergedBranch, "web!2 merged", "branch "+webMergedBranch, "enter clean up")
	if _, err := os.Stat(web); err != nil {
		t.Fatalf("showing the plan removed the worktree: %v", err)
	}

	e.press("enter")
	if e.m.act.stage != adDone {
		t.Fatalf("stage = %v err %v", e.m.act.stage, e.m.act.err)
	}
	e.want("removed demo-gl/web/" + webMergedBranch + ", deleted branch")
	if _, err := os.Stat(web); !os.IsNotExist(err) {
		t.Fatalf("the worktree is still there: %v", err)
	}
	for _, action := range []string{"worktree.remove", "git.branch.delete"} {
		if es, err := e.app.Audit.Query(audit.Filter{Action: action}); err != nil || len(es) != 1 {
			t.Errorf("%s audit = %+v, %v", action, es, err)
		}
	}
}

func TestHomeCleanupKeepsWorkWithAnOpenMRAndPaletteGCSaysTheSame(t *testing.T) {
	e, _, api := cleanupEnv(t)
	e.selectRow(trk(1).String())
	e.press("x")
	e.want("keep    demo-gl/api/"+apiOpenBranch, "still open", "Nothing can be removed yet.")
	e.wantNot("enter clean up")
	e.press("enter", "esc")
	if e.m.act != nil {
		t.Fatal("esc closes")
	}
	if _, err := os.Stat(api); err != nil {
		t.Fatalf("a kept worktree is gone: %v", err)
	}

	// The palette's gc entry judges every worktree: web!2's goes, api!1's stays.
	var gc *palEntry
	for _, pe := range e.m.paletteEntries() {
		if pe.name == "Clean up merged work everywhere" {
			gc = &pe
		}
	}
	if gc == nil || gc.cli != "ade gc" {
		t.Fatal("the palette has no entry for ade gc")
	}
	next, cmd := gc.run(e.m)
	e.m = next
	e.settle(cmd)
	if e.m.act == nil || e.m.act.stage != adConfirm {
		t.Fatalf("dialog = %+v", e.m.act)
	}
	out := strings.Join(e.m.act.plan.lines, "\n")
	if !strings.Contains(out, "remove  demo-gl/web/"+webMergedBranch) || !strings.Contains(out, "keep    demo-gl/api/"+apiOpenBranch) {
		t.Fatalf("plan:\n%s", out)
	}
	e.press("enter")
	if e.m.act.stage != adDone {
		t.Fatalf("stage = %v err %v", e.m.act.stage, e.m.act.err)
	}
	if _, err := os.Stat(api); err != nil {
		t.Fatalf("gc removed the open MR's worktree: %v", err)
	}
}
