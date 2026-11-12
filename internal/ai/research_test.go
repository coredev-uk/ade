package ai

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/ledger"
	"github.com/coredev-uk/ade/internal/records"
)

var researchRef = forge.Ref{Project: "grp/tracker", IID: 15, Kind: forge.KindIssue}

func researchInput() ResearchInput {
	three := 3
	return ResearchInput{
		Issue: forge.Issue{
			Ref: researchRef, Title: "Rate limit the export API", Labels: []string{"type::feature"},
			Description: "Exports hammer the db. Parent: grp/tracker#9", State: "opened", Comments: &three,
		},
		Comments: []forge.Discussion{{Notes: []forge.Note{
			{Author: forge.User{Username: "rae"}, Body: "Per tenant or global?"},
			{Author: forge.User{Username: "bot"}, Body: "assigned to someone", System: true},
		}}},
		Links: []ResearchLink{{Type: "blocks", Ref: forge.Ref{Project: "grp/api", IID: 4, Kind: forge.KindIssue}, Title: "Add token bucket", State: "opened"}},
		MRs:   []forge.MR{{Ref: forge.Ref{Project: "grp/api", IID: 7, Kind: forge.KindMR}, Title: "Add limiter", SourceBranch: "limiter", TargetBranch: "main", State: "opened"}},
	}
}

// inlineWorker replaces the detached spawn with the worker run in-process, the
// way `ade ai research --worker` runs it, and counts the spawns.
func inlineWorker(t *testing.T, e *env, in ResearchInput) *int {
	t.Helper()
	spawns := 0
	old := ResearchSpawn
	t.Cleanup(func() { ResearchSpawn = old })
	ResearchSpawn = func(sp ResearchSpawnSpec) (int, error) {
		spawns++
		job := ResearchJob(sp.Forge, in)
		_, _ = e.svc.RunResearch(context.Background(), sp.Forge, sp.JobID, job, ResearchOpts{Profile: sp.Profile, Refresh: sp.Refresh, Override: sp.Override, Assume: true, Notify: true})
		return os.Getpid(), nil
	}
	return &spawns
}

func TestResearchPromptHoldsTheIssueItsDiscussionLinksAndMRsButNoSystemNotes(t *testing.T) {
	j := ResearchJob("sandbox", researchInput())
	for _, want := range []string{"Rate limit the export API", "type::feature", "rae: Per tenant or global?", "blocks grp/api#4: Add token bucket [opened]", "grp/api!7 Add limiter (limiter -> main)"} {
		if !strings.Contains(j.Prompt, want) {
			t.Errorf("prompt is missing %q:\n%s", want, j.Prompt)
		}
	}
	if strings.Contains(j.Prompt, "assigned to someone") {
		t.Error("system notes are not discussion")
	}
	if j.Feature != "research" || j.Role != RolePlan || j.Ref != researchRef {
		t.Errorf("job = %+v", j)
	}
}

func TestDetachedResearchIsStoredInTheRecordAndNeverBoughtTwice(t *testing.T) {
	e := newEnv(t)
	in := researchInput()
	spawns := inlineWorker(t, e, in)
	ctx := context.Background()

	st, err := e.svc.StartResearch(ctx, "sandbox", ResearchJob("sandbox", in), ResearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Result != nil || *spawns != 1 || e.drv.calls != 1 {
		t.Fatalf("first start should spawn the worker once and pay once: result=%v spawns=%d calls=%d", st.Result, *spawns, e.drv.calls)
	}
	if st.Preflight.Model == "" {
		t.Error("the pre-flight is returned to be shown before the spawn")
	}
	v, err := e.svc.ReadResearch("sandbox", researchRef)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != records.ResearchDone || v.Artifact == "" || !strings.Contains(v.Text, "rate limiter") || v.CostUSD != 0.002 {
		t.Fatalf("stored research = %+v", v)
	}
	if _, err := os.Stat(e.svc.Records.ResearchPath("sandbox", researchRef)); err != nil {
		t.Fatalf("the job state belongs in the item record: %v", err)
	}

	// Asking again about the unchanged issue: answered from the cache, no
	// worker, no second call, and the job is filed as a $0 hit.
	again, err := e.svc.StartResearch(ctx, "sandbox", ResearchJob("sandbox", in), ResearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Result == nil || !again.Result.CacheHit || *spawns != 1 || e.drv.calls != 1 {
		t.Fatalf("second start must be a cache hit: result=%+v spawns=%d calls=%d", again.Result, *spawns, e.drv.calls)
	}
	v, _ = e.svc.ReadResearch("sandbox", researchRef)
	if !v.CacheHit || v.State != records.ResearchDone || v.Text == "" {
		t.Fatalf("stored research after the hit = %+v", v)
	}
	if got, _ := e.svc.Ledger.Spend("", time.Time{}, time.Time{}); got != 0.002 {
		t.Fatalf("ledger spend = %v: the second ask must add nothing", got)
	}
	es := e.entries("ai.research")
	if len(es) != 2 || es[0].AI.CacheHit || !es[1].AI.CacheHit {
		t.Fatalf("audit: %+v", es)
	}

	// --refresh buys a new answer.
	if _, err := e.svc.StartResearch(ctx, "sandbox", ResearchJob("sandbox", in), ResearchOpts{Refresh: true}); err != nil {
		t.Fatal(err)
	}
	if *spawns != 2 || e.drv.calls != 2 {
		t.Fatalf("refresh should spawn and pay again: spawns=%d calls=%d", *spawns, e.drv.calls)
	}
}

func TestResearchRefusedByACapStartsNothing(t *testing.T) {
	e := newEnv(t)
	e.conf.Budget.DailyUSD = 0.01
	e.log.Append(auditSpend(0.0099))
	spawns := inlineWorker(t, e, researchInput())

	_, err := e.svc.StartResearch(context.Background(), "sandbox", ResearchJob("sandbox", researchInput()), ResearchOpts{})
	if stop, ok := AsStop(err); !ok || !strings.Contains(stop.Message, "daily cap reached") {
		t.Fatalf("want the daily-cap stop, got %v", err)
	}
	if *spawns != 0 || e.drv.calls != 0 {
		t.Fatalf("a refused job must not start a worker (spawns=%d calls=%d)", *spawns, e.drv.calls)
	}
	if _, err := e.svc.ReadResearch("sandbox", researchRef); !errors.Is(err, records.ErrNoResearch) {
		t.Fatalf("a refused job leaves no job state: %v", err)
	}
	if len(e.entries(ledger.StopAction)) != 1 {
		t.Fatal("the refusal is audited as budget.stop")
	}

	// Override lets it through; the worker audits the override.
	if _, err := e.svc.StartResearch(context.Background(), "sandbox", ResearchJob("sandbox", researchInput()), ResearchOpts{Override: true}); err != nil {
		t.Fatal(err)
	}
	if *spawns != 1 || len(e.entries("budget.override")) != 1 {
		t.Fatalf("override: spawns=%d overrides=%d", *spawns, len(e.entries("budget.override")))
	}
}

func TestResearchThatNeedsAYesWaitsForIt(t *testing.T) {
	e := newEnv(t)
	e.conf.AI.Mode = "ask"
	e.conf.Budget.ConfirmOverUSD = 0.0000001
	spawns := inlineWorker(t, e, researchInput())
	job := func() Job { return ResearchJob("sandbox", researchInput()) }

	_, err := e.svc.StartResearch(context.Background(), "sandbox", job(), ResearchOpts{})
	var nc *ErrNeedsConfirm
	if !errors.As(err, &nc) || *spawns != 0 {
		t.Fatalf("want ErrNeedsConfirm and no worker, got %v (spawns %d)", err, *spawns)
	}
	declined := func(Preflight) (bool, error) { return false, nil }
	if _, err := e.svc.StartResearch(context.Background(), "sandbox", job(), ResearchOpts{Confirm: declined}); !errors.Is(err, ErrCancelled) || *spawns != 0 {
		t.Fatalf("a declined pre-flight must cancel: %v", err)
	}
	if _, err := e.svc.StartResearch(context.Background(), "sandbox", job(), ResearchOpts{Assume: true}); err != nil || *spawns != 1 {
		t.Fatalf("assume goes ahead: %v (spawns %d)", err, *spawns)
	}
}

func TestResearchWithAIOffIsRefusedCleanly(t *testing.T) {
	e := newEnv(t)
	e.conf.AI.Mode = "off"
	spawns := inlineWorker(t, e, researchInput())
	_, err := e.svc.StartResearch(context.Background(), "sandbox", ResearchJob("sandbox", researchInput()), ResearchOpts{})
	if !errors.Is(err, ErrDisabled) || *spawns != 0 || e.drv.calls != 0 {
		t.Fatalf("err = %v spawns=%d calls=%d", err, *spawns, e.drv.calls)
	}
}

func TestResearchWorkerFailureIsStoredAndTheNextStartIsAllowed(t *testing.T) {
	e := newEnv(t)
	e.drv.err = errors.New("gateway down")
	in := researchInput()
	inlineWorker(t, e, in)
	if _, err := e.svc.StartResearch(context.Background(), "sandbox", ResearchJob("sandbox", in), ResearchOpts{}); err != nil {
		t.Fatalf("starting succeeds, the worker fails later: %v", err)
	}
	v, err := e.svc.ReadResearch("sandbox", researchRef)
	if err != nil || v.State != records.ResearchFailed || !strings.Contains(v.Error, "gateway down") {
		t.Fatalf("stored = %+v err=%v", v, err)
	}
	e.drv.err = nil
	if _, err := e.svc.StartResearch(context.Background(), "sandbox", ResearchJob("sandbox", in), ResearchOpts{}); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.svc.ReadResearch("sandbox", researchRef); v.State != records.ResearchDone {
		t.Fatalf("a retry after a failure runs: %+v", v)
	}
}

func TestRunningResearchBlocksAnotherUntilItsWorkerIsGone(t *testing.T) {
	e := newEnv(t)
	in := researchInput()
	spawns := inlineWorker(t, e, in)
	running := records.Research{ID: "j1", Ref: researchRef.String(), State: records.ResearchRunning, PID: os.Getpid(), Started: time.Now()}
	if err := e.svc.Records.WriteResearch("sandbox", researchRef, running); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StartResearch(context.Background(), "sandbox", ResearchJob("sandbox", in), ResearchOpts{}); !errors.Is(err, ErrResearchRunning) || *spawns != 0 {
		t.Fatalf("a live worker blocks a second job: %v", err)
	}

	// A worker that died is reported as failed, and a new job may start.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Skip("no true(1)")
	}
	running.PID = dead.Process.Pid
	if err := e.svc.Records.WriteResearch("sandbox", researchRef, running); err != nil {
		t.Fatal(err)
	}
	v, _ := e.svc.ReadResearch("sandbox", researchRef)
	if v.State != records.ResearchFailed || !strings.Contains(v.Error, "ended without writing a result") {
		t.Fatalf("a dead worker should read as failed: %+v", v)
	}
	if _, err := e.svc.StartResearch(context.Background(), "sandbox", ResearchJob("sandbox", in), ResearchOpts{}); err != nil || *spawns != 1 {
		t.Fatalf("restart after a dead worker: %v (spawns %d)", err, *spawns)
	}
}

func TestResearchNotesAreInThePromptAndTheCacheKey(t *testing.T) {
	plain := ResearchJob("sandbox", researchInput())
	noted := WithNotes(plain, "- 2026-11-12 10:00 the db is the bottleneck")
	if !noted.Notes || !strings.Contains(noted.Prompt, "the db is the bottleneck") {
		t.Fatalf("notes missing: %+v", noted)
	}
	if InputHash(plain.PromptVersion, "plan", "m", plain.System, plain.Prompt) == InputHash(noted.PromptVersion, "plan", "m", noted.System, noted.Prompt) {
		t.Error("notes must change the cache key")
	}
}
