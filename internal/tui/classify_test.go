package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/budget"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// newClassifyEnv is the Start work env with tracker#11 (no type or app label)
// on Home, and AI off unless a test turns it on.
func newClassifyEnv(t *testing.T) *swEnvT {
	t.Helper()
	e := newSWEnv(t)
	e.app.RecordsDir = t.TempDir()
	e.app.Config.AI.Mode = "off"
	e.m.results[0].Items = append(e.m.results[0].Items, e.item(11, "Exports page sometimes shows stale totals"))
	e.m.rebuildHome("")
	e.selectRow(trk(11).String())
	return e
}

func (e *swEnvT) issueLabels(iid int) []string {
	e.t.Helper()
	is, err := gitlab.New(e.srv.URL, fake.Token).Issue(context.Background(), trk(iid))
	if err != nil {
		e.t.Fatal(err)
	}
	return is.Labels
}

// stubOMP makes profile "sub" an omp stub answering with text.
func stubOMP(t *testing.T, e *swEnvT, text string) {
	t.Helper()
	dir := t.TempDir()
	canned := filepath.Join(dir, "out.jsonl")
	line := `{"type":"turn_end","message":{"role":"assistant","model":"claude-haiku-4-5","stopReason":"stop","content":[{"type":"text","text":` + jsonString(text) + `}],"usage":{"input":900,"output":40,"cost":{"total":0.001}}}}` + "\n"
	if err := os.WriteFile(canned, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "omp")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\ncat "+canned+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	e.app.Config.AI.Profile = "sub"
	e.app.Config.AI.Profiles["sub"] = config.AIProfile{
		Driver: "omp", Command: script, Models: map[string]string{"explain": "claude-haiku-4-5"},
		Prices: map[string]config.Price{"claude-haiku-4-5": {Input: 1, Output: 5}},
	}
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func TestClassifyWithAIOffSuggestsFromKeywordsAndAppliesOnConfirm(t *testing.T) {
	e := newClassifyEnv(t)
	before := e.issueLabels(11)
	e.press("c")
	if e.m.cl == nil || e.m.cl.stage != clSuggest {
		t.Fatalf("stage = %+v", e.m.cl)
	}
	e.want("Classify · "+trk(11).String(), "Exports page sometimes shows stale totals", "Suggested labels", "keywords", "no cost", "type::bug", "no app keyword matched", "no app label suggested", "labels are only added, never removed")
	if got := e.issueLabels(11); len(got) != len(before) {
		t.Fatalf("a suggestion changed the issue's labels: %v", got)
	}
	e.press("enter")
	if e.m.cl.stage != clDone {
		t.Fatalf("stage after enter = %v err %v", e.m.cl.stage, e.m.cl.err)
	}
	e.want("added type::bug")
	got := e.issueLabels(11)
	if !strings.Contains(strings.Join(got, ","), "type::bug") || len(got) != len(before)+1 {
		t.Fatalf("labels %v (before %v): exactly the ticked label is added", got, before)
	}
	es, _ := e.app.Audit.Query(audit.Filter{Action: "forge.issue.labels"})
	if len(es) != 1 || es[0].Target != trk(11).String() || !strings.Contains(es[0].Params["reason"].(string), "keywords") {
		t.Fatalf("audit: %+v", es)
	}
	e.press("esc")
	if e.m.cl != nil {
		t.Fatal("dialog still open")
	}
}

func TestClassifyAppliesNothingUntilConfirmedAndOnlyTickedLabels(t *testing.T) {
	e := newClassifyEnv(t)
	before := e.writes()
	e.press("c", "x") // untick the only label
	e.press("enter")
	if e.m.cl == nil || e.m.cl.stage != clSuggest {
		t.Fatalf("dialog must stay on the proposal: %+v", e.m.cl)
	}
	e.want("nothing ticked")
	e.press("esc")
	if e.m.cl != nil || e.writes() != before {
		t.Fatalf("cancelled: open=%v writes %d -> %d", e.m.cl != nil, before, e.writes())
	}
}

func TestClassifyWithAIAsksTheModelAndCostsAreShown(t *testing.T) {
	e := newClassifyEnv(t)
	e.app.Config.AI.Mode = "auto"
	stubOMP(t, e, `{"type":"bug","apps":["api","mobile"],"reason":"totals lag behind the table"}`)
	e.press("c")
	if e.m.cl.stage != clSuggest {
		t.Fatalf("stage %v err %v", e.m.cl.stage, e.m.cl.err)
	}
	e.want("Suggested labels", "AI", "claude-haiku-4-5", "$0.0010", "type::bug", "app::api", "totals lag behind the table", "ignored app \"mobile\"")
	e.wantNot("keywords ·")
	e.press("j", "x", "enter") // untick app::api, apply the type only
	got := strings.Join(e.issueLabels(11), ",")
	if !strings.Contains(got, "type::bug") || strings.Contains(got, "app::api") {
		t.Fatalf("labels %s", got)
	}
	if es, _ := e.app.Audit.Query(audit.Filter{Action: "ai.classify"}); len(es) != 1 {
		t.Fatalf("the AI call is audited: %+v", es)
	}
}

func TestClassifyAsksBeforeASpendingCall(t *testing.T) {
	e := newClassifyEnv(t)
	e.app.Config.AI.Mode = "ask"
	e.app.Config.Budget.ConfirmOverUSD = 0.0000001
	stubOMP(t, e, `{"type":"bug","apps":[]}`)
	e.press("c")
	if e.m.cl.stage != clAsk {
		t.Fatalf("stage %v err %v", e.m.cl.stage, e.m.cl.err)
	}
	e.want("this asks the model first", "you set to be asked first")
	e.wantNot("confirm_over_usd")
	for _, ln := range strings.Split(ansi.Strip(e.m.classifyOverlay(100, 30)), "\n") {
		if s := strings.TrimSpace(strings.Trim(ln, e.m.g.Border.Left)); strings.HasPrefix(s, "·") {
			t.Fatalf("a pre-flight line starts with a bare separator: %q", ln)
		}
	}
	if es, _ := e.app.Audit.Query(audit.Filter{Action: "ai.*"}); len(es) != 0 {
		t.Fatal("nothing was sent yet")
	}
	e.press("y")
	if e.m.cl.stage != clSuggest {
		t.Fatalf("stage %v err %v", e.m.cl.stage, e.m.cl.err)
	}
	e.want("type::bug")
}

func TestClassifyNeedsNothingWhenTheIssueIsLabelled(t *testing.T) {
	e := newClassifyEnv(t)
	e.selectRow(trk(3).String()) // type and app labels present in the fake
	before := e.writes()
	e.press("c")
	if e.m.cl == nil || e.m.cl.stage != clFailed {
		t.Fatalf("stage %+v", e.m.cl)
	}
	e.want("already has a type and an app label", "no model call was made")
	if e.writes() != before {
		t.Fatal("wrote")
	}
}

func TestClassifyOnlyOpensOnAHomeItem(t *testing.T) {
	e := newClassifyEnv(t)
	e.m.page = pageTrees
	e.press("c")
	if e.m.cl != nil {
		t.Fatal("c on the Worktrees page opened a dialog")
	}
}

func TestAIChipStatesCarryAWordAndAGlyph(t *testing.T) {
	g := asciiGlyphs
	for _, tc := range []struct {
		chip aiChip
		want string
	}{
		{aiChip{}, ""},
		{aiChip{ok: true, off: true}, "AI off"},
		{aiChip{ok: true, period: "day", spent: 0.38}, "AI $0.38"},
		{aiChip{ok: true, period: "day", spent: 0.38, limit: 2}, "AI $0.38 / $2.00 day"},
		{aiChip{ok: true, period: "week", spent: 6.4, limit: 8}, "! AI $6.40 / $8.00 week"},
		{aiChip{ok: true, period: "day", spent: 2.5, limit: 2}, "x AI $2.50 / $2.00 day cap"},
	} {
		if got := ansi.Strip(tc.chip.render(g)); got != tc.want {
			t.Errorf("render(%+v) = %q, want %q", tc.chip, got, tc.want)
		}
	}
}

func TestPickChipChoosesThePeriodNearestItsCap(t *testing.T) {
	caps := []budget.Cap{
		{Period: "day", SpentUSD: 0.5, LimitUSD: 2, Known: true},
		{Period: "week", SpentUSD: 7, LimitUSD: 8, Known: true},
		{Period: "sprint", Known: false},
	}
	if c := pickChip(caps); c.period != "week" || c.spent != 7 || c.limit != 8 {
		t.Fatalf("%+v", c)
	}
	// No cap anywhere: today's spend alone.
	if c := pickChip([]budget.Cap{{Period: "day", SpentUSD: 0.3, Known: true}, {Period: "week", SpentUSD: 1, Known: true}}); c.limit != 0 || c.spent != 0.3 {
		t.Fatalf("%+v", c)
	}
}

func TestHeaderShowsTheChipNextToUnread(t *testing.T) {
	e := newClassifyEnv(t)
	e.m.chip = aiChip{ok: true, period: "day", spent: 1.7, limit: 2}
	line := strings.SplitN(e.screen(), "\n", 2)[0]
	if !strings.Contains(line, "AI $1.70 / $2.00 day") || !strings.Contains(line, "no unread") {
		t.Fatalf("header = %q", line)
	}
	if strings.Index(line, "AI $1.70") > strings.Index(line, "no unread") {
		t.Fatalf("the chip sits left of the unread count: %q", line)
	}
	e.m.chip = aiChip{}
	if strings.Contains(strings.SplitN(e.screen(), "\n", 2)[0], "AI $") {
		t.Fatal("no chip until loaded")
	}
}

func TestClassifyFailureShowsACauseAndARecoveryAndTheRawErrorStaysInTheLog(t *testing.T) {
	e := newClassifyEnv(t)
	e.app.Config.AI.Mode = "auto"
	stubOMP(t, e, `{}`)
	p := e.app.Config.AI.Profiles["sub"]
	p.Command = "omp-not-installed-" + t.Name()
	e.app.Config.AI.Profiles["sub"] = p
	e.press("c")
	if e.m.cl == nil || e.m.cl.stage != clFailed {
		t.Fatalf("stage %+v", e.m.cl)
	}
	e.want("omp is not installed", "install it or set ai.mode = off")
	e.wantNot("exec:", "$PATH", "executable file not found")
	es, _ := e.app.Audit.Query(audit.Filter{Action: "ai.classify"})
	if len(es) != 1 || es[0].Outcome != "error" || !strings.Contains(es[0].Error, "executable file not found") {
		t.Fatalf("the raw error belongs in the log: %+v", es)
	}
}

func TestClassifyFailureWordsThePageThatFailed(t *testing.T) {
	e := newClassifyEnv(t)
	e.m.cl = &classifyDialog{ref: trk(11), stage: clFailed, failedAt: clApplying, err: errors.New("403 Forbidden: token scope")}
	e.want("the labels were not added", "try again")
	e.wantNot("403", "token scope")
}

func TestPackFieldsHangsContinuationLinesWithoutALeadingSeparator(t *testing.T) {
	got := packFields(unicodeGlyphs, []string{"claude-sub", "claude-haiku-4-5", "explain", "~620 in / ≤900 out tokens", "est $0.0010", "to omp:anthropic"}, " · ", "  ", 40)
	if len(got) < 3 {
		t.Fatalf("expected a wrap, got %q", got)
	}
	for _, ln := range got {
		if !strings.HasPrefix(ln, "  ") || strings.HasPrefix(strings.TrimSpace(ln), "·") || lipgloss.Width(ln) > 40 {
			t.Fatalf("bad line %q in %q", ln, got)
		}
	}
}

// addClassifyRow puts an item with ref on Home and selects it.
func (e *swEnvT) addClassifyRow(ref forge.Ref, title string) {
	e.t.Helper()
	it := e.item(1, title)
	it.Ref, it.Issue.Ref = ref, ref
	e.m.results[0].Items = append(e.m.results[0].Items, it)
	e.m.rebuildHome("")
	e.selectRow(ref.String())
}

func TestClassifyWorksFromAnAppRepoRow(t *testing.T) {
	e := newClassifyEnv(t)
	api := forge.Ref{Project: grp + "/api", IID: 1, Kind: forge.KindIssue}
	for _, r := range []forge.Ref{api, trk(1)} {
		if err := e.srv.SetLabels(r); err != nil {
			t.Fatal(err)
		}
	}
	e.addClassifyRow(api, "Rate-limit the export endpoint")
	e.press("c")
	if e.m.cl == nil || e.m.cl.stage != clSuggest {
		t.Fatalf("stage = %+v", e.m.cl)
	}
	e.want("Classify · "+api.String(), "Suggested labels")
	e.wantNot("could not read")
}

func TestClassifyCountsTheFacetsOfTheTrackerParent(t *testing.T) {
	e := newClassifyEnv(t)
	api := forge.Ref{Project: grp + "/api", IID: 1, Kind: forge.KindIssue}
	if err := e.srv.SetLabels(api); err != nil { // tracker#1 keeps type::story and app::api
		t.Fatal(err)
	}
	e.addClassifyRow(api, "Rate-limit the export endpoint")
	e.press("c")
	if e.m.cl == nil || e.m.cl.stage != clFailed || !errors.Is(e.m.cl.err, ai.ErrNothingToClassify) {
		t.Fatalf("dialog = %+v", e.m.cl)
	}
	e.want("already has a type and an app label")
	e.wantNot("could not read")
}

func TestClassifyWorksOnAClosedTrackerIssue(t *testing.T) {
	e := newClassifyEnv(t)
	if err := e.srv.SetLabels(trk(10)); err != nil {
		t.Fatal(err)
	}
	e.addClassifyRow(trk(10), "Upgrade export worker dependencies")
	e.press("c")
	if e.m.cl == nil || e.m.cl.stage != clSuggest {
		t.Fatalf("stage = %+v", e.m.cl)
	}
	e.want("Suggested labels", "type::chore")
}

func TestClassifySkipsAManifestThatFailsToLoad(t *testing.T) {
	e := newClassifyEnv(t)
	e.app.Config.Projects = append([]config.ProjectRef{{Source: filepath.Join(t.TempDir(), "gone.ade.toml")}}, e.app.Config.Projects...)
	e.press("c")
	if e.m.cl == nil || e.m.cl.stage != clSuggest {
		t.Fatalf("stage = %+v", e.m.cl)
	}
}

func TestClassifyReadFailureShowsTheError(t *testing.T) {
	e := newClassifyEnv(t)
	e.addClassifyRow(forge.Ref{Project: "other/place", IID: 1, Kind: forge.KindIssue}, "Elsewhere")
	e.press("c")
	if e.m.cl == nil || e.m.cl.stage != clFailed {
		t.Fatalf("dialog = %+v", e.m.cl)
	}
	e.want("could not read the issue: no registered project serves other/place", "press c again")
	e.wantNot("from the forge")
	// The unreadable manifest is named when it is why nothing serves the issue.
	e.m.cl = nil
	e.app.Config.Projects = []config.ProjectRef{{Source: filepath.Join(t.TempDir(), "gone.ade.toml")}}
	e.press("c")
	e.want("1 manifest(s) could not be")
}
