package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/records"
)

// The MR description (B), CI triage (t on the CI tab) and issue research (R on
// Home) run against the fake GitLab and a stub omp that answers each by its
// system prompt, like the other AI tests of the page.

const (
	stubDescribe = "```markdown\n## What\nA per-tenant token bucket.\n\n## Why\nExports hammered the db.\n/assign_reviewer @bob\n```"
	stubTriage   = "### lint\nCause: `eslint: 2 errors` in src/audit/pager.ts.\nFix: remove the unused variable."
	stubResearch = "**Goal**\nRate limit exports per tenant.\n**Open questions**\n- Per tenant or global?"
)

// enableAIText points the page at a stub omp for the three features of this
// file (and a summary, since opening the Overview asks for one).
func (e *mrEnv) enableAIText(mode string) *aiStub {
	e.t.Helper()
	dir := e.t.TempDir()
	for name, text := range map[string]string{"sum": stubSummary, "describe": stubDescribe, "triage": stubTriage, "research": stubResearch} {
		if err := os.WriteFile(filepath.Join(dir, name+".jsonl"), []byte(turnEnd(text)), 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
	calls := filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
in=$(cat)
case "$*" in
  *"summarise merge requests"*) f=sum ;;
  *"description of a merge request"*) f=describe ;;
  *"triage failed CI jobs"*) f=triage ;;
  *"You research an issue"*) f=research ;;
  *) f=unknown ;;
esac
printf '%s' "$in" > "` + dir + `/prompt-$f.txt"
echo $f >> "` + calls + `"
cat "` + dir + `/$f.jsonl"
`
	path := filepath.Join(dir, "omp")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		e.t.Fatal(err)
	}
	a := e.m.app
	a.RecordsDir = e.t.TempDir()
	a.Config.AI.Mode, a.Config.AI.Profile = mode, "sub"
	a.Config.AI.Profiles["sub"] = config.AIProfile{
		Driver: "omp", Command: path,
		Models: map[string]string{"explain": "claude-haiku-4-5", "draft": "claude-haiku-4-5"},
		Prices: map[string]config.Price{"claude-haiku-4-5": {Input: 1, Output: 5}},
	}
	return &aiStub{t: e.t, dir: dir, calls: calls}
}

func (e *mrEnv) overspend(usd float64) {
	e.m.app.Config.Budget.DailyUSD = 0.01
	e.m.app.Audit.Append(audit.Entry{Action: "ai.summarize", AI: &audit.AIInfo{Profile: "sub", Model: "m", CostUSD: usd, CostSource: "reported"}})
}

func (e *mrEnv) published(kind string) []events.Event {
	var out []events.Event
	for _, ev := range e.drainEvents() {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func ctrl(e *mrEnv, r rune) {
	e.t.Helper()
	next, cmd := e.m.Update(tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl})
	e.m = next.(model)
	e.settle(cmd)
}

// --- B ---------------------------------------------------------------------------------

func TestBDraftsTheDescriptionAndEnterWritesItToTheMR(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAIText("auto")
	ref := mrRef("api", 1)
	e.open(ref.String())
	before, _, _ := e.srv.Description(ref)

	e.press("B")
	e.want("MR description · "+ref.String(), "AI draft · claude-haiku-4-5", "A per-tenant token bucket.", "enter write to the MR")
	e.wantNot("/assign_reviewer", "```")
	if d, n, _ := e.srv.Description(ref); d != before || n != 0 {
		t.Fatal("the draft is only shown: nothing is written until enter")
	}
	prompt := stub.prompt("describe")
	if !strings.Contains(prompt, "Expose Burst") || !strings.Contains(prompt, "Closes #1") {
		t.Errorf("the prompt must carry the commits and the links to keep:\n%s", prompt)
	}

	e.press("enter")
	d, n, _ := e.srv.Description(ref)
	if n != 1 || !strings.Contains(d, "## What\nA per-tenant token bucket.") || !strings.Contains(d, "Closes #1") || !strings.Contains(d, "Part of "+grp+"/tracker#1.") {
		t.Fatalf("written description (%d writes):\n%s", n, d)
	}
	if es := e.audited("forge.mr.description"); len(es) != 1 || es[0].Item != ref.String() || es[0].Params["source"] != "ai-describe" || es[0].Outcome != "ok" {
		t.Fatalf("audit: %+v", es)
	}
	if got := e.published("mr.describe"); len(got) != 1 || !strings.Contains(got[0].Title, "Description updated on") {
		t.Fatalf("events: %+v", got)
	}
	if e.m.mr.modal != nil {
		t.Error("the dialog closes on enter")
	}

	// B again: the draft is reopened from memory, nothing is asked, and the
	// MR already has it.
	e.press("B")
	e.press("enter")
	e.want("already has this description")
	if stub.count("describe") != 1 {
		t.Errorf("model calls = %d, want 1", stub.count("describe"))
	}
	if _, n, _ := e.srv.Description(ref); n != 1 {
		t.Error("an unchanged description is not written again")
	}
}

func TestBEscThrowsTheDraftAwayAndACachedDraftCostsNothing(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAIText("auto")
	ref := mrRef("api", 1)
	e.open(ref.String())
	e.press("B", "esc")
	if _, n, _ := e.srv.Description(ref); n != 0 || e.m.mr.modal != nil {
		t.Fatal("esc must write nothing")
	}
	e.m.mrCache = nil // a new session: the artifact cache answers
	e.press("esc")
	e.open(ref.String())
	e.press("B")
	e.want("A per-tenant token bucket.", "cached, $0")
	if stub.count("describe") != 1 {
		t.Errorf("the second draft called the model: %v", stub.asked())
	}
	if es := e.audited("ai.describe"); len(es) != 2 || !es[1].AI.CacheHit || es[1].AI.CostUSD != 0 {
		t.Fatalf("audit: %+v", es)
	}
	// ctrl+r buys a new draft past the cache.
	ctrl(e, 'r')
	if stub.count("describe") != 2 {
		t.Errorf("ctrl+r should ask again: %v", stub.asked())
	}
}

func TestBRefusedByACapOrAskingFirst(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAIText("auto")
	ref := mrRef("api", 1)
	e.open(ref.String())
	e.overspend(0.0099)
	e.press("B")
	if stub.count("describe") != 0 || e.m.mr.modal != nil {
		t.Fatal("a refused call sends nothing and opens nothing")
	}
	got := e.published("ai.failed")
	if len(got) != 1 || !strings.Contains(got[0].Title, "MR description failed") || !strings.Contains(got[0].Detail, "budget stop") {
		t.Fatalf("events: %+v", got)
	}
	if len(e.audited("budget.stop")) != 1 {
		t.Error("the refusal is audited")
	}
	if _, n, _ := e.srv.Description(ref); n != 0 {
		t.Fatal("nothing is written")
	}

	// In ask mode a call that needs a yes waits in a dialog; n sends nothing.
	e.m.app.Config.Budget.DailyUSD = 0
	e.m.app.Config.AI.Mode = "ask"
	e.m.app.Config.Budget.ConfirmOverUSD = 0.0000001
	e.press("B")
	e.want("Ask the model · MR description", "Nothing is sent until you say yes.")
	e.press("n")
	if stub.count("describe") != 0 {
		t.Fatal("n must not send")
	}
	e.press("B", "y")
	e.want("A per-tenant token bucket.")
}

func TestBIsAbsentWithAIOff(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.open(mrRef("api", 1).String())
	e.press("B")
	if e.m.mr.modal != nil || len(e.audited("ai.*")) != 0 {
		t.Fatal("B must do nothing without AI")
	}
	e.wantNot("AI description", "B")
	if rows := mrHelpRows(false); len(rows) == len(mrHelpRows(true)) {
		t.Error("the AI rows are only in the help while AI is on")
	}
}

// --- t on the CI tab ---------------------------------------------------------------------

func TestTOnTheCITabTriagesTheFailedJobsAndIsCached(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAIText("auto")
	ref := mrRef("web", 1) // its lint job failed
	e.open(ref.String())
	e.press("5")
	e.want("lint", "1 failed job", "t asks the AI for cause and fix")
	e.press("t")
	e.want("CI triage", "AI · claude-haiku-4-5", "Cause: `eslint: 2 errors`", "Fix: remove the unused variable.")
	prompt := stub.prompt("triage")
	if !strings.Contains(prompt, "### Job lint") || !strings.Contains(prompt, "no-unused-vars") || strings.Contains(prompt, "### Job build") {
		t.Errorf("only the failed job's log goes to the model:\n%s", prompt)
	}
	e.press("t")
	if stub.count("triage") != 1 {
		t.Errorf("the same logs are answered from the cache: %v", stub.asked())
	}
	if es := e.audited("ai.ci-triage"); len(es) != 2 || es[0].AI.CacheHit || !es[1].AI.CacheHit {
		t.Fatalf("audit: %+v", es)
	}
	if len(e.audited("forge.*")) != 0 {
		t.Error("triage writes nothing to the forge")
	}
}

func TestTWithNothingFailedOrAIOffAsksNothing(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAIText("auto")
	e.open(mrRef("api", 1).String()) // its pipeline passed
	e.press("5", "t")
	if stub.count("triage") != 0 || e.m.mr.data.ai[aiKey{kind: aiCITriage}] != nil && e.m.mr.data.ai[aiKey{kind: aiCITriage}].l.busy() {
		t.Fatal("no failed job: nothing is asked")
	}
	e.wantNot("t asks the AI")

	off := newMREnv(t, fake.Free)
	off.open(mrRef("web", 1).String())
	off.press("5", "t")
	off.wantNot("t asks the AI", "CI triage")
	if len(off.audited("ai.*")) != 0 {
		t.Fatal("AI off: no call")
	}
}

func TestTriageRefusedByACapSendsNothing(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAIText("auto")
	e.open(mrRef("web", 1).String())
	e.press("5")
	e.overspend(0.0099)
	e.press("t")
	if stub.count("triage") != 0 {
		t.Fatal("a refused triage sends nothing")
	}
	if got := e.published("ai.failed"); len(got) != 1 || !strings.Contains(got[0].Title, "CI triage failed") {
		t.Fatalf("events: %+v", got)
	}
}

// --- R on Home ---------------------------------------------------------------------------

// researchWorkerInline runs the detached worker in-process where the dialog
// would start a process.
func researchWorkerInline(t *testing.T, e *mrEnv) *int {
	t.Helper()
	spawns := 0
	old := ai.ResearchSpawn
	t.Cleanup(func() { ai.ResearchSpawn = old })
	ai.ResearchSpawn = func(sp ai.ResearchSpawnSpec) (int, error) {
		spawns++
		ctx := context.Background()
		in, err := ai.GatherResearch(ctx, e.api, sp.Ref)
		if err != nil {
			t.Error(err)
			return 0, err
		}
		svc, err := ai.NewService(e.m.app)
		if err != nil {
			t.Error(err)
			return 0, err
		}
		if _, err := svc.RunResearch(ctx, sp.Forge, sp.JobID, ai.ResearchJob(sp.Forge, in), ai.ResearchOpts{Assume: true, Notify: true}); err != nil {
			t.Error(err)
		}
		return os.Getpid(), nil
	}
	return &spawns
}

func selectTracker(e *mrEnv) {
	for i, r := range e.m.homeRows {
		if r.item != nil {
			e.m.homeSel = i
		}
	}
}

func TestRStartsADetachedResearchStoresItAndShowsItLater(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAIText("auto")
	spawns := researchWorkerInline(t, e)
	selectTracker(e)

	e.press("R")
	e.want("Research · "+grp+"/tracker#1", "none yet", "enter start in the background")
	e.press("enter")
	if *spawns != 1 || stub.count("research") != 1 {
		t.Fatalf("spawns=%d calls=%d", *spawns, stub.count("research"))
	}
	e.want("done", "Rate limit exports per tenant.", "Per tenant or global?")
	if _, err := os.Stat(e.m.researchService().Records.ResearchPath("f", forge.Ref{Project: grp + "/tracker", IID: 1})); err != nil {
		t.Fatalf("the job state belongs in the item's record: %v", err)
	}
	e.press("esc")

	// Later: the side panel says it is done, R shows the stored answer, and
	// nothing is spent.
	e.m.showDetail = true
	e.want("research", "R shows it")
	e.press("R")
	e.want("Rate limit exports per tenant.")
	if stub.count("research") != 1 {
		t.Fatal("showing a stored answer costs nothing")
	}
	e.press("esc")

	// The job file is lost: starting again is answered from the AI cache.
	if err := os.Remove(e.m.researchService().Records.ResearchPath("f", forge.Ref{Project: grp + "/tracker", IID: 1})); err != nil {
		t.Fatal(err)
	}
	e.press("R", "enter")
	e.want("cached, $0", "Rate limit exports per tenant.")
	if *spawns != 1 || stub.count("research") != 1 {
		t.Fatalf("the unchanged issue was bought twice (spawns=%d calls=%d)", *spawns, stub.count("research"))
	}
	if es := e.audited("ai.research"); len(es) != 2 || es[0].AI.CacheHit || !es[1].AI.CacheHit {
		t.Fatalf("audit: %+v", es)
	}
	if got := e.published("ai.research.done"); len(got) != 1 {
		t.Fatalf("one done notification for the worker's job: %+v", got)
	}
}

func TestRRefusedByACapStartsNoWorkerAndAskingFirstWaitsForYes(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAIText("auto")
	spawns := researchWorkerInline(t, e)
	selectTracker(e)
	e.overspend(0.0099)
	e.press("R", "enter")
	e.want("budget stop")
	if *spawns != 0 || stub.count("research") != 0 {
		t.Fatal("a refused research starts nothing")
	}
	e.press("esc")

	e.m.app.Config.Budget.DailyUSD = 0
	e.m.app.Config.AI.Mode = "ask"
	e.m.app.Config.Budget.ConfirmOverUSD = 0.0000001
	e.press("R", "enter")
	e.want("waiting for a yes", "Nothing is sent until you say yes.")
	if *spawns != 0 {
		t.Fatal("nothing starts before the yes")
	}
	e.press("y")
	if *spawns != 1 {
		t.Fatalf("y goes ahead (spawns=%d)", *spawns)
	}
	e.want("Rate limit exports per tenant.")
}

func TestRNeedsAIAndAFailedWorkerCanBeRetried(t *testing.T) {
	e := newMREnv(t, fake.Free)
	selectTracker(e)
	e.press("R")
	if e.m.nt != nil {
		t.Fatal("R opens nothing with AI off")
	}

	e.enableAIText("auto")
	researchWorkerInline(t, e)
	rec := e.m.researchService().Records
	tr := forge.Ref{Project: grp + "/tracker", IID: 1}
	if err := rec.WriteResearch("f", tr, records.Research{ID: "x", Ref: tr.String(), State: records.ResearchFailed, Error: "gateway down"}); err != nil {
		t.Fatal(err)
	}
	e.press("R")
	e.want("failed", "gateway down", "enter try again")
	e.press("enter")
	e.want("Rate limit exports per tenant.")
}
