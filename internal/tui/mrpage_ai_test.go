package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/review"
)

// The AI actions of the MR page run against the fake GitLab and a stub omp: a
// script that answers each feature (picked by its system prompt) from a canned
// turn_end line, and logs which features were asked.

const (
	stubSummary = "- Renames the bucket file and tracks tokens explicitly.\n- Allow reports whether a token is free.\nRisk: low"
	stubThread  = "Rae asks whether Tokens refill on a timer or lazily; nobody has answered yet."
	stubReply   = "Lazily, on Allow: it avoids a goroutine per tenant."
	stubReview  = `[
	  {"path":"limit/token_bucket.go","line":11,"side":"new","severity":"blocking","body":"Allow never consumes a token; decrement Tokens when it returns true."},
	  {"path":"limit/token_bucket.go","line":7,"side":"new","severity":"question","body":"Why float64 for Tokens?"},
	  {"path":"limit/token_bucket.go","line":900,"side":"new","severity":"nit","body":"not on the diff"}
	]`
)

type aiStub struct {
	t     *testing.T
	dir   string
	calls string // file: one line per omp call, the feature it answered
}

// prompt is what the stub last received for a feature ("sum", "thread", "reply", "review").
func (s *aiStub) prompt(feature string) string {
	b, err := os.ReadFile(filepath.Join(s.dir, "prompt-"+feature+".txt"))
	if err != nil {
		s.t.Fatalf("no %s prompt was sent: %v", feature, err)
	}
	return string(b)
}

func turnEnd(text string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "turn_end",
		"message": map[string]any{
			"role": "assistant", "model": "claude-haiku-4-5", "stopReason": "stop",
			"content": []map[string]string{{"type": "text", "text": text}},
			"usage":   map[string]any{"input": 900, "output": 60, "cost": map[string]float64{"total": 0.0012}},
		},
	})
	return string(b) + "\n"
}

// enableAI points the page at the stub omp with the given ai.mode.
func (e *mrEnv) enableAI(mode string) *aiStub {
	e.t.Helper()
	dir := e.t.TempDir()
	for name, text := range map[string]string{"sum": stubSummary, "thread": stubThread, "reply": stubReply, "review": stubReview} {
		if err := os.WriteFile(filepath.Join(dir, name+".jsonl"), []byte(turnEnd(text)), 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
	calls := filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
in=$(cat)
case "$*" in
  *"summarise merge requests"*) f=sum ;;
  *"summarise one code-review thread"*) f=thread ;;
  *"draft a reply to a code-review thread"*) f=reply ;;
  *"review a merge request diff"*) f=review ;;
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
		Models: map[string]string{"explain": "claude-haiku-4-5", "draft": "claude-haiku-4-5", "review": "claude-haiku-4-5"},
		Prices: map[string]config.Price{"claude-haiku-4-5": {Input: 1, Output: 5}},
	}
	return &aiStub{t: e.t, dir: dir, calls: calls}
}

// asked lists the features the stub answered, in order.
func (s *aiStub) asked() []string {
	b, err := os.ReadFile(s.calls)
	if err != nil {
		return nil
	}
	return strings.Fields(string(b))
}

func (s *aiStub) count(feature string) int {
	n := 0
	for _, f := range s.asked() {
		if f == feature {
			n++
		}
	}
	return n
}

// --- summary ------------------------------------------------------------------------

func TestOverviewShowsTheAISummaryAndDTogglesTheRawDescription(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAI("auto")
	ref := mrRef("api", 2)
	e.open(ref.String())
	e.want("Summary", "AI · claude-haiku-4-5 · $0.0012", "Renames the bucket file", "Risk: low", "d shows the raw description")
	e.wantNot("Stacked on !1")
	if got := stub.asked(); len(got) != 1 || got[0] != "sum" {
		t.Fatalf("model calls = %v, want one summary", got)
	}

	e.press("d")
	e.want("Description", "Stacked on !1", "d shows the AI summary")
	e.wantNot("Renames the bucket file")
	e.press("d")
	e.want("Renames the bucket file")
	e.wantNot("Stacked on !1")
	if len(stub.asked()) != 1 {
		t.Errorf("toggling called the model again: %v", stub.asked())
	}

	es := e.audited("ai.summarize")
	if len(es) != 1 || es[0].Target != ref.String() || es[0].AI == nil || es[0].AI.CostUSD <= 0 {
		t.Fatalf("audit: %+v", es)
	}
	var arts []string
	_ = filepath.WalkDir(e.m.app.RecordsDir, func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() && strings.Contains(p, "summary-mr2") {
			arts = append(arts, p)
		}
		return nil
	})
	if len(arts) != 1 {
		t.Errorf("summary artifacts under %s: %v", e.m.app.RecordsDir, arts)
	}
}

func TestSummaryIsCachedAcrossOpeningsAndRunsOncePerHead(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAI("auto")
	ref := mrRef("api", 2)
	e.open(ref.String())
	e.press("2", "1", "3", "1") // away and back: no second call
	if len(stub.asked()) != 1 {
		t.Fatalf("calls = %v", stub.asked())
	}
	e.press("esc")
	e.m.mrCache = nil // a new session: nothing in memory, the artifact cache answers
	e.open(ref.String())
	e.want("Renames the bucket file", "cached, $0")
	if len(stub.asked()) != 1 {
		t.Errorf("the second open called the model: %v", stub.asked())
	}
	es := e.audited("ai.summarize")
	if len(es) != 2 || es[1].AI == nil || !es[1].AI.CacheHit || es[1].AI.CostUSD != 0 {
		t.Fatalf("the cache hit is audited at $0: %+v", es)
	}
}

func TestSummaryNeverBlocksThePage(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.enableAI("auto")
	e.open(mrRef("api", 2).String())
	d := e.m.mr.data
	k := aiKey{kind: aiSummary}
	cmd := e.m.aiAsk(d, k, true, false) // asking returns at once; the call is the returned command
	if cmd == nil || !d.ai[k].l.busy() {
		t.Fatalf("the ask is not in flight: %+v", d.ai[k])
	}
	e.want("asking the model")
	e.m.mr.rawDesc = true
	e.want("Description", "Stacked on !1") // the page still answers while the call runs
	e.press("tab")
	if e.m.mr.tab != tabThreads {
		t.Fatal("a key was not handled while the model was working")
	}
	e.settle(cmd)
	if d.ai[k].l.busy() {
		t.Fatal("the ask never finished")
	}
}

func TestAIOffHidesEveryAIAffordance(t *testing.T) {
	for _, mode := range []string{"off", "noprofile"} {
		e := newMREnv(t, fake.Free)
		stub := e.enableAI("auto")
		if mode == "off" {
			e.m.app.Config.AI.Mode = "off"
		} else {
			e.m.app.Config.AI.Profile = ""
		}
		e.open(mrRef("api", 2).String())
		e.wantNot("Summary", "asking the model", "agent review pass", "A agent review")
		e.press("d", "s", "A")
		e.press("2", "d", "s", "A")
		e.press("4")
		e.wantNot("agent review running", "A runs", "A agent review")
		e.press("?")
		e.wantNot("agent review pass", "AI draft reply")
		e.want("agent menu")
		e.press("esc")
		if len(stub.asked()) != 0 {
			t.Errorf("mode %s: the model was called: %v", mode, stub.asked())
		}
		if e.m.mr.modal != nil {
			t.Errorf("mode %s: a dialog opened", mode)
		}
	}
}

func TestAskModeWaitsForAYesWhenThePreflightSaysSo(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAI("ask")
	e.m.app.Config.Budget.ConfirmOverUSD = 0.0000001
	e.open(mrRef("api", 2).String())
	e.want("Summary", "waiting for a yes", "over the", "Stacked on !1", "s asks")
	if len(stub.asked()) != 0 {
		t.Fatalf("a call went out without a yes: %v", stub.asked())
	}
	e.press("s")
	e.want("Ask the model · summary", "Nothing is sent until you say yes")
	e.press("esc")
	if len(stub.asked()) != 0 || e.m.mr.modal != nil {
		t.Fatalf("esc must send nothing: %v", stub.asked())
	}
	e.press("s", "enter")
	e.want("Renames the bucket file", "AI · claude-haiku-4-5")
	if len(stub.asked()) != 1 {
		t.Errorf("calls = %v", stub.asked())
	}
}

func TestBudgetStopShowsInTheSummarySectionAndNothingIsSent(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAI("auto")
	e.m.app.Config.Budget.CallUSD = 0.0000001
	e.open(mrRef("api", 2).String())
	e.want("no summary", "budget stop", "Stacked on !1")
	if len(stub.asked()) != 0 {
		t.Errorf("a refused call was sent: %v", stub.asked())
	}
	if len(e.audited("budget.stop")) != 1 {
		t.Errorf("the stop is not audited")
	}
}

// --- threads ------------------------------------------------------------------------

func TestThreadSummaryAndDraftReplyLandInTheEditorAndNeverPostBySelf(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAI("auto")
	ref := mrRef("api", 2)
	before := len(e.discussions(ref)[0].Notes)
	e.open(ref.String())
	e.press("2")
	e.want("d AI reply", "s AI summary")

	e.press("s")
	e.want("Summary", "nobody has")
	if stub.count("thread") != 1 {
		t.Fatalf("calls = %v", stub.asked())
	}

	e.press("d")
	if e.m.mr.modal == nil || e.m.mr.modal.kind != modalReply {
		t.Fatalf("the draft did not open the reply editor: %+v", e.m.mr.modal)
	}
	e.want("Reply · ", "AI draft · claude-haiku-4-5", stubReply, "enter posts")
	if got := len(e.discussions(ref)[0].Notes); got != before {
		t.Fatalf("the draft was posted by itself (%d notes, was %d)", got, before)
	}
	if len(e.audited("forge.thread.reply")) != 0 {
		t.Fatal("a reply write was audited before enter")
	}

	e.press("esc") // thrown away
	if got := len(e.discussions(ref)[0].Notes); got != before {
		t.Fatal("esc posted the draft")
	}
	e.press("r")
	e.wantNot("AI draft ·", stubReply) // the draft was used up: r opens an empty editor

	e.press("esc", "d") // ask again: cached, $0, and it is back in the editor
	e.want(stubReply)
	if stub.count("reply") != 1 {
		t.Errorf("the second draft was not a cache hit: %v", stub.asked())
	}
	e.typ(" Thanks.") // edited before sending
	e.press("enter")
	var posted bool
	for _, n := range e.discussions(ref)[0].Notes {
		posted = posted || n.Body == stubReply+" Thanks."
	}
	if !posted {
		t.Fatalf("the edited draft is not on the forge: %+v", e.discussions(ref)[0].Notes)
	}
	if es := e.audited("ai.draft-reply"); len(es) != 2 {
		t.Errorf("ai.draft-reply audit entries = %d, want 2 (one paid, one cached)", len(es))
	}
}

func TestDraftReplyFinishingAwayFromTheThreadWaitsForR(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.enableAI("auto")
	ref := mrRef("api", 2)
	e.open(ref.String())
	e.press("2")
	d := e.m.mr.data
	id := d.threadList()[0].d.ID
	cmd := e.m.aiAsk(d, aiKey{kind: aiReply, id: id}, false, false)
	e.press("1") // the page moves on while the model works
	e.settle(cmd)
	if e.m.mr.modal != nil {
		t.Fatal("the editor opened over a tab the user had left")
	}
	e.press("2")
	e.want("AI draft reply ready")
	e.press("r")
	e.want("AI draft · ", stubReply)
}

// A draft the model did not finish carries its note in the editor, not in the
// text that enter would post.
func TestTruncatedDraftReplyShowsTheNoteOutsideTheText(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.enableAI("auto")
	ref := mrRef("api", 2)
	e.open(ref.String())
	e.press("2")
	d := e.m.mr.data
	id := d.threadList()[0].d.ID
	res := ai.Result{Text: "Thanks, this adds an", Truncated: true, Model: "m"}
	st := &aiText{l: load{st: loadOK}, text: res.Text, model: res.Model, cut: res.CutShort()}
	e.m.openReply(d, id, st)
	e.want("Thanks, this adds an", "cut short", "finish it before posting")
	if got := e.m.mr.modal.ed.String(); got != "Thanks, this adds an" {
		t.Errorf("the editor holds %q: the note must not be part of what is posted", got)
	}
}

// --- agent review pass ---------------------------------------------------------------

func TestAgentReviewPassRunsInTheBackgroundAndLandsPendingDrafts(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAI("auto")
	ref := mrRef("api", 2)
	threadsBefore := len(e.discussions(ref))
	e.m.w = 200 // the footer drops hints that do not fit; this one is checked in full
	e.open(ref.String())
	e.want("A agent review")
	e.m.w = 140

	e.press("4")
	e.want("A runs an agent review pass")
	next, cmd := e.m.Update(keyMsg("A"))
	e.m = next.(model)
	e.want("agent review…", "agent review running") // the strip and the Review tab say so; the page stays usable
	e.settle(cmd)

	e.want("agent review done", "2 comments pending", "1 dropped", "agent · pending", "Allow never consumes a token")
	ds, err := e.store.List(ref)
	if err != nil || len(ds) != 2 {
		t.Fatalf("drafts = %+v, %v", ds, err)
	}
	for _, d := range ds {
		if d.Source != review.SourceAgent || d.State != review.StatePending {
			t.Errorf("draft %+v must be a pending agent draft", d)
		}
	}
	if stub.count("review") != 1 {
		t.Errorf("calls = %v", stub.asked())
	}
	if got := len(e.discussions(ref)); got != threadsBefore || len(e.audited("forge.review.submit")) != 0 {
		t.Errorf("the pass reached the forge: %d threads (was %d)", got, threadsBefore)
	}
	if es := e.audited("ai.review"); len(es) != 1 || es[0].Target != ref.String() {
		t.Errorf("ai.review audit: %+v", es)
	}
	var kinds []string
	for _, ev := range e.drainEvents() {
		kinds = append(kinds, ev.Kind)
	}
	if !strings.Contains(strings.Join(kinds, " "), "mr.agent_review") {
		t.Errorf("events = %v", kinds)
	}

	// The same pass again adds nothing twice (cached answer, duplicate drafts skipped).
	e.press("A")
	if ds, _ := e.store.List(ref); len(ds) != 2 {
		t.Errorf("a repeated pass added drafts: %d", len(ds))
	}
	if stub.count("review") != 1 {
		t.Errorf("the repeated pass was not a cache hit: %v", stub.asked())
	}
}

func TestAgentMenuIsOnPAndAIsReviewPass(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.enableAI("auto")
	e.open(grp + "/tracker#1")
	e.press("A")
	if e.m.agt != nil {
		t.Fatal("A opened the agent menu: it is the review pass now")
	}
	e.press("4")
	e.want("agent review done")
	next, _ := e.m.Update(keyMsg("P")) // the menu's own polling is not run here
	e.m = next.(model)
	if e.m.agt == nil {
		t.Fatal("P did not open the agent menu")
	}
}

// --- D: delta -------------------------------------------------------------------------

func TestPatchOfWritesHeadersForNewDeletedAndRenamedFiles(t *testing.T) {
	d := forge.Diff{Files: []forge.DiffFile{
		{OldPath: "a.go", NewPath: "a.go", NewFile: true, Diff: "@@ -0,0 +1 @@\n+x\n"},
		{OldPath: "b.go", NewPath: "b.go", Deleted: true, Diff: "@@ -1 +0,0 @@\n-y"},
		{OldPath: "c.go", NewPath: "d.go", Renamed: true, Diff: "@@ -1 +1 @@\n-1\n+2\n"},
		{OldPath: "e.bin", NewPath: "f.bin", Renamed: true},
	}}
	want := "diff --git a/a.go b/a.go\nnew file mode 100644\n--- /dev/null\n+++ b/a.go\n@@ -0,0 +1 @@\n+x\n" +
		"diff --git a/b.go b/b.go\ndeleted file mode 100644\n--- a/b.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-y\n" +
		"diff --git a/c.go b/d.go\nrename from c.go\nrename to d.go\n--- a/c.go\n+++ b/d.go\n@@ -1 +1 @@\n-1\n+2\n" +
		"diff --git a/e.bin b/f.bin\nrename from e.bin\nrename to f.bin\n"
	if got := patchOf(d); got != want {
		t.Errorf("patch:\n%s\nwant:\n%s", got, want)
	}
}

func TestPagerLinePrefersDeltaThenGitsPager(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		delta     bool
		gitPager  string
		env       map[string]string
		wantStart string
	}{
		{true, "less", map[string]string{"PAGER": "more"}, "delta < "},
		{false, "bat -p\n", map[string]string{"PAGER": "more"}, "bat -p < "},
		{false, "", map[string]string{"GIT_PAGER": "diff-so-fancy | less", "PAGER": "more"}, "diff-so-fancy | less < "},
		{false, "", map[string]string{"PAGER": "more"}, "more < "},
		{false, "", nil, "less -R < "},
	} {
		got := pagerLine("/tmp/it's.diff", c.delta, c.gitPager, env(c.env))
		if !strings.HasPrefix(got, c.wantStart) || !strings.HasSuffix(got, `'/tmp/it'\''s.diff'`) {
			t.Errorf("%+v -> %q", c, got)
		}
	}
}

// In zellij, D opens delta in a floating pane that closes with the pager; the
// pane reads the MR's diff from a file.
func TestDOpensDeltaInAZellijFloatingPane(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "zellij.log")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write("zellij", "#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+log+"'\n")
	write("delta", "#!/bin/sh\ncat\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ZELLIJ", "0")

	e := newMREnv(t, fake.Free)
	e.open(mrRef("api", 2).String())
	e.press("3")
	e.press("D") // file list focus
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("zellij was not run: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(got)), "\n")
	if args[0] != "run" || !contains(args, "--floating") || !contains(args, "--close-on-exit") {
		t.Fatalf("zellij args = %q", args)
	}
	script := args[len(args)-1]
	if !strings.HasPrefix(script, "delta < '") {
		t.Fatalf("pane script = %q", script)
	}
	file := strings.Split(strings.TrimPrefix(script, "delta < '"), "'")[0]
	patch, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "diff --git a/limit/bucket.go b/limit/token_bucket.go") || !strings.Contains(string(patch), "+func (b *Bucket) Allow()") {
		t.Errorf("patch:\n%s", patch)
	}
	_ = os.Remove(file)
	if e.m.mr.modal != nil {
		t.Error("a dialog opened")
	}
}

// Outside zellij the dashboard steps aside and runs the pager here.
func TestDOutsideZellijRunsThePagerInThisTerminal(t *testing.T) {
	t.Setenv("ZELLIJ", "")
	e := newMREnv(t, fake.Free)
	e.open(mrRef("api", 2).String())
	e.press("3")
	next, cmd := e.m.Update(keyMsg("D"))
	e.m = next.(model)
	if cmd == nil {
		t.Fatal("D did nothing")
	}
}

// The user's notes on the MR go into the prompt, as they do for `ade ai summarize`.
func TestSummaryCarriesTheUsersNotes(t *testing.T) {
	e := newMREnv(t, fake.Free)
	stub := e.enableAI("auto")
	ref := mrRef("api", 2)
	rec := records.New(e.m.app.RecordsDir)
	wi := domain.WorkItem{Project: "demo", Ref: ref, Issue: forge.Issue{Ref: ref, Title: "Rename bucket"}}
	if _, _, err := rec.WriteSnapshot("f", wi, nil, 0); err != nil { // the record sync keeps for the item
		t.Fatal(err)
	}
	if err := rec.WriteNotes("f", ref, "Rae prefers lazy refill over a timer."); err != nil {
		t.Fatal(err)
	}
	e.open(ref.String())
	e.want("Summary", "includes your notes")
	if p := stub.prompt("sum"); !strings.Contains(p, "Rae prefers lazy refill over a timer.") {
		t.Errorf("the notes are not in the prompt:\n%s", p)
	}
}
