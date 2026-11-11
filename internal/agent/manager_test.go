package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/review"
	"github.com/coredev-uk/ade/internal/store"
)

// fakeOMP stands in for the omp binary: it speaks omp's JSON event stream.
// FAKE_OMP picks the scenario; the process works in its cwd like the real one.
const fakeOMP = `#!/bin/sh
cat >/dev/null
write=no
case "$*" in *--auto-approve*) write=yes;; esac
echo '{"type":"session","id":"SESSION-MARKER-7f3a"}'
turn() { printf '{"type":"turn_end","message":{"role":"assistant","model":"fake-model","stopReason":"%s","content":[{"type":"text","text":"%s"}],"usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":%s}}}}\n' "$1" "$2" "$3"; }
case "$FAKE_OMP" in
overspend)
  turn toolUse "thinking" 0.3
  turn toolUse "thinking more" 0.3
  sleep 5
  touch "$FAKE_OMP_SURVIVED"
  turn stop "done" 0.01
  ;;
hang)
  turn toolUse "working" 0.01
  sleep 30
  ;;
fail)
  echo "boom: no credentials" >&2
  exit 1
  ;;
address)
  echo fixed >> hello.txt
  turn stop 'Fixed it.\n\n` + "```json" + `\n{\"replies\":[{\"thread\":\"d1\",\"body\":\"Done: renamed the variable.\"},{\"thread\":\"nope\",\"body\":\"invented\"},{\"thread\":\"d2\",\"body\":\"Kept as is: needed by callers.\"}]}\n` + "```" + `' 0.02
  ;;
*)
  if [ $write = yes ]; then
    echo "hello from the agent" > hello.txt
    turn stop "Created hello.txt" 0.04
  else
    turn stop "# Plan\n\n- add hello.txt with a greeting" 0.01
  fi
  ;;
esac
`

type env struct {
	t     *testing.T
	m     *Manager
	st    *Store
	cache *store.Store
	dir   string // records dir
	wt    string // the item's worktree
	bare  string // origin
	bus   *events.Bus
	evs   <-chan events.Event
	tgt   Target
}

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "omp")
	if err := os.WriteFile(bin, []byte(fakeOMP), 0o755); err != nil {
		t.Fatal(err)
	}
	// origin ← main checkout ← item worktree (what start work leaves behind).
	bare := filepath.Join(tmp, "origin.git")
	sh(t, tmp, "init", "-q", "--bare", "-b", "main", bare)
	main := filepath.Join(tmp, "api")
	sh(t, tmp, "init", "-q", "-b", "main", main)
	sh(t, main, "config", "user.name", "Test User")
	sh(t, main, "config", "user.email", "test@example.com")
	sh(t, main, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(main, "README.md"), []byte("api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, main, "add", ".")
	sh(t, main, "commit", "-q", "-m", "init")
	sh(t, main, "push", "-q", "origin", "main")
	wt := filepath.Join(tmp, "42-version")
	sh(t, main, "worktree", "add", "-q", "-b", "42-version", wt)

	rec := filepath.Join(tmp, "records")
	al, err := audit.Open(rec, "user")
	if err != nil {
		t.Fatal(err)
	}
	cache, err := store.Open(filepath.Join(tmp, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cache.Close() })
	cfg := config.Defaults()
	cfg.AI.Profile = "t"
	cfg.AI.Profiles = map[string]config.AIProfile{"t": {
		Driver: "omp", Command: bin,
		Models: map[string]string{"explain": "fake-model", "plan": "fake-model", "implement": "fake-model"},
	}}
	cfg.Budget.AgentRunUSD = 0.5
	st := NewStore(cache, records.New(rec), rec)
	bus := events.NewBus()
	evs, cancel := bus.Subscribe(64)
	t.Cleanup(cancel)
	m := NewManager(&cfg, st, al, bus)
	m.GitFor = func(context.Context, Run) (git.Client, error) { return git.Client{}, nil }
	m.Reviews = review.NewStore(cache)
	mr := forge.Ref{Project: "g/api", IID: 3, Kind: forge.KindMR}
	return &env{t: t, m: m, st: st, cache: cache, dir: rec, wt: wt, bare: bare, bus: bus, evs: evs, tgt: Target{
		Forge: "gl", Project: "demo", Tracker: forge.Ref{Project: "g/tracker", IID: 42, Kind: forge.KindIssue},
		Repo: "api", Worktree: wt, Branch: "42-version", MR: &mr,
	}}
}

func (e *env) ctx() Context {
	return Context{Issue: forge.Issue{Ref: e.tgt.Tracker, Title: "Add /version endpoint", Description: "Return the build version."}}
}

func (e *env) newRun(kind Kind, c Context) Run {
	e.t.Helper()
	r, _, err := e.m.New(context.Background(), Request{Kind: kind, Target: e.tgt, Context: c})
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) drain() []events.Event {
	var out []events.Event
	for {
		select {
		case ev := <-e.evs:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func (e *env) auditEntries(action string) []audit.Entry {
	e.t.Helper()
	es, err := e.m.Audit.Query(audit.Filter{})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []audit.Entry
	for _, a := range es {
		if a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

func TestLifecyclePlanApproveRunReviewPush(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r := e.newRun(KindPlan, e.ctx())
	if r.State != StateQueued || r.CapUSD != 0.5 {
		t.Fatalf("new run = %+v", r)
	}

	if _, err := e.m.Implement(ctx, r.ID); err == nil || !strings.Contains(err.Error(), "approve") {
		t.Errorf("a run cannot start before a plan exists and is approved: %v", err)
	}
	r, err := e.m.Plan(ctx, r.ID)
	if err != nil || r.State != StateAwaiting {
		t.Fatalf("plan: %v %+v", err, r)
	}
	plan, err := e.m.PlanText(r)
	if err != nil || !strings.Contains(plan, "add hello.txt") {
		t.Errorf("plan.md = %q %v", plan, err)
	}
	if out := sh(t, e.wt, "status", "--porcelain"); out != "" {
		t.Errorf("a plan is read-only but the tree changed: %q", out)
	}
	if _, err := e.m.Implement(ctx, r.ID); err == nil {
		t.Error("an unapproved plan must not run")
	}

	r, err = e.m.Approve(r.ID)
	if err != nil || !r.Approved {
		t.Fatalf("approve: %v", err)
	}
	// Editing the plan after approval voids it.
	planPath := filepath.Join(e.st.Dir(r), FilePlan)
	if err := os.WriteFile(planPath, []byte(plan+"\n- also delete everything\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Implement(ctx, r.ID); err == nil || !strings.Contains(err.Error(), "changed after approval") {
		t.Errorf("edited plan: %v", err)
	}
	if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err = e.m.Implement(ctx, r.ID)
	if err != nil || r.State != StateReview {
		t.Fatalf("implement: %v %+v", err, r)
	}
	d, _, err := e.m.Diff(ctx, r.ID, true)
	if err != nil || len(d.Files) != 1 || d.Files[0].Path != "hello.txt" || d.Files[0].Added != 1 || !strings.Contains(d.Patch, "+hello from the agent") {
		t.Fatalf("diff = %+v %v", d, err)
	}
	// Reviewing must not stage anything for the user.
	if out := sh(t, e.wt, "diff", "--cached", "--name-only"); out != "" {
		t.Errorf("diff touched the index: %q", out)
	}
	if md, err := os.ReadFile(filepath.Join(e.st.Dir(r), FileDiff)); err != nil || !strings.Contains(string(md), "hello.txt") {
		t.Errorf("diff.md = %q %v", md, err)
	}

	r, err = e.m.Push(ctx, r.ID, PushOpts{})
	if err != nil || r.State != StatePushed || len(r.Commits) != 1 {
		t.Fatalf("push: %v %+v", err, r)
	}
	if got := sh(t, e.bare, "log", "-1", "--format=%an <%ae>|%cn|%s", "42-version"); got != "Test User (ade agent) <test@example.com>|Test User|Add /version endpoint" {
		t.Errorf("pushed commit = %q", got)
	}
	if got := sh(t, e.bare, "rev-parse", "42-version"); got != r.Commits[0] {
		t.Errorf("origin has %s, run recorded %s", got, r.Commits[0])
	}
	if _, err := e.m.Push(ctx, r.ID, PushOpts{}); err == nil {
		t.Error("a pushed run cannot be pushed again")
	}

	// Cost: both calls counted, total under the cap, written to cost.json.
	if r.CostUSD < 0.0499 || r.CostUSD > 0.0501 || len(r.Calls) != 2 || r.Calls[0].Kind != KindPlan || r.Calls[1].Kind != KindRun {
		t.Errorf("cost %v calls %+v", r.CostUSD, r.Calls)
	}
	if b, err := os.ReadFile(filepath.Join(e.st.Dir(r), FileCost)); err != nil || !strings.Contains(string(b), `"total_usd": 0.05`) {
		t.Errorf("cost.json = %s %v", b, err)
	}
	// run.md and the transcript are in the run dir.
	for _, f := range []string{fileRunMD, fileRun, FileTranscript, FilePlan, "prompt-plan.md", "prompt-run.md", "context.json"} {
		if _, err := os.Stat(filepath.Join(e.st.Dir(r), f)); err != nil {
			t.Errorf("run dir lacks %s", f)
		}
	}

	// Audit: brackets per call, cost on ai.* entries, push and commit.
	if n := len(e.auditEntries("agent.run.start")); n != 2 {
		t.Errorf("agent.run.start entries = %d, want 2", n)
	}
	ends := e.auditEntries("agent.run.end")
	if len(ends) != 2 || ends[0].Actor != "agent:"+r.ID || ends[0].Outcome != "ok" {
		t.Errorf("agent.run.end = %+v", ends)
	}
	ais := e.auditEntries("ai.agent-plan")
	if len(ais) != 2 || ais[1].AI == nil || ais[1].AI.CostUSD != 0.04 || ais[1].AI.CostSource != "reported" {
		t.Errorf("ai entries (the ledger's feed) = %+v", ais)
	}
	for _, a := range []string{"agent.plan.approve", "git.commit", "git.push"} {
		if len(e.auditEntries(a)) != 1 {
			t.Errorf("audit lacks one %s", a)
		}
	}

	// Events: plan.ready, run.end and pushed are news; running is not.
	seen := map[string]events.Event{}
	for _, ev := range e.drain() {
		seen[ev.Kind] = ev
	}
	for _, k := range []string{EventPlanReady, EventRunEnd, "agent.run.pushed"} {
		if ev, ok := seen[k]; !ok || ev.Transient || ev.Subject != "g/tracker#42" {
			t.Errorf("event %s = %+v (present %v)", k, ev, ok)
		}
	}
	if ev := seen["agent.run.running"]; !ev.Transient {
		t.Errorf("running is status, not news: %+v", ev)
	}
}

// TestTranscriptStaysInTheRunDir is the privacy contract: what the agent said
// is in the run directory and nowhere else ADE keeps data.
func TestTranscriptStaysInTheRunDir(t *testing.T) {
	e := newEnv(t)
	r, err := e.m.Plan(context.Background(), e.newRun(KindPlan, e.ctx()).ID)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "SESSION-MARKER-7f3a"
	tr, err := os.ReadFile(filepath.Join(e.st.Dir(r), FileTranscript))
	if err != nil || !strings.Contains(string(tr), marker) {
		t.Fatalf("transcript lacks the marker: %v", err)
	}
	e.cache.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`) //nolint:errcheck
	var leaks []string
	for _, root := range []string{e.cache.Path, filepath.Join(e.dir, "audit")} {
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error { //nolint:errcheck
			if err != nil || d.IsDir() {
				return nil
			}
			if b, _ := os.ReadFile(p); strings.Contains(string(b), marker) || strings.Contains(string(b), "add hello.txt with a greeting") {
				leaks = append(leaks, p)
			}
			return nil
		})
	}
	// cache.db's run row carries run metadata only.
	var body string
	if err := e.cache.DB.QueryRow(`SELECT body FROM agent_runs WHERE id = ?`, r.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, marker) || strings.Contains(body, "greeting") {
		t.Errorf("cache row holds transcript content: %s", body)
	}
	if len(leaks) > 0 {
		t.Errorf("transcript content found outside the run dir: %v", leaks)
	}
}

func TestCostCapKillsTheProcess(t *testing.T) {
	e := newEnv(t)
	survived := filepath.Join(t.TempDir(), "survived")
	t.Setenv("FAKE_OMP", "overspend")
	t.Setenv("FAKE_OMP_SURVIVED", survived)
	r := e.newRun(KindPlan, e.ctx())
	start := time.Now()
	r, err := e.m.Plan(context.Background(), r.ID)
	if _, ok := ai.AsStop(err); !ok {
		t.Fatalf("err = %v, want a budget stop", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("the hard stop took %v: the process was waited out, not killed", time.Since(start))
	}
	if r.State != StateFailed || !strings.Contains(r.Error, "cost cap") {
		t.Errorf("run = %s %q", r.State, r.Error)
	}
	if r.CostUSD < 0.59 || r.Calls[0].StoppedBy != StopCostCap {
		t.Errorf("cost %v stopped_by %q: the spend up to the kill must be kept", r.CostUSD, r.Calls[0].StoppedBy)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(survived); err == nil {
		t.Error("the agent process outlived the kill")
	}
	if len(e.auditEntries("budget.stop")) != 1 {
		t.Error("a cap stop must leave a budget.stop audit entry")
	}
	var failedEv bool
	for _, ev := range e.drain() {
		if ev.Kind == EventRunFailed && ev.Severity == events.Error {
			failedEv = true
		}
	}
	if !failedEv {
		t.Error("a failed run must raise an error event (pop-up)")
	}
	// The cost reaches cost.json and the ledger's feed even for a killed run.
	if ais := e.auditEntries("ai.agent-plan"); len(ais) != 1 || ais[0].AI.CostUSD < 0.59 || ais[0].Outcome != "error" {
		t.Errorf("ai entry = %+v", ais)
	}
}

func TestAgentFailureSurfacesStderr(t *testing.T) {
	e := newEnv(t)
	t.Setenv("FAKE_OMP", "fail")
	r, err := e.m.Plan(context.Background(), e.newRun(KindPlan, e.ctx()).ID)
	if err == nil || r.State != StateFailed || !strings.Contains(r.Error, "boom: no credentials") {
		t.Errorf("state %s err %v run.Error %q", r.State, err, r.Error)
	}
	if _, err := e.m.Approve(r.ID); err == nil {
		t.Error("a failed run cannot be approved")
	}
}

func TestCancelKillsARunningAgent(t *testing.T) {
	e := newEnv(t)
	t.Setenv("FAKE_OMP", "hang")
	r := e.newRun(KindPlan, e.ctx())
	done := make(chan error, 1)
	var got Run
	go func() {
		var err error
		got, err = e.m.Plan(context.Background(), r.ID)
		done <- err
	}()
	var pid int
	for range 100 {
		if cur, _ := e.st.Get(r.ID); cur.PID > 0 {
			pid = cur.PID
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the run never recorded its process id")
	}
	cancelled, err := e.m.Cancel(r.ID)
	if err != nil || cancelled.State != StateCancelled {
		t.Fatalf("cancel: %v %+v", err, cancelled)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrCancelled) || got.State != StateCancelled {
			t.Errorf("plan returned %v with state %s", err, got.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Plan did not return after cancel")
	}
	for range 40 {
		if !Alive(pid) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if Alive(pid) {
		t.Error("the agent process is still alive")
	}
	if cur, _ := e.st.Get(r.ID); cur.State != StateCancelled {
		t.Errorf("the finished call must not overwrite the cancellation: %s", cur.State)
	}
	if _, err := e.m.Cancel(r.ID); err == nil {
		t.Error("cancelling a finished run must say so")
	}
}

func TestContextCancelCancelsTheRun(t *testing.T) {
	e := newEnv(t)
	t.Setenv("FAKE_OMP", "hang")
	r := e.newRun(KindPlan, e.ctx())
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	got, err := e.m.Plan(ctx, r.ID)
	if !errors.Is(err, ErrCancelled) || got.State != StateCancelled {
		t.Errorf("Ctrl-C: err %v state %s", err, got.State)
	}
}

func TestWorktreeGuards(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// A plain checkout is not a start-work worktree.
	bad := e.tgt
	bad.Worktree = filepath.Dir(e.wt) + "/api"
	if _, _, err := e.m.New(ctx, Request{Kind: KindPlan, Target: bad, Context: e.ctx()}); err == nil || !strings.Contains(err.Error(), "not a start-work worktree") {
		t.Errorf("checkout: %v", err)
	}
	wrongBranch := e.tgt
	wrongBranch.Branch = "other"
	if _, _, err := e.m.New(ctx, Request{Kind: KindPlan, Target: wrongBranch, Context: e.ctx()}); err == nil || !strings.Contains(err.Error(), "not the item's branch") {
		t.Errorf("wrong branch: %v", err)
	}
	// A dirty tree would mix the user's edits into the agent's diff.
	r := e.newRun(KindPlan, e.ctx())
	r, _ = e.m.Plan(ctx, r.ID)
	r, _ = e.m.Approve(r.ID)
	if err := os.WriteFile(filepath.Join(e.wt, "wip.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Implement(ctx, r.ID); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("dirty tree: %v", err)
	}
	if cur, _ := e.st.Get(r.ID); cur.State != StateAwaiting {
		t.Errorf("a refused run must stay awaiting approval, is %s", cur.State)
	}
}

func TestNoChangesFailsTheRun(t *testing.T) {
	e := newEnv(t)
	t.Setenv("FAKE_OMP", "") // plan-style output even with --auto-approve? no: force a no-op writer
	// A writer that edits nothing: replace the binary with one that only talks.
	p := e.m.Cfg.AI.Profiles["t"]
	talk := filepath.Join(t.TempDir(), "omp")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"type\":\"turn_end\",\"message\":{\"role\":\"assistant\",\"stopReason\":\"stop\",\"content\":[{\"type\":\"text\",\"text\":\"nothing to do\"}],\"usage\":{\"cost\":{\"total\":0.001}}}}'\n"
	if err := os.WriteFile(talk, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p.Command = talk
	e.m.Cfg.AI.Profiles["t"] = p
	ctx := context.Background()
	r := e.newRun(KindPlan, e.ctx())
	r, _ = e.m.Plan(ctx, r.ID)
	r, _ = e.m.Approve(r.ID)
	r, err := e.m.Implement(ctx, r.ID)
	if !errors.Is(err, errNoChanges) || r.State != StateFailed {
		t.Errorf("err %v state %s", err, r.State)
	}
}

func TestAddressDraftsRepliesNeverPosts(t *testing.T) {
	e := newEnv(t)
	t.Setenv("FAKE_OMP", "address")
	c := e.ctx()
	c.Threads = []Thread{
		{ID: "d1", Position: &forge.Position{Path: "main.go", NewLine: 12}, Notes: []forge.Note{{Body: "rename this"}}},
		{ID: "d2", Notes: []forge.Note{{Body: "why not inline?\nlong explanation"}}},
	}
	r := e.newRun(KindAddress, c)
	r, err := e.m.Address(context.Background(), r.ID)
	if err != nil || r.State != StateReview || r.Drafts != 2 {
		t.Fatalf("address: %v %+v", err, r)
	}
	ds, err := e.m.Reviews.List(*e.tgt.MR)
	if err != nil || len(ds) != 2 {
		t.Fatalf("drafts = %+v %v", ds, err)
	}
	by := map[string]review.Draft{}
	for _, d := range ds {
		by[d.Body] = d
	}
	line := by["Done: renamed the variable."]
	if line.Path != "main.go" || line.Line != 12 || line.Source != review.SourceAgent || line.State != review.StatePending {
		t.Errorf("a thread's reply anchors at the thread's line, pending: %+v", line)
	}
	overview, ok := by["Re: why not inline?\n\nKept as is: needed by callers."]
	if !ok || !overview.Overview() || overview.Selected() {
		t.Errorf("a thread without a position becomes a pending overview draft naming the thread: %+v", ds)
	}
	for _, d := range ds {
		if d.Selected() || d.Published || d.RemoteID != 0 {
			t.Errorf("agent drafts need triage before submit: %+v", d)
		}
	}
	if d, _, err := e.m.Diff(context.Background(), r.ID, false); err != nil || len(d.Files) != 1 || d.Files[0].Path != "hello.txt" {
		t.Errorf("the code change is in the diff: %+v %v", d, err)
	}
}

func TestAddressNeedsMRAndThreads(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, _, err := e.m.New(ctx, Request{Kind: KindAddress, Target: e.tgt, Context: e.ctx()}); err == nil || !strings.Contains(err.Error(), "no unresolved threads") {
		t.Errorf("no threads: %v", err)
	}
	noMR := e.tgt
	noMR.MR = nil
	if _, _, err := e.m.New(ctx, Request{Kind: KindAddress, Target: noMR, Context: e.ctx()}); err == nil || !strings.Contains(err.Error(), "merge request") {
		t.Errorf("no MR: %v", err)
	}
}

func TestPreflight(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// Driver binary missing: unavailable, before any run exists.
	p := e.m.Cfg.AI.Profiles["t"]
	good := p.Command
	p.Command = "/nonexistent/omp"
	e.m.Cfg.AI.Profiles["t"] = p
	if _, _, err := e.m.New(ctx, Request{Kind: KindPlan, Target: e.tgt, Context: e.ctx()}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("missing binary: %v", err)
	}
	if rs, _ := e.st.List(); len(rs) != 0 {
		t.Errorf("a refused run must not be created: %+v", rs)
	}
	p.Command = good
	// codex reports no cost: without a price the cap could not be enforced.
	p.Agent, p.AgentCommand = "codex", good
	e.m.Cfg.AI.Profiles["t"] = p
	if _, _, err := e.m.New(ctx, Request{Kind: KindPlan, Target: e.tgt, Context: e.ctx()}); err == nil || !strings.Contains(err.Error(), "no price is known") {
		t.Errorf("unpriced codex: %v", err)
	}
	p.Agent, p.AgentCommand = "", ""
	e.m.Cfg.AI.Profiles["t"] = p
	// ai.mode off.
	e.m.Cfg.AI.Mode = "off"
	if _, _, err := e.m.New(ctx, Request{Kind: KindPlan, Target: e.tgt, Context: e.ctx()}); !errors.Is(err, ai.ErrDisabled) {
		t.Errorf("ai off: %v", err)
	}
	e.m.Cfg.AI.Mode = "ask"
	// The flag lowers the cap, never raises it.
	if got := e.m.capFor(0.1); got != 0.1 {
		t.Errorf("cap with --max-usd 0.1 = %v", got)
	}
	if got := e.m.capFor(9); got != 0.5 {
		t.Errorf("cap with --max-usd 9 = %v, want the configured 0.5", got)
	}
}

func TestStoreRebuildsFromRunDirs(t *testing.T) {
	e := newEnv(t)
	r := e.newRun(KindPlan, e.ctx())
	if _, err := e.cache.DB.Exec(`DELETE FROM agent_runs`); err != nil {
		t.Fatal(err)
	}
	rs, err := e.st.List()
	if err != nil || len(rs) != 1 || rs[0].ID != r.ID {
		t.Errorf("after losing cache.db the runs come back from the record dirs: %+v %v", rs, err)
	}
	if got, err := e.st.Get(r.ID[len(r.ID)-3:]); err != nil || got.ID != r.ID {
		t.Errorf("get by suffix: %v %v", got.ID, err)
	}
}
