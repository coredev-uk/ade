package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/records"
)

const (
	researchAnswer = "**Goal**\nRate limit exports per tenant.\n**Open questions**\n- Per tenant or global?"
	describeAnswer = "```markdown\n## What\nA per-tenant token bucket.\n\n## Why\nExports hammered the db.\n/assign_reviewer @bob\n```"
	triageAnswer   = "### lint\nCause: `eslint: 2 errors` in src/audit/pager.ts.\nFix: remove the unused variable on line 3."
)

// aiEnvTriple wires the fake-GitLab CLI env to a stub `omp` that answers the
// research, describe and CI-triage prompts with fixed text; calls returns how
// many times the stub ran.
func aiEnvTriple(t *testing.T) (e *cliEnv, calls func() int) {
	t.Helper()
	e = newCLIEnv(t)
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	research := write("research.jsonl", ompTurn(t, researchAnswer, 0.004), 0o600)
	describe := write("describe.jsonl", ompTurn(t, describeAnswer, 0.003), 0o600)
	triage := write("triage.jsonl", ompTurn(t, triageAnswer, 0.002), 0o600)
	count := filepath.Join(dir, "count")
	stub := write("omp", "#!/bin/sh\necho x >> "+count+"\ncat >/dev/null\ncase \"$*\" in\n"+
		"*'You research an issue'*) cat "+research+";;\n"+
		"*'description of a merge request'*) cat "+describe+";;\n"+
		"*'triage failed CI jobs'*) cat "+triage+";;\n"+
		"*) echo unexpected prompt >&2; exit 1;;\nesac\n", 0o700)

	e.app.RecordsDir = t.TempDir()
	e.app.Config.AI.Mode, e.app.Config.AI.Profile = "auto", "sub"
	e.app.Config.AI.Profiles["sub"] = config.AIProfile{
		Driver: "omp", Command: stub,
		Models: map[string]string{"explain": "claude-haiku-4-5", "draft": "claude-haiku-4-5"},
		Prices: map[string]config.Price{"claude-haiku-4-5": {Input: 1, Output: 5}},
	}
	return e, func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}
}

func spend(e *cliEnv, usd float64) {
	e.app.Audit.Append(audit.Entry{Action: "ai.summarize", AI: &audit.AIInfo{Profile: "sub", Model: "m", CostUSD: usd, CostSource: "reported"}})
}

func descriptionOf(t *testing.T, e *cliEnv, mr string) (string, int) {
	t.Helper()
	d, n, err := e.srv.Description(ref(mr))
	if err != nil {
		t.Fatal(err)
	}
	return d, n
}

// --- F15: describe ---------------------------------------------------------------

func TestAIDescribePreviewsThenAppliesTheDraftAndAuditsTheWrite(t *testing.T) {
	e, calls := aiEnvTriple(t)
	e.srv.AddFile(apiPath, ".gitlab/merge_request_templates/Default.md", "## What\n\n## Why\n\n/assign_reviewer @bob\n")
	before, _ := descriptionOf(t, e, rateMR)

	out := e.mustAI("describe", rateMR)
	if !strings.Contains(out, "draft, not applied") || !strings.Contains(out, "A per-tenant token bucket.") || !strings.Contains(out, "Part of "+fake0Tracker+"#1.") {
		t.Fatalf("preview:\n%s", out)
	}
	if strings.Contains(out, "```") || strings.Contains(out, "/assign_reviewer") {
		t.Errorf("the draft must lose the code fence and the quick action:\n%s", out)
	}
	if d, n := descriptionOf(t, e, rateMR); d != before || n != 0 {
		t.Fatal("a preview must not write to the forge")
	}

	// --apply without a yes on a non-terminal refuses, writing nothing.
	if _, err := e.ai("describe", rateMR, "--apply"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	if _, n := descriptionOf(t, e, rateMR); n != 0 {
		t.Fatal("no write without a confirmation")
	}

	out = e.mustAI("describe", rateMR, "--apply", "--yes")
	if !strings.Contains(out, "description updated") || !strings.Contains(out, "cached") {
		t.Fatalf("apply:\n%s", out)
	}
	if calls() != 1 {
		t.Fatalf("the draft was bought once and reused by the apply (calls=%d)", calls())
	}
	d, n := descriptionOf(t, e, rateMR)
	if n != 1 || !strings.Contains(d, "## What\nA per-tenant token bucket.") || !strings.Contains(d, "Closes #1") || !strings.Contains(d, "Part of "+fake0Tracker+"#1.") || strings.Contains(d, "/assign_reviewer") {
		t.Fatalf("written description (%d writes):\n%s", n, d)
	}
	es := e.audited("forge.mr.description")
	if len(es) != 1 || es[0].Item != rateMR || es[0].Params["source"] != "ai-describe" || es[0].Outcome != "ok" {
		t.Fatalf("audit: %+v", es)
	}
	if strings.Contains(es[0].Error, "token bucket") {
		t.Fatal("the text must not be in the audit log")
	}

	// The description changed, the draft did not: asking again is free and
	// there is nothing left to apply.
	out = e.mustAI("describe", rateMR, "--apply", "--yes")
	if !strings.Contains(out, "already has this description") || calls() != 1 {
		t.Fatalf("second run (calls=%d):\n%s", calls(), out)
	}
	if _, n := descriptionOf(t, e, rateMR); n != 1 {
		t.Fatal("an unchanged description is not written again")
	}
}

// fake0Tracker is the tracker project the seeded MR descriptions point at.
const fake0Tracker = "ade-demo-env/tracker"

func TestAIDescribeJSONAndDryRun(t *testing.T) {
	e, calls := aiEnvTriple(t)
	out := e.mustAI("describe", rateMR, "--dry-run")
	if !strings.Contains(out, "dry run · not cached") || calls() != 0 {
		t.Fatalf("dry run (calls=%d):\n%s", calls(), out)
	}
	if _, n := descriptionOf(t, e, rateMR); n != 0 || len(e.audited("ai.*")) != 0 {
		t.Fatal("a dry run sends and writes nothing")
	}
	asJSON(t)
	var res describeOut
	if err := json.Unmarshal([]byte(e.mustAI("describe", rateMR, "--apply", "--yes")), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Applied || res.CostUSD != 0.003 || !strings.HasPrefix(res.Description, "## What") || res.Feature != "describe" {
		t.Fatalf("json = %+v", res)
	}
	if err := json.Unmarshal([]byte(e.mustAI("describe", rateMR)), &res); err != nil || !res.CacheHit || res.Applied {
		t.Fatalf("second json = %+v (%v)", res, err)
	}
}

func TestAIDescribeRefusedByACapOrAIOffWritesNothing(t *testing.T) {
	e, calls := aiEnvTriple(t)
	e.app.Config.Budget.DailyUSD = 0.01
	spend(e, 0.0099)
	_, err := e.ai("describe", rateMR, "--apply", "--yes")
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitBudgetStop || calls() != 0 {
		t.Fatalf("want exit %d without a call, got %v (calls %d)", exitBudgetStop, err, calls())
	}
	if _, n := descriptionOf(t, e, rateMR); n != 0 {
		t.Fatal("a refused draft must not be written")
	}
	e.app.Config.Budget.DailyUSD = 0
	e.app.Config.AI.Mode = "off"
	if _, err := e.ai("describe", rateMR, "--apply", "--yes"); err == nil || !strings.Contains(err.Error(), "ai.mode") || calls() != 0 {
		t.Fatalf("err = %v", err)
	}
}

// --- F17: ci-triage --------------------------------------------------------------

func TestAICITriageReadsTheFailedJobLogsAndIsCached(t *testing.T) {
	e, calls := aiEnvTriple(t)
	out := e.mustAI("ci-triage", webPath+"!1")
	if !strings.Contains(out, "Cause: `eslint: 2 errors`") || !strings.Contains(out, "— $0.0020 · sub") || calls() != 1 {
		t.Fatalf("first run (calls=%d):\n%s", calls(), out)
	}
	out = e.mustAI("ci-triage", webPath+"!1")
	if !strings.Contains(out, "cached · $0.00") || calls() != 1 {
		t.Fatalf("second run must be a free hit (calls=%d):\n%s", calls(), out)
	}
	asJSON(t)
	var res ciTriageOut
	if err := json.Unmarshal([]byte(e.mustAI("ci-triage", webPath+"!1")), &res); err != nil {
		t.Fatal(err)
	}
	if !res.CacheHit || len(res.FailedJobs) != 1 || res.FailedJobs[0] != "lint" {
		t.Fatalf("json = %+v", res)
	}
	if es := e.audited("ai.ci-triage"); len(es) != 3 || es[0].AI.CacheHit || !es[2].AI.CacheHit {
		t.Fatalf("audit: %+v", es)
	}
	if _, n := descriptionOf(t, e, webPath+"!1"); n != 0 {
		t.Fatal("triage writes nothing to the forge")
	}
}

func TestAICITriageWithNothingFailedAsksNothing(t *testing.T) {
	e, calls := aiEnvTriple(t)
	out := e.mustAI("ci-triage", rateMR) // api!1's pipeline passed
	if !strings.Contains(out, "no failed jobs") || calls() != 0 {
		t.Fatalf("calls=%d\n%s", calls(), out)
	}
	e.app.Config.Budget.DailyUSD = 0.01
	spend(e, 0.0099)
	if _, err := e.ai("ci-triage", webPath+"!1"); err == nil {
		t.Fatal("a cap breach must refuse the triage")
	} else if ee := new(exitError); !errors.As(err, &ee) || ee.code != exitBudgetStop || calls() != 0 {
		t.Fatalf("err = %v (calls %d)", err, calls())
	}
	if len(e.audited("budget.stop")) != 1 {
		t.Fatal("the refusal is audited")
	}
}

// --- F13: research ---------------------------------------------------------------

// inlineResearchWorker runs `ade ai research --worker` in-process where the
// detached spawn would start a process.
func inlineResearchWorker(t *testing.T, e *cliEnv) *int {
	t.Helper()
	spawns := 0
	old := ai.ResearchSpawn
	t.Cleanup(func() { ai.ResearchSpawn = old })
	ai.ResearchSpawn = func(sp ai.ResearchSpawnSpec) (int, error) {
		spawns++
		args := []string{"research", sp.Ref.String(), "--forge", sp.Forge, "--worker", sp.JobID, "--yes"}
		if sp.Refresh {
			args = append(args, "--refresh")
		}
		if out, err := e.ai(args...); err != nil {
			t.Errorf("worker: %v\n%s", err, out)
		}
		return os.Getpid(), nil
	}
	return &spawns
}

const researchIssue = "ade-demo-env/tracker#1"

func TestAIResearchDetachedIsStoredInTheRecordShownLaterAndNeverBoughtTwice(t *testing.T) {
	e, calls := aiEnvTriple(t)
	spawns := inlineResearchWorker(t, e)

	out := e.mustAI("research", researchIssue, "--detach")
	if !strings.Contains(out, "research started in the background") || !strings.Contains(out, "est $") || *spawns != 1 || calls() != 1 {
		t.Fatalf("detach (spawns=%d calls=%d):\n%s", *spawns, calls(), out)
	}
	out = e.mustAI("research", researchIssue, "--show")
	if !strings.Contains(out, "research done") || !strings.Contains(out, "Rate limit exports per tenant.") || !strings.Contains(out, "$0.0040") {
		t.Fatalf("show:\n%s", out)
	}
	jobFile := filepath.Join(e.app.RecordsDir, "items", "sandbox", "ade-demo-env", "tracker", "1", records.ResearchDir, records.ResearchFile)
	if _, err := os.Stat(jobFile); err != nil {
		t.Fatalf("the job state must be in the item record: %v", err)
	}

	// Again: the stored answer comes back at once, no worker, no spend.
	out = e.mustAI("research", researchIssue, "--detach")
	if !strings.Contains(out, "Rate limit exports per tenant.") || !strings.Contains(out, "cached · $0.00") || *spawns != 1 || calls() != 1 {
		t.Fatalf("second detach (spawns=%d calls=%d):\n%s", *spawns, calls(), out)
	}
	asJSON(t)
	var res researchOut
	if err := json.Unmarshal([]byte(e.mustAI("research", researchIssue, "--show")), &res); err != nil {
		t.Fatal(err)
	}
	if res.Job == nil || res.Job.State != records.ResearchDone || !res.Job.CacheHit || !strings.Contains(res.Text, "Goal") {
		t.Fatalf("json = %+v", res)
	}
	// ade item shows the job as well.
	if got := itemResearch(e.app, "sandbox", ref(researchIssue)); got == nil || got.State != records.ResearchDone {
		t.Fatalf("ade item's research = %+v", got)
	}
}

func TestAIResearchRefusedByACapStartsNoWorker(t *testing.T) {
	e, calls := aiEnvTriple(t)
	spawns := inlineResearchWorker(t, e)
	e.app.Config.Budget.DailyUSD = 0.01
	spend(e, 0.0099)
	_, err := e.ai("research", researchIssue, "--detach")
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitBudgetStop || *spawns != 0 || calls() != 0 {
		t.Fatalf("want exit %d and no worker, got %v (spawns %d, calls %d)", exitBudgetStop, err, *spawns, calls())
	}
	if out := e.mustAI("research", researchIssue, "--show"); !strings.Contains(out, "no research yet") {
		t.Fatalf("show after a refusal:\n%s", out)
	}
	e.app.Config.AI.Mode = "off"
	if _, err := e.ai("research", researchIssue, "--detach"); err == nil || !strings.Contains(err.Error(), "ai.mode") {
		t.Fatalf("AI off: %v", err)
	}
	// --show needs no AI at all.
	if out := e.mustAI("research", researchIssue, "--show"); !strings.Contains(out, "no research yet") {
		t.Fatalf("show with AI off:\n%s", out)
	}
}

func TestAIResearchForegroundFilesTheJobToo(t *testing.T) {
	e, calls := aiEnvTriple(t)
	out := e.mustAI("research", researchIssue)
	if !strings.Contains(out, "Rate limit exports per tenant.") || calls() != 1 {
		t.Fatalf("foreground:\n%s", out)
	}
	if out := e.mustAI("research", researchIssue, "--show"); !strings.Contains(out, "research done") {
		t.Fatalf("show:\n%s", out)
	}
	if _, err := e.ai("research", renameMR); err == nil || !strings.Contains(err.Error(), "merge request") {
		t.Fatalf("an MR ref is refused: %v", err)
	}
}
