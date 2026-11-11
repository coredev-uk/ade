package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/agent"
	"github.com/coredev-uk/ade/internal/forge"
)

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
