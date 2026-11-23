package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/startwork/swtest"
	"github.com/coredev-uk/ade/internal/workspace"
)

// pushEnv is Home with item 3 (repo api) and a feature worktree of api that
// has one commit the remote lacks.
func pushEnv(t *testing.T) (*swEnvT, string) {
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
	bare := filepath.Join(e.root, "demo-gl", "api", ".bare")
	tree := filepath.Join(e.root, "demo-gl", "api", "3-retry")
	swtest.Git(t, bare, "worktree", "add", "--quiet", "-b", "3-retry", tree, "origin/main")
	swtest.Git(t, tree, "commit", "--quiet", "--allow-empty", "-m", "Retry with backoff")
	old := wsItemWorktrees
	wsItemWorktrees = func(context.Context, string, *config.Manifest, domain.WorkItem) []workspace.ItemWorktree {
		return []workspace.ItemWorktree{{Repo: "api", Path: tree, Branch: "3-retry", Primary: true}}
	}
	t.Cleanup(func() { wsItemWorktrees = old })
	e.selectRow(trk(3).String())
	return e, tree
}

func (e *swEnvT) remoteHas(repo, branch string) bool {
	e.t.Helper()
	b := e.remote[repo].Bare
	return exec.Command("git", "-C", b, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

func TestHomePushListsTheCommitsThenPushesAndSetsTheUpstream(t *testing.T) {
	e, tree := pushEnv(t)
	e.press("u")
	if e.m.act == nil || e.m.act.stage != adConfirm {
		t.Fatalf("dialog = %+v", e.m.act)
	}
	e.want("Push your branch", "demo-gl/api/3-retry → origin/3-retry (new branch)", "1 commit ahead", "Retry with backoff", "enter push")
	if e.remoteHas("api", "3-retry") {
		t.Fatal("showing the plan pushed")
	}

	e.press("enter")
	if e.m.act.stage != adDone {
		t.Fatalf("stage = %v err %v", e.m.act.stage, e.m.act.err)
	}
	e.want("pushed demo-gl/api/3-retry to origin/3-retry")
	if !e.remoteHas("api", "3-retry") {
		t.Fatal("the branch is not on the remote")
	}
	if got := strings.TrimSpace(swtest.Git(t, tree, "rev-parse", "--abbrev-ref", "@{u}")); got != "origin/3-retry" {
		t.Errorf("upstream = %q", got)
	}
	es, err := e.app.Audit.Query(audit.Filter{Action: "git.push"})
	if err != nil || len(es) != 1 || es[0].Params["branch"] != "3-retry" || es[0].Outcome != "ok" {
		t.Fatalf("audit = %+v, %v", es, err)
	}
	e.press("esc")

	// Nothing ahead now: the dialog says so, and enter pushes nothing.
	e.press("u")
	e.want("everything is on origin/3-retry")
	e.wantNot("enter push")
	e.press("enter", "esc")
	if e.m.act != nil {
		t.Fatal("esc closes")
	}
	if es, _ := e.app.Audit.Query(audit.Filter{Action: "git.push"}); len(es) != 1 {
		t.Fatalf("a push with nothing ahead was audited: %+v", es)
	}
}

func TestHomePushEscapeWritesNothing(t *testing.T) {
	e, _ := pushEnv(t)
	e.press("u", "esc")
	if e.m.act != nil || e.remoteHas("api", "3-retry") {
		t.Fatalf("dialog %v, pushed %v", e.m.act, e.remoteHas("api", "3-retry"))
	}
}

func TestMRPagePushesTheShownMRsBranch(t *testing.T) {
	e, _ := pushEnv(t)
	mr := forge.MR{Ref: forge.Ref{Project: grp + "/api", IID: 9, Kind: forge.KindMR}, Title: "Retry failed webhook deliveries", SourceBranch: "3-retry", State: "opened"}
	e.m.mr = &mrPage{project: "demo-gl", mrs: []forge.MR{mr}, cur: 0}
	e.press("u")
	if e.m.act == nil || e.m.act.stage != adConfirm || len(e.m.act.plan.lines) == 0 || !strings.Contains(strings.Join(e.m.act.plan.lines, "\n"), "demo-gl/api/3-retry → origin/3-retry (new branch)") {
		t.Fatalf("dialog = %+v", e.m.act)
	}
	e.press("enter")
	if e.m.act.stage != adDone || !e.remoteHas("api", "3-retry") {
		t.Fatalf("stage = %v err %v, remote has branch: %v", e.m.act.stage, e.m.act.err, e.remoteHas("api", "3-retry"))
	}
}
