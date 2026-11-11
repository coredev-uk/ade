package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/ledger"
	"github.com/coredev-uk/ade/internal/review"
)

// ompTurn is the one JSON event ADE reads from `omp -p --mode json`.
func ompTurn(t *testing.T, text string, cost float64) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "turn_end", "message": map[string]any{
		"role": "assistant", "model": "claude-haiku-4-5", "stopReason": "stop",
		"content": []map[string]string{{"type": "text", "text": text}},
		"usage":   map[string]any{"input": 1000, "output": 100, "cost": map[string]float64{"total": cost}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// aiEnvFor wires the fake-GitLab CLI env to a stub `omp` that answers review
// prompts with reviewJSON and everything else with a fixed summary. calls
// returns how many times the stub ran.
func aiEnvFor(t *testing.T, reviewJSON string) (e *cliEnv, calls func() int) {
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
	summary := write("summary.jsonl", ompTurn(t, "- adds a token bucket\nRisk: low", 0.002), 0o600)
	rev := write("review.jsonl", ompTurn(t, reviewJSON, 0.01), 0o600)
	count := filepath.Join(dir, "count")
	stub := write("omp", "#!/bin/sh\necho x >> "+count+"\ncat >/dev/null\ncase \"$*\" in\n*'review a merge request diff'*) cat "+rev+";;\n*) cat "+summary+";;\nesac\n", 0o700)

	e.app.RecordsDir = t.TempDir()
	e.app.Config.AI.Mode, e.app.Config.AI.Profile = "auto", "sub"
	e.app.Config.AI.Profiles["sub"] = config.AIProfile{
		Driver: "omp", Command: stub,
		Models: map[string]string{"explain": "claude-haiku-4-5", "review": "claude-haiku-4-5"},
		Prices: map[string]config.Price{"claude-haiku-4-5": {Input: 1, Output: 5}},
	}
	return e, func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}
}

// ai runs `ade ai …` with a non-terminal stdin, as a script would.
func (e *cliEnv) ai(args ...string) (string, error) {
	e.t.Helper()
	cmd := newAICmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, e.app))
	return out.String(), err
}

func (e *cliEnv) mustAI(args ...string) string {
	e.t.Helper()
	out, err := e.ai(args...)
	if err != nil {
		e.t.Fatalf("ade ai %v: %v\n%s", args, err, out)
	}
	return out
}

func TestAISummarizeSecondRunIsAFreeCacheHit(t *testing.T) {
	e, calls := aiEnvFor(t, "[]")
	out := e.mustAI("summarize", renameMR)
	if !strings.Contains(out, "adds a token bucket") || !strings.Contains(out, "— $0.0020 · sub") {
		t.Fatalf("first run:\n%s", out)
	}
	out = e.mustAI("summarize", renameMR)
	if !strings.Contains(out, "cached · $0.00") || calls() != 1 {
		t.Fatalf("second run must be a cache hit without a call (calls=%d):\n%s", calls(), out)
	}
	asJSON(t)
	var res aiOut
	if err := json.Unmarshal([]byte(e.mustAI("summarize", renameMR)), &res); err != nil {
		t.Fatal(err)
	}
	if !res.CacheHit || res.CostUSD != 0 || res.SavedUSD != 0.002 || res.Artifact == "" {
		t.Fatalf("json = %+v", res)
	}
	// The artifact is in the MR's item record, and both hits are audited at $0.
	if _, err := os.Stat(filepath.Join(e.app.RecordsDir, filepath.FromSlash(res.Artifact))); err != nil {
		t.Fatal(err)
	}
	es := e.audited("ai.summarize")
	if len(es) != 3 || es[0].AI.CacheHit || !es[1].AI.CacheHit || !es[2].AI.CacheHit || es[1].AI.CostUSD != 0 {
		t.Fatalf("audit: %+v", es)
	}
}

func TestAIDryRunSendsNothing(t *testing.T) {
	e, calls := aiEnvFor(t, "[]")
	out := e.mustAI("summarize", renameMR, "--dry-run")
	if !strings.Contains(out, "dry run · not cached") || !strings.Contains(out, "est $") || calls() != 0 {
		t.Fatalf("calls=%d\n%s", calls(), out)
	}
	if len(e.audited("ai.*")) != 0 {
		t.Fatal("a dry run leaves no ai audit entry")
	}
}

func TestAICapBreachBlocksWithAClearMessageAndExitCode(t *testing.T) {
	e, calls := aiEnvFor(t, "[]")
	e.app.Config.Budget.DailyUSD = 0.01
	e.app.Audit.Append(audit.Entry{Action: "ai.summarize", AI: &audit.AIInfo{Profile: "sub", Model: "m", CostUSD: 0.0099, CostSource: "reported"}})
	out, err := e.ai("summarize", renameMR)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitBudgetStop {
		t.Fatalf("want exit %d, got %v\n%s", exitBudgetStop, err, out)
	}
	if !strings.Contains(err.Error(), "daily cap reached") || !strings.Contains(err.Error(), "budget.daily_usd") || calls() != 0 {
		t.Fatalf("message = %v (calls %d)", err, calls())
	}
	if len(e.audited("budget.stop")) != 1 {
		t.Fatal("no budget.stop audit entry")
	}
	// --override-budget goes through and is audited.
	e.mustAI("summarize", renameMR, "--override-budget")
	if len(e.audited("budget.override")) != 1 || calls() != 1 {
		t.Fatal("override not honoured / audited")
	}
}

func TestAIModeOffRefusesWithoutTouchingAnythingElse(t *testing.T) {
	e, calls := aiEnvFor(t, "[]")
	e.app.Config.AI.Mode = "off"
	if _, err := e.ai("summarize", renameMR); err == nil || !strings.Contains(err.Error(), "ai.mode") || calls() != 0 {
		t.Fatalf("err = %v", err)
	}
	// Non-AI commands keep working: reading the MR needs no AI.
	if out := e.mustMR("show", renameMR); !strings.Contains(out, renameMR) {
		t.Fatalf("mr show: %s", out)
	}
}

func TestAIConfirmationNeededWithoutAYes(t *testing.T) {
	e, calls := aiEnvFor(t, "[]")
	e.app.Config.AI.Mode = "ask"
	e.app.Config.Budget.ConfirmOverUSD = 0.0000001
	out, err := e.ai("summarize", renameMR) // stdin is not a terminal under test
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitNeedsConfirm || calls() != 0 {
		t.Fatalf("err = %v calls=%d\n%s", err, calls(), out)
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("message must say how to proceed: %v", err)
	}
	e.mustAI("summarize", renameMR, "--yes")
	if calls() != 1 {
		t.Fatal("--yes did not go through")
	}
}

const reviewAnswer = `[
 {"path":"limit/token_bucket.go","line":10,"side":"new","severity":"blocking","body":"Allow never consumes a token: concurrent callers all pass. Decrement under a lock."},
 {"path":"limit/token_bucket.go","line":7,"side":"new","severity":"suggestion","body":"Tokens as float64 drifts; count milli-tokens in an int64."},
 {"path":"limit/token_bucket.go","line":3,"side":"new","severity":"question","body":"Nothing refills the bucket, so what does continuously mean?"},
 {"path":"limit/token_bucket.go","line":99,"side":"new","severity":"nit","body":"This line is not in the diff."}
]`

func TestAIReviewDraftsTriageAndOneBatchPostsOnlyWhatWasAccepted(t *testing.T) {
	e, _ := aiEnvFor(t, reviewAnswer)
	out := e.mustAI("review", renameMR)
	if !strings.Contains(out, "3 comments saved as pending agent drafts") || !strings.Contains(out, "1 rejected") || !strings.Contains(out, "outside the diff hunks") {
		t.Fatalf("review output:\n%s", out)
	}
	asJSON(t)
	var drafts []review.Draft
	if err := json.Unmarshal([]byte(e.mustAI("triage", renameMR)), &drafts); err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 3 {
		t.Fatalf("drafts = %+v", drafts)
	}
	byLine := map[int]review.Draft{}
	for _, d := range drafts {
		if d.Source != review.SourceAgent || d.State != review.StatePending {
			t.Fatalf("agent drafts start pending: %+v", d)
		}
		byLine[d.Line] = d
	}
	jsonOut = false

	// Pending drafts are never sent: submitting now has nothing to send.
	if _, err := e.review("submit", renameMR); err == nil || !strings.Contains(err.Error(), "nothing to submit") {
		t.Fatalf("pending agent drafts must not be submitted: %v", err)
	}

	e.mustAI("triage", "accept", renameMR, byLine[10].ID)
	e.mustAI("triage", "deny", renameMR, byLine[7].ID, "--reason", "noisy")
	e.mustAI("triage", "edit", renameMR, byLine[3].ID, "--body", "Agreed, the doc line overpromises; drop 'continuously'.")
	if _, err := e.ai("triage", "accept", renameMR, "nonexistent"); err == nil {
		t.Fatal("an unknown draft id must be an error")
	}
	// Your own drafts are not the agent's to triage.
	e.mustReview("add", renameMR, "--path", "limit/token_bucket.go", "--line", "1", "--body", "My own note.")

	out = e.mustReview("submit", renameMR)
	c := gitlab.New(e.srv.URL, fake.Token)
	ds, _ := c.Discussions(context.Background(), ref(renameMR))
	var posted []string
	for _, d := range ds {
		for _, n := range d.Notes {
			posted = append(posted, n.Body)
		}
	}
	all := strings.Join(posted, "\n")
	for _, want := range []string{"Allow never consumes a token", "Agreed, the doc line overpromises", "My own note."} {
		if !strings.Contains(all, want) {
			t.Errorf("forge lacks %q after submit:\n%s", want, all)
		}
	}
	for _, never := range []string{"float64 drifts", "Nothing refills the bucket"} {
		if strings.Contains(all, never) {
			t.Errorf("%q must never be posted (denied, or replaced by its edit)", never)
		}
	}
	if n, _ := e.srv.Publishes(ref(renameMR)); n != 1 {
		t.Errorf("accepted drafts must arrive as one batch: %d publishes\n%s", n, out)
	}

	// Every decision is audited, and the rates attribute them to the profile.
	for _, a := range []string{"review.comment.accept", "review.comment.deny", "review.comment.edit", "review.comment.draft"} {
		if len(e.audited(a)) == 0 {
			t.Errorf("no %s audit entry", a)
		}
	}
	rates, err := ledger.ReviewRates(e.app.Audit, time.Time{})
	if err != nil || len(rates) != 1 {
		t.Fatalf("%v %v", rates, err)
	}
	if r := rates[0]; r.Profile != "sub" || r.Drafts != 3 || r.Accepted != 1 || r.Denied != 1 || r.Edited != 1 {
		t.Fatalf("rates = %+v", r)
	}
}

func TestAIReviewSecondPassOverTheSameMRCostsNothingAndAddsNoDuplicates(t *testing.T) {
	e, calls := aiEnvFor(t, reviewAnswer)
	e.mustAI("review", renameMR)
	out := e.mustAI("review", renameMR)
	if calls() != 1 || !strings.Contains(out, "cached · $0.00") || !strings.Contains(out, "0 comments saved") {
		t.Fatalf("calls=%d:\n%s", calls(), out)
	}
}

func TestAIDeniedDraftsStayDeniedAndAcceptAllTakesOnlyPending(t *testing.T) {
	e, _ := aiEnvFor(t, reviewAnswer)
	e.mustAI("review", renameMR)
	asJSON(t)
	var drafts []review.Draft
	json.Unmarshal([]byte(e.mustAI("triage", renameMR)), &drafts) //nolint:errcheck // asserted below
	jsonOut = false
	if len(drafts) != 3 {
		t.Fatal(drafts)
	}
	e.mustAI("triage", "deny", renameMR, drafts[0].ID)
	e.mustAI("triage", "accept", renameMR, "--all")
	jsonOut = true
	json.Unmarshal([]byte(e.mustAI("triage", renameMR)), &drafts) //nolint:errcheck
	jsonOut = false
	states := map[string]int{}
	for _, d := range drafts {
		states[string(d.State)]++
	}
	if states["denied"] != 1 || states["accepted"] != 2 {
		t.Fatalf("--all must not resurrect a denied draft: %v", states)
	}
	e.mustReview("submit", renameMR)
	c := gitlab.New(e.srv.URL, fake.Token)
	ds, _ := c.Discussions(context.Background(), ref(renameMR))
	var buf bytes.Buffer
	for _, d := range ds {
		for _, n := range d.Notes {
			buf.WriteString(n.Body + "\n")
		}
	}
	if strings.Contains(buf.String(), drafts[0].Body) {
		t.Fatal("a denied draft was posted")
	}
}

func TestPickDiscussionByIDOrPrefix(t *testing.T) {
	ds := []forge.Discussion{
		{ID: "abc123", Notes: []forge.Note{{Body: "first thread"}}},
		{ID: "abd999", Notes: []forge.Note{{Body: "second"}}},
	}
	if d, err := pickDiscussion(ds, "abc123"); err != nil || d.ID != "abc123" {
		t.Fatalf("%v %v", d, err)
	}
	if d, err := pickDiscussion(ds, "abd"); err != nil || d.ID != "abd999" {
		t.Fatalf("%v %v", d, err)
	}
	if _, err := pickDiscussion(ds, "ab"); err == nil || !strings.Contains(err.Error(), "several") {
		t.Fatalf("ambiguous prefix: %v", err)
	}
	if _, err := pickDiscussion(ds, "zzz"); err == nil || !strings.Contains(err.Error(), "first thread") {
		t.Fatalf("no match lists the threads: %v", err)
	}
}

func classifyEnv(t *testing.T) *cliEnv {
	t.Helper()
	e, _ := aiEnvFor(t, "[]")
	manifest, err := os.ReadFile("../../scripts/sandbox/manifests/gitlab.ade.toml")
	if err != nil {
		t.Fatal(err)
	}
	mpath := filepath.Join(t.TempDir(), "demo.ade.toml")
	if err := os.WriteFile(mpath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.Config.Forges["sandbox-gitlab"] = config.Forge{Kind: "gitlab", URL: e.srv.URL}
	e.app.Config.Projects = []config.ProjectRef{{Source: mpath}}
	return e
}

func issueLabels(t *testing.T, e *cliEnv, r string) string {
	t.Helper()
	is, err := gitlab.New(e.srv.URL, fake.Token).Issue(context.Background(), ref(r))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(is.Labels, ",")
}

func TestAIClassifySuggestsWithoutWritingAndApplyNeedsAConfirmation(t *testing.T) {
	e := classifyEnv(t)
	e.app.Config.AI.Mode = "off" // keywords: no model, no cost
	tracker := fake.GroupPath + "/tracker#11"
	before := issueLabels(t, e, tracker)

	out := e.mustAI("classify", tracker)
	if !strings.Contains(out, "suggested labels (keywords): type::bug") || strings.Contains(out, "added") {
		t.Fatalf("output:\n%s", out)
	}
	if got := issueLabels(t, e, tracker); got != before {
		t.Fatalf("suggesting changed labels: %s", got)
	}

	// --apply from a script without --yes: refused, nothing written.
	_, err := e.ai("classify", tracker, "--apply")
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitNeedsConfirm || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	if got := issueLabels(t, e, tracker); got != before {
		t.Fatalf("applied without a confirmation: %s", got)
	}

	// --dry-run never writes, even with --yes.
	e.mustAI("classify", tracker, "--apply", "--yes", "--dry-run")
	if got := issueLabels(t, e, tracker); got != before {
		t.Fatalf("dry run wrote: %s", got)
	}

	out = e.mustAI("classify", tracker, "--apply", "--yes")
	if !strings.Contains(out, "added type::bug") {
		t.Fatalf("output:\n%s", out)
	}
	if got := issueLabels(t, e, tracker); !strings.Contains(got, "type::bug") {
		t.Fatalf("labels after apply: %s", got)
	}
	if es := e.audited("forge.issue.labels"); len(es) != 1 {
		t.Fatalf("audit: %+v", es)
	}
	// Labelled now: nothing left to classify.
	if out := e.mustAI("classify", tracker, "--apply", "--yes"); strings.Contains(out, "added") {
		t.Fatalf("second apply: %s", out)
	}
}
