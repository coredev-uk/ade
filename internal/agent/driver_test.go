package agent

import (
	"bufio"
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
)

func feed(t *testing.T, p Parser, fixture string) Progress {
	t.Helper()
	f, err := os.Open("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		p.Feed(sc.Bytes())
	}
	return p.Progress()
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// argValue returns the argument after flag, or "" when flag is absent.
func argValue(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func TestOMPCommand(t *testing.T) {
	d := &ompDriver{cmd: "omp"}
	plan := d.Command(Spec{Kind: KindPlan, Model: "m1", Dir: "/w/api", AddDirs: []string{"/w/web", "/w/lib"}, Prompt: "SECRET PROMPT", MaxCostUSD: 0.25})
	if plan.Path != "omp" || plan.Dir != "/w/api" || plan.Stdin != "SECRET PROMPT" {
		t.Errorf("plan cmd = %+v", plan)
	}
	if slices.Contains(plan.Args, "SECRET PROMPT") {
		t.Error("the prompt is on argv: it belongs on stdin")
	}
	if got := argValue(plan.Args, "--tools"); got != "read,grep,glob" {
		t.Errorf("a plan must be read-only: tools = %q", got)
	}
	if slices.Contains(plan.Args, "--auto-approve") {
		t.Error("a plan must not auto-approve tool calls")
	}
	if got := argValue(plan.Args, "--max-cost"); got != "0.250000" {
		t.Errorf("--max-cost = %q", got)
	}
	if got := argValue(plan.Args, "--cwd"); got != "/w/api" {
		t.Errorf("--cwd = %q", got)
	}
	var adds []string
	for i, a := range plan.Args {
		if a == "--add-dir" {
			adds = append(adds, plan.Args[i+1])
		}
	}
	if !slices.Equal(adds, []string{"/w/web", "/w/lib"}) {
		t.Errorf("--add-dir = %v", adds)
	}
	for _, must := range []string{"-p", "--no-session", "--no-context-files", "--no-extensions"} {
		if !slices.Contains(plan.Args, must) {
			t.Errorf("missing %s in %v", must, plan.Args)
		}
	}
	if i := slices.Index(plan.Args, "--mode"); i < 0 || plan.Args[i+1] != "json" {
		t.Errorf("not in json mode: %v", plan.Args)
	}

	run := d.Command(Spec{Kind: KindRun, Dir: "/w/api"})
	if got := argValue(run.Args, "--tools"); !strings.Contains(got, "edit") || !strings.Contains(got, "bash") {
		t.Errorf("a run needs edit and bash: tools = %q", got)
	}
	if !slices.Contains(run.Args, "--auto-approve") {
		t.Error("a run is non-interactive: it must auto-approve")
	}
	if slices.Contains(run.Args, "--max-cost") {
		t.Error("no cap known: --max-cost must be absent")
	}
}

func TestOMPParse(t *testing.T) {
	p := feed(t, (&ompDriver{}).Parser(Spec{}), "omp.jsonl")
	// Two turns in the recording: 0.0009614 + 0.0000657.
	if !near(p.CostUSD, 0.0010271) || p.Source != "reported" || p.Turns != 2 {
		t.Errorf("cost %v source %q turns %d", p.CostUSD, p.Source, p.Turns)
	}
	if p.In != 4+4610+2+87 || p.Out != 82 || p.Cache != 4610 {
		t.Errorf("tokens in %d out %d cache %d", p.In, p.Out, p.Cache)
	}
	if p.Text == "" || p.Failed != "" || !p.Done || p.Model == "" {
		t.Errorf("progress = %+v", p)
	}
	if p.Activity != "read" {
		t.Errorf("activity = %q", p.Activity)
	}
}

func TestOMPParseRunningCostGrowsPerTurn(t *testing.T) {
	p := (&ompDriver{}).Parser(Spec{})
	p.Feed([]byte(`{"type":"turn_end","message":{"role":"assistant","stopReason":"toolUse","usage":{"cost":{"total":0.2}}}}`))
	if !near(p.Progress().CostUSD, 0.2) {
		t.Fatalf("after turn 1: %v", p.Progress().CostUSD)
	}
	p.Feed([]byte(`{"type":"turn_end","message":{"role":"assistant","stopReason":"error","errorMessage":"429","usage":{"cost":{"total":0.1}}}}`))
	if pr := p.Progress(); !near(pr.CostUSD, 0.3) || pr.Failed != "429" {
		t.Errorf("after turn 2: %+v", pr)
	}
	p.Feed([]byte("not json"))
	p.Feed([]byte(`{"type":"turn_end","message":{"role":"user"}}`))
	if p.Progress().Turns != 2 {
		t.Errorf("non-assistant and garbage lines must be ignored: %+v", p.Progress())
	}
}

func TestClaudeCommand(t *testing.T) {
	d := &claudeDriver{cmd: "claude"}
	plan := d.Command(Spec{Kind: KindPlan, Model: "claude-haiku-4-5", Dir: "/w", AddDirs: []string{"/x"}, Prompt: "p", MaxCostUSD: 0.4})
	if plan.Path != "claude" || plan.Stdin != "p" {
		t.Errorf("cmd = %+v", plan)
	}
	for flag, want := range map[string]string{
		"--output-format":   "stream-json",
		"--model":           "claude-haiku-4-5",
		"--permission-mode": "plan",
		"--tools":           "Read,Grep,Glob",
		"--max-budget-usd":  "0.400000",
		"--add-dir":         "/x",
	} {
		if got := argValue(plan.Args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	if !slices.Contains(plan.Args, "-p") || !slices.Contains(plan.Args, "--verbose") {
		t.Errorf("stream-json needs -p and --verbose: %v", plan.Args)
	}
	run := d.Command(Spec{Kind: KindAddress, Dir: "/w"})
	if argValue(run.Args, "--permission-mode") != "acceptEdits" || !strings.Contains(argValue(run.Args, "--tools"), "Edit") {
		t.Errorf("a writing run: %v", run.Args)
	}
}

func TestClaudeParse(t *testing.T) {
	price := &config.Price{Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25}
	p := feed(t, (&claudeDriver{}).Parser(Spec{Price: price}), "claude.jsonl")
	if !near(p.CostUSD, 0.0123) || p.Source != "reported" {
		t.Errorf("the result's total_cost_usd wins over the estimate: %v %q", p.CostUSD, p.Source)
	}
	if p.Text != "Plan: change a.txt." || p.Turns != 2 || p.Failed != "" || !p.Done || p.Model != "claude-haiku-4-5" {
		t.Errorf("progress = %+v", p)
	}
}

func TestClaudeRunningEstimateBeforeResult(t *testing.T) {
	price := &config.Price{Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25}
	p := (&claudeDriver{}).Parser(Spec{Price: price})
	p.Feed([]byte(`{"type":"assistant","message":{"model":"m","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":1000000,"output_tokens":100000}}}`))
	if pr := p.Progress(); !near(pr.CostUSD, 1.5) || pr.Source != "computed" {
		t.Errorf("estimate = %v %q, want 1.5 computed (1M in × $1 + 100k out × $5)", pr.CostUSD, pr.Source)
	}
	// No price table: nothing to estimate, and the source stays honest.
	q := (&claudeDriver{}).Parser(Spec{})
	q.Feed([]byte(`{"type":"assistant","message":{"content":[],"usage":{"input_tokens":5}}}`))
	if q.Progress().CostUSD != 0 || q.Progress().Source != "" {
		t.Errorf("unpriced: %+v", q.Progress())
	}
}

func TestClaudeBudgetStopIsAFailure(t *testing.T) {
	p := feed(t, (&claudeDriver{}).Parser(Spec{}), "claude_budget.jsonl")
	if p.Failed == "" || !near(p.CostUSD, 0.5) {
		t.Errorf("error_max_budget_usd must fail the call and keep its cost: %+v", p)
	}
}

func TestCodexCommand(t *testing.T) {
	d := &codexDriver{cmd: "codex"}
	plan := d.Command(Spec{Kind: KindPlan, Model: "gpt-x", Dir: "/w", AddDirs: []string{"/x"}, Prompt: "p"})
	if plan.Args[0] != "exec" || plan.Args[len(plan.Args)-1] != "-" || plan.Stdin != "p" {
		t.Errorf("cmd = %+v", plan)
	}
	for flag, want := range map[string]string{"-C": "/w", "-m": "gpt-x", "-s": "read-only", "--add-dir": "/x"} {
		if got := argValue(plan.Args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	if !slices.Contains(plan.Args, "--json") {
		t.Error("missing --json")
	}
	if got := argValue(d.Command(Spec{Kind: KindRun, Dir: "/w"}).Args, "-s"); got != "workspace-write" {
		t.Errorf("a run's sandbox = %q", got)
	}
}

func TestCodexParse(t *testing.T) {
	price := &config.Price{Input: 2, Output: 8, CacheRead: 0.5}
	p := feed(t, (&codexDriver{}).Parser(Spec{Price: price}), "codex.jsonl")
	// 2000 fresh in ($2/M) + 10000 cached ($0.5/M) + 300 out ($8/M).
	want := (2000*2 + 10000*0.5 + 300*8) / 1e6
	if !near(p.CostUSD, want) || p.Source != "computed" {
		t.Errorf("cost %v (%q), want %v computed", p.CostUSD, p.Source, want)
	}
	if p.In != 2000 || p.Cache != 10000 || p.Out != 300 || p.Text != "Plan: edit a.txt." || p.Turns != 1 || p.Activity != "bash" {
		t.Errorf("progress = %+v", p)
	}
	q := feed(t, (&codexDriver{}).Parser(Spec{}), "codex.jsonl")
	if q.CostUSD != 0 || q.Source != "unknown" {
		t.Errorf("no price: cost must be unknown, not a made-up number: %v %q", q.CostUSD, q.Source)
	}
}

func TestCodexFailure(t *testing.T) {
	p := feed(t, (&codexDriver{}).Parser(Spec{}), "codex_failed.jsonl")
	if p.Failed != "usage limit reached" {
		t.Errorf("failed = %q", p.Failed)
	}
}

func TestCodexTransientErrorIsClearedByATurn(t *testing.T) {
	p := (&codexDriver{}).Parser(Spec{})
	p.Feed([]byte(`{"type":"error","message":"reconnecting"}`))
	p.Feed([]byte(`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`))
	if p.Progress().Failed != "" {
		t.Errorf("a later completed turn must clear a transient error: %+v", p.Progress())
	}
}

func TestForProfile(t *testing.T) {
	cases := []struct {
		p        config.AIProfile
		override string
		name     string
		bin      string
	}{
		{config.AIProfile{Driver: "omp"}, "", "omp", "omp"},
		{config.AIProfile{Driver: "omp", Command: "/opt/omp"}, "", "omp", "/opt/omp"},
		{config.AIProfile{Driver: "anthropic", Agent: "claude"}, "", "claude", "claude"},
		{config.AIProfile{Driver: "omp", Agent: "claude", AgentCommand: "/x/claude"}, "", "claude", "/x/claude"},
		{config.AIProfile{Driver: "omp", Command: "/opt/omp"}, "codex", "codex", "codex"},
	}
	for _, c := range cases {
		d, err := ForProfile(c.p, c.override)
		if err != nil {
			t.Fatal(err)
		}
		if d.Name() != c.name || d.Binary() != c.bin {
			t.Errorf("%+v/%q → %s %s, want %s %s", c.p, c.override, d.Name(), d.Binary(), c.name, c.bin)
		}
	}
	if _, err := ForProfile(config.AIProfile{Agent: "gemini"}, ""); err == nil {
		t.Error("an unknown agent driver must be an error")
	}
}

func TestAvailableMissingBinary(t *testing.T) {
	d, _ := ForProfile(config.AIProfile{AgentCommand: "definitely-not-installed-xyz", Agent: "claude"}, "")
	if err := Available(d); err == nil || !strings.Contains(err.Error(), "not found on PATH") {
		t.Errorf("err = %v", err)
	}
}

func TestStateMachine(t *testing.T) {
	ok := [][2]State{
		{StateQueued, StatePlanning},
		{StateQueued, StateRunning},
		{StatePlanning, StateAwaiting},
		{StateAwaiting, StateRunning},
		{StateAwaiting, StatePlanning},
		{StateRunning, StateReview},
		{StateReview, StatePushed},
		{StateRunning, StateFailed},
		{StateReview, StateCancelled},
	}
	for _, m := range ok {
		if !CanMove(m[0], m[1]) {
			t.Errorf("%s → %s must be allowed", m[0], m[1])
		}
	}
	bad := [][2]State{
		{StateQueued, StateReview},
		{StateQueued, StatePushed},
		{StatePlanning, StateRunning},
		{StateAwaiting, StateReview},
		{StateAwaiting, StatePushed},
		{StatePushed, StateRunning},
		{StateFailed, StatePlanning},
		{StateCancelled, StateQueued},
		{StateRunning, StatePushed},
	}
	for _, m := range bad {
		if CanMove(m[0], m[1]) {
			t.Errorf("%s → %s must be refused", m[0], m[1])
		}
	}
	for _, s := range []State{StatePushed, StateFailed, StateCancelled} {
		if !s.Terminal() || len(transitions[s]) != 0 {
			t.Errorf("%s must be terminal", s)
		}
	}
}

func TestMatchID(t *testing.T) {
	ids := []string{"20261111-100000-a3f", "20261111-100500-b41", "20261112-090000-a3f"}
	for _, c := range []struct{ in, want, err string }{
		{"20261111-100000-a3f", ids[0], ""},
		{"20261112", ids[2], ""},
		{"b41", ids[1], ""},
		{"a3f", "", "matches 2 runs"},
		{"zzz", "", "no run"},
		{"", "", "no run id"},
	} {
		got, err := matchID(ids, c.in)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: err = %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q → %q, %v", c.in, got, err)
		}
	}
}

func TestParseReplies(t *testing.T) {
	text := "Done.\n```json\n{\"replies\":[{\"thread\":\"d1\",\"body\":\"ok\"}]}\n```\nlater\n```json\n{\"replies\":[{\"thread\":\"d2\",\"body\":\" fixed \"},{\"thread\":\"\",\"body\":\"x\"},{\"thread\":\"d3\",\"body\":\"  \"}]}\n```\n"
	rs, err := ParseReplies(text)
	if err != nil || len(rs) != 1 || rs[0].Thread != "d2" {
		t.Fatalf("the last block wins, empty entries are dropped: %+v %v", rs, err)
	}
	if rs, err := ParseReplies("no block"); err != nil || rs != nil {
		t.Errorf("no block is no replies, not an error: %v %v", rs, err)
	}
	if _, err := ParseReplies("```json\n{oops\n```"); err == nil {
		t.Error("a malformed block must be reported")
	}
}
