package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/workspace"
)

func (e *cliEnv) push(args ...string) (string, error) { return e.run(newPushCmd, args...) }

func TestPushShowsTheCommitsThenPushesAndSetsTheUpstream(t *testing.T) {
	e := newCLIEnv(t)
	api, web := e.gcLayout()
	const apiBranch = "1-rate-limit-the-export-endpoint"

	// An existing tracked branch with one new commit: --dry-run lists it and pushes nothing.
	gitAt(t, api, "commit", "--quiet", "--allow-empty", "-m", "Tighten the burst limit")
	out, err := e.push(api, "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	for _, want := range []string{"demo-gl/api/" + apiBranch, "→ origin/" + apiBranch, "would push 1 commit", "Tighten the burst limit"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	origin := e.originOf("api")
	if remote := gitOut(t, origin, "rev-parse", apiBranch); remote == gitOut(t, api, "rev-parse", "HEAD") || len(e.audited("git.push")) != 0 {
		t.Fatalf("a dry run pushed (remote %s) or was audited %v", remote, e.audited("git.push"))
	}

	// The push lands on the remote and is audited.
	out, err = e.push(api)
	if err != nil || !strings.Contains(out, "pushing 1 commit") || !strings.Contains(out, "pushed demo-gl/api/"+apiBranch+" to origin/"+apiBranch) {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if remote, head := gitOut(t, origin, "rev-parse", apiBranch), gitOut(t, api, "rev-parse", "HEAD"); remote != head {
		t.Fatalf("remote %s, HEAD %s", remote, head)
	}
	es := e.audited("git.push")
	if len(es) != 1 || es[0].Project != "demo-gl" || es[0].Params["branch"] != apiBranch || es[0].Params["repo"] != "api" || es[0].Outcome != "ok" {
		t.Fatalf("audit = %+v", es)
	}

	// Nothing ahead any more: said, exit 0, nothing audited.
	out, err = e.push(api)
	if err != nil || !strings.Contains(out, "nothing to push: everything is on origin/"+apiBranch) || len(e.audited("git.push")) != 1 {
		t.Fatalf("second push: %v\n%s", err, out)
	}

	// A branch the remote does not have yet is created and made the upstream.
	const webBranch = "2-fix-truncated-column-headers-in-export-preview"
	gitAt(t, web, "commit", "--quiet", "--allow-empty", "-m", "Fix the header width")
	out, err = e.push(web)
	if err != nil || !strings.Contains(out, "(new branch)") || !strings.Contains(out, "Fix the header width") {
		t.Fatalf("new branch: %v\n%s", err, out)
	}
	if got := gitOut(t, web, "rev-parse", "--abbrev-ref", "@{u}"); got != "origin/"+webBranch {
		t.Errorf("upstream = %q", got)
	}
	if remote, head := gitOut(t, e.originOf("web"), "rev-parse", webBranch), gitOut(t, web, "rev-parse", "HEAD"); remote != head {
		t.Errorf("web remote %s, HEAD %s", remote, head)
	}
}

func TestPushJSONAndTheRefusals(t *testing.T) {
	e := newCLIEnv(t)
	api, _ := e.gcLayout()
	const apiBranch = "1-rate-limit-the-export-endpoint"

	// A branch that diverged from its remote one is refused, never forced.
	other := filepath.Join(t.TempDir(), "other")
	gitAt(t, filepath.Dir(other), "clone", "--quiet", e.originOf("api"), other)
	gitAt(t, other, "checkout", "--quiet", apiBranch)
	gitAt(t, other, "commit", "--quiet", "--allow-empty", "-m", "Somebody else's")
	gitAt(t, other, "push", "--quiet", "origin", apiBranch)
	gitAt(t, api, "fetch", "--quiet", "origin")
	gitAt(t, api, "commit", "--quiet", "--allow-empty", "-m", "Mine")
	before := gitOut(t, e.originOf("api"), "rev-parse", apiBranch)
	out, err := e.push(api)
	if err == nil || exitCode(err) != 2 || !strings.Contains(out, "update the branch first") || !strings.Contains(err.Error(), "never force-pushes") {
		t.Fatalf("diverged: err %v code %d\n%s", err, exitCode(err), out)
	}
	if after := gitOut(t, e.originOf("api"), "rev-parse", apiBranch); after != before || len(e.audited("git.push")) != 0 {
		t.Fatalf("a refused push changed the remote or was audited")
	}

	// --json carries the plan.
	asJSON(t)
	var res struct {
		Branch string `json:"branch"`
		Ahead  int    `json:"ahead"`
		Behind int    `json:"behind"`
		Refuse string `json:"refuse"`
		Pushed bool   `json:"pushed"`
	}
	if out, err = e.push(api, "--dry-run"); err == nil {
		t.Fatalf("a diverged --json dry run should refuse too:\n%s", out)
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if res.Branch != apiBranch || res.Ahead != 1 || res.Behind != 1 || res.Pushed || !strings.Contains(res.Refuse, "update the branch first") {
		t.Fatalf("json = %+v", res)
	}
}

func TestPushRefusesAgentRunsAndForeignDirectories(t *testing.T) {
	e := newCLIEnv(t)
	api, _ := e.gcLayout()
	gitAt(t, api, "commit", "--quiet", "--allow-empty", "-m", "agent change")
	// The central guard (agentguard.go) refuses `ade push` while ADE_AGENT_RUN=1.
	cmd, _, err := newRoot().Find([]string{"push"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(workspace.AgentRunEnv, "1")
	if err := checkAgentRun(cmd); err == nil || !strings.Contains(err.Error(), "refused while an agent run is in progress") {
		t.Fatalf("an agent run may push: %v", err)
	}
	t.Setenv(workspace.AgentRunEnv, "")
	if err := checkAgentRun(cmd); err != nil {
		t.Fatalf("a user may not push: %v", err)
	}
	if _, err := e.push(t.TempDir()); err == nil {
		t.Fatal("a directory outside ADE's layout was pushed")
	}
}

func TestPushSendsThePlannedCommitAndRefusesABranchThatMoved(t *testing.T) {
	e := newCLIEnv(t)
	api, _ := e.gcLayout()
	const apiBranch = "1-rate-limit-the-export-endpoint"
	ps := project.New(e.app, "user")
	gitAt(t, api, "commit", "--quiet", "--allow-empty", "-m", "first")
	plan, err := ps.PushPlan(context.Background(), api)
	if err != nil || !plan.Pushable() || plan.Head != gitOut(t, api, "rev-parse", "HEAD") {
		t.Fatalf("plan = %+v, %v", plan, err)
	}

	// A commit made after the preview is not what the user looked at.
	before := gitOut(t, e.originOf("api"), "rev-parse", apiBranch)
	gitAt(t, api, "commit", "--quiet", "--allow-empty", "-m", "second")
	if err := ps.Push(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "changed since the preview") {
		t.Fatalf("push of a moved branch: %v", err)
	}
	if got := gitOut(t, e.originOf("api"), "rev-parse", apiBranch); got != before || len(e.audited("git.push")) != 0 {
		t.Fatalf("a refused push changed the remote (%s -> %s) or was audited", before, got)
	}

	// A fresh preview pushes exactly its commit.
	plan, err = ps.PushPlan(context.Background(), api)
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.Push(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, e.originOf("api"), "rev-parse", apiBranch); got != plan.Head {
		t.Fatalf("remote %s, planned %s", got, plan.Head)
	}
	es := e.audited("git.push")
	if len(es) != 1 || es[0].Params["sha"] != plan.Head {
		t.Fatalf("audit = %+v", es)
	}
}

func TestPushOfABranchWithNoCommitsOfItsOwnSaysSo(t *testing.T) {
	e := newCLIEnv(t)
	_, web := e.gcLayout() // cut from origin/main, nothing committed, no remote branch
	out, err := e.push(web)
	if err != nil || !strings.Contains(out, "has no commits that origin does not already have") {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if len(e.audited("git.push")) != 0 {
		t.Fatal("nothing was pushed, but it was audited")
	}
}
