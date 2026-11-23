package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/agent"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
)

// gitWorktree makes a repo with a linked worktree on branch 7-x, the way start
// work leaves one, and returns the worktree.
func gitWorktree(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	tmp := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	main := filepath.Join(tmp, "api")
	run(tmp, "init", "-q", "-b", "main", main)
	run(main, "config", "user.name", "Test User")
	run(main, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(main, "README.md"), []byte("api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(main, "add", ".")
	run(main, "commit", "-q", "-m", "init")
	wt := filepath.Join(tmp, "7-x")
	run(main, "worktree", "add", "-q", "-b", "7-x", wt)
	return wt
}

// seedRun stores a run the way `ade agent plan` leaves it, without any agent.
func (e *cliEnv) seedRun(id string, st agent.State, cost float64) (agent.Run, *agent.Store) {
	e.t.Helper()
	e.app.RecordsDir = e.t.TempDir()
	mgr, err := agent.ForApp(e.app)
	if err != nil {
		e.t.Fatal(err)
	}
	now := time.Now()
	r := agent.Run{
		ID: id, Kind: agent.KindPlan, State: st, Forge: "sandbox", Project: "demo",
		Tracker: forge.Ref{Project: "g/tracker", IID: 7, Kind: forge.KindIssue}, Repo: "api",
		Worktree: e.t.TempDir(), Branch: "7-x", Driver: "omp", Model: "m", CapUSD: 0.5, CostUSD: cost, Created: now, Updated: now,
		Calls: []agent.Call{{Kind: agent.KindPlan, Started: now.Add(-time.Minute), Ended: now, CostUSD: cost}},
	}
	if err := mgr.Store.Save(r); err != nil {
		e.t.Fatal(err)
	}
	return r, mgr.Store
}

func (e *cliEnv) agent(args ...string) (string, error) { return e.run(newAgentCmd, args...) }

func TestAgentListShowApproveCancel(t *testing.T) {
	e := newCLIEnv(t)
	r, st := e.seedRun("20261111-100000-aaa", agent.StateAwaiting, 0.0123)
	done := r
	done.ID, done.State = "20261110-100000-bbb", agent.StatePushed
	if err := st.Save(done); err != nil {
		t.Fatal(err)
	}

	out, err := e.agent("list")
	if err != nil || !strings.Contains(out, "20261111-100000-aaa") || !strings.Contains(out, "awaiting-approval") || !strings.Contains(out, "$0.0123/$0.50") {
		t.Errorf("list = %q %v", out, err)
	}
	if strings.Contains(out, "20261110-100000-bbb") {
		t.Errorf("a finished run is hidden without --all:\n%s", out)
	}
	if out, _ := e.agent("list", "--all"); !strings.Contains(out, "20261110-100000-bbb") || !strings.Contains(out, "pushed") {
		t.Errorf("list --all = %q", out)
	}

	asJSON(t)
	out, err = e.agent("show", "aaa")
	var got agent.Run
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.ID != r.ID || got.CostUSD != 0.0123 {
		t.Errorf("show by suffix = %q %v", out, err)
	}
	jsonOut = false

	// Approve needs the plan; the unapproved run cannot be run or pushed.
	if _, err := e.agent("approve", "aaa"); err == nil || !strings.Contains(err.Error(), "no plan") {
		t.Errorf("approve without plan.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir(r), agent.FilePlan), []byte("# Plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := e.agent("approve", "aaa"); err != nil || !strings.Contains(out, "awaiting-approval") {
		t.Errorf("approve: %q %v", out, err)
	}
	if _, err := e.agent("push", "aaa"); err == nil || !strings.Contains(err.Error(), "only a run in review") {
		t.Errorf("push of a run not in review: %v", err)
	}
	if _, err := e.agent("diff", "aaa"); err == nil || !strings.Contains(err.Error(), "has not changed anything yet") {
		t.Errorf("diff before the run: %v", err)
	}
	if _, err := e.agent("show", "zzz"); err == nil || !strings.Contains(err.Error(), "no run") {
		t.Errorf("unknown run: %v", err)
	}
	if _, err := e.agent("cancel", "aaa"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := st.Get("aaa"); cur.State != agent.StateCancelled {
		t.Errorf("state after cancel = %s", cur.State)
	}
	if _, err := e.agent("cancel", "aaa"); err == nil || !strings.Contains(err.Error(), "already cancelled") {
		t.Errorf("cancel twice: %v", err)
	}
	if audits := e.audited("agent.plan.approve"); len(audits) != 1 {
		t.Errorf("approval audited %d times", len(audits))
	}
}

func TestAgentPlanNeedsARegisteredProject(t *testing.T) {
	e := newCLIEnv(t)
	e.app.RecordsDir = t.TempDir()
	// plan needs a registered project first; with none, the error names that
	// step rather than reaching the agent.
	if _, err := e.agent("plan", "g/tracker#1", "--yes"); err == nil || !strings.Contains(err.Error(), "no projects registered") {
		t.Errorf("plan with no project: %v", err)
	}
}

func TestAgentRunBackgroundStartsADetachedAIBranchRun(t *testing.T) {
	e := newCLIEnv(t)
	r, st := e.seedRun("20261111-100000-aaa", agent.StateAwaiting, 0.01)
	// The checks that can fail up front run before the child starts: a real
	// worktree, an AI profile, and an agent CLI on the path.
	dir := t.TempDir()
	omp := filepath.Join(dir, "omp")
	if err := os.WriteFile(omp, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	e.app.Config.AI.Profile = "t"
	e.app.Config.AI.Profiles = map[string]config.AIProfile{"t": {Driver: "omp", Command: omp, Models: map[string]string{"implement": "m"}}}
	r.Worktree, r.Profile = gitWorktree(t), "t"
	if err := st.Save(r); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(dir, "args")
	stub := filepath.Join(dir, "ade")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"$@\" > "+args+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := adeExecutable
	adeExecutable = func() (string, error) { return stub, nil }
	t.Cleanup(func() { adeExecutable = old })

	// An unapproved plan starts nothing.
	if _, err := e.agent("run", "aaa", "--background"); err == nil || !strings.Contains(err.Error(), "approve the plan first") {
		t.Fatalf("background run of an unapproved plan: %v", err)
	}
	if _, err := os.Stat(args); err == nil {
		t.Fatal("a refused background run started a process")
	}

	if err := os.WriteFile(filepath.Join(st.Dir(r), agent.FilePlan), []byte("# Plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.agent("approve", "aaa"); err != nil {
		t.Fatal(err)
	}
	out, err := e.agent("run", "aaa", "--background")
	if err != nil || !strings.Contains(out, "in the background") || !strings.Contains(out, "7-x-ai") || !strings.Contains(out, "ade agent accept") {
		t.Fatalf("run --background: %q %v", out, err)
	}
	// The child is the same binary asked for an --ai-branch run of this run.
	var got string
	for range 100 {
		if b, err := os.ReadFile(args); err == nil && len(b) > 0 {
			got = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got != "agent run "+r.ID+" --ai-branch --detached" {
		t.Fatalf("child args = %q", got)
	}
	if _, err := os.Stat(filepath.Join(st.Dir(r), "background.log")); err != nil {
		t.Fatalf("no background.log: %v", err)
	}
}

func TestAgentAcceptAndDiscardNeedAnAIBranchRun(t *testing.T) {
	e := newCLIEnv(t)
	_, st := e.seedRun("20261111-100000-aaa", agent.StateReview, 0.01)
	if _, err := e.agent("accept", "aaa"); err == nil || !strings.Contains(err.Error(), "no -ai branch") {
		t.Errorf("accept on a run without a -ai branch: %v", err)
	}
	// Discard on a worktree run is a restore: a run that changed nothing has
	// nothing to discard.
	if _, err := e.agent("discard", "aaa"); err == nil || !strings.Contains(err.Error(), "nothing to discard") {
		t.Errorf("discard on a run that changed nothing: %v", err)
	}
	// A finished run that still holds a -ai branch stays in the default list.
	r, _ := st.Get("aaa")
	r.State, r.AIBranch, r.AIWorktree = agent.StateFailed, "7-x-ai", "/tmp/x-ai"
	if err := st.Save(r); err != nil {
		t.Fatal(err)
	}
	if out, err := e.agent("list"); err != nil || !strings.Contains(out, "failed (-ai)") {
		t.Errorf("list must keep a failed run with a pending -ai branch: %q %v", out, err)
	}
}
