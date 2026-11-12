package tui

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// newWeightEnv is Home with tracker#11 selected; the issue carries the labels
// and the weight given.
func newWeightEnv(t *testing.T, labels ...string) *swEnvT {
	t.Helper()
	e := newClassifyEnv(t)
	e.m.results[0].Forge = "sandbox-gitlab"
	if len(labels) > 0 {
		if err := gitlab.New(e.srv.URL, fake.Token).SetLabels(context.Background(), trk(11), labels, nil); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *swEnvT) weightAudit() []audit.Entry {
	e.t.Helper()
	es, err := e.app.Audit.Query(audit.Filter{Action: "forge.issue.weight"})
	if err != nil {
		e.t.Fatal(err)
	}
	return es
}

// closedSample is a closed, weighted, labelled item on Home's data.
func (e *swEnvT) closedSample(iid, w int, title, typ string, apps ...string) {
	wi := e.item(iid, title)
	wi.Issue.State, wi.Stage, wi.Weight, wi.Type, wi.Apps = "closed", domain.StageDone, &w, typ, apps
	e.m.results[0].Items = append(e.m.results[0].Items, wi)
}

func TestWeightDialogSwapsTheLabelAfterShowingTheWrites(t *testing.T) {
	e := newWeightEnv(t, "weight::5")
	e.open(model.openWeight)
	if e.m.wt == nil || e.m.wt.stage != wtInput {
		t.Fatalf("dialog = %+v", e.m.wt)
	}
	e.want("Weight · "+trk(11).String(), "now 5w", "[ _ ]", "Writes")
	e.press("2")
	e.want("[ 2_ ]", "label weight::{value}", "weight 5 → 2 (label: add weight::2; remove weight::5)")
	if got := e.issueLabels(11); !slices.Contains(got, "weight::5") {
		t.Fatalf("typing wrote something: %v", got)
	}
	before := e.writes()
	e.press("enter")
	if e.m.wt.stage != wtDone {
		t.Fatalf("stage = %v err %v", e.m.wt.stage, e.m.wt.err)
	}
	e.want("done", "weight 5 → 2")
	got := e.issueLabels(11)
	if !slices.Contains(got, "weight::2") || slices.Contains(got, "weight::5") {
		t.Errorf("labels = %v, want weight::2 and the old label removed", got)
	}
	if e.writes() <= before {
		t.Error("nothing was written")
	}
	if es := e.weightAudit(); len(es) != 1 || es[0].Target != trk(11).String() || es[0].Params["from"] != "5" || es[0].Params["to"] != "2" || es[0].Outcome != "ok" {
		t.Errorf("audit = %+v", es)
	}
	e.press("esc")
	if e.m.wt != nil {
		t.Fatal("esc closes")
	}
}

func TestWeightDialogClearsWithN(t *testing.T) {
	e := newWeightEnv(t, "weight::3")
	e.open(model.openWeight)
	e.press("n")
	e.want("[ none ]", "weight 3 → none (label: remove weight::3)")
	e.press("enter")
	if got := e.issueLabels(11); slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, "weight::") }) {
		t.Errorf("labels = %v", got)
	}
}

func TestWeightDialogRefusesNonsenseAndWritesNothing(t *testing.T) {
	e := newWeightEnv(t, "weight::3")
	e.open(model.openWeight)
	before := e.writes()
	e.press("enter")
	e.want("type a weight of 1 or more, or n to clear it")
	e.press("0", "enter")
	e.want("type a weight of 1 or more")
	e.press("backspace", "3", "enter")
	e.want("already 3: nothing to write")
	e.press("esc")
	if e.writes() != before || len(e.weightAudit()) != 0 {
		t.Errorf("writes %d -> %d, audit %v", before, e.writes(), e.weightAudit())
	}
}

func TestWeightDialogPrefillsTheSuggestionAndKeepsItOptional(t *testing.T) {
	e := newWeightEnv(t, "type::bug", "app::api")
	e.closedSample(20, 3, "Exports stale totals fix", "bug", "api")
	e.closedSample(21, 5, "Exports totals mismatch", "bug", "api")
	e.closedSample(22, 13, "Exports dashboard dark mode", "story", "api")
	// The suggested type and app are the issue's own labels here.
	e.m.rebuildHome("")
	e.selectRow(trk(11).String())
	e.open(model.openWeight)
	e.want("[ 3_ ]", "suggested 3 · weighted median of 2 similar closed issues", "s suggested")
	e.press("backspace", "8")
	e.want("[ 8_ ]")
	e.press("s")
	e.want("[ 3_ ]")
	e.press("+", "+")
	e.want("[ 5_ ]")
	e.press("-", "-", "-", "-", "-", "-")
	e.want("[ 1_ ]") // never below 1
}

func TestWeightFromClassifyHandsOverTheSuggestion(t *testing.T) {
	e := newWeightEnv(t, "type::bug", "app::api")
	e.closedSample(20, 2, "Exports stale totals fix", "bug", "api")
	e.m.rebuildHome("")
	e.selectRow(trk(11).String())
	e.press("c")
	if e.m.cl == nil || e.m.cl.stage != clSuggest {
		t.Fatalf("classify = %+v", e.m.cl)
	}
	e.want("Suggested weight", "2w", "only one similar issue", "= sets it", "set weight")
	e.wantNot("Suggested labels") // every facet is there already
	e.press("=")
	if e.m.cl != nil || e.m.wt == nil || e.m.wt.input != "2" {
		t.Fatalf("cl=%v wt=%+v", e.m.cl, e.m.wt)
	}
	e.want("Weight · "+trk(11).String(), "[ 2_ ]")
}

func TestWeightDialogShowsAReasonNotAGuessWhenNothingIsComparable(t *testing.T) {
	e := newWeightEnv(t, "type::bug", "app::api")
	e.open(model.openWeight)
	e.want("no weight suggested: no closed issue of the same type and app is similar enough", "[ _ ]")
	e.press("s")
	e.want("nothing to suggest")
}

func TestWeightDialogChildFollowsAndCanBeLeftAlone(t *testing.T) {
	e := newWeightEnv(t)
	api := gitlab.New(e.srv.URL, fake.Token)
	ctx := context.Background()
	child, err := api.CreateIssue(ctx, grp+"/api", forge.IssueDraft{Title: "Child", Description: "Parent: " + trk(11).String(), Labels: []string{"weight::1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := api.LinkIssues(ctx, trk(11), child.Ref, "relates_to"); err != nil {
		t.Fatal(err)
	}
	e.open(model.openWeight)
	e.press("4")
	e.want("child "+child.Ref.String()+" follows the weight", "(child)")
	e.press("c")
	e.want("is left alone")
	e.wantNot("(child) weight")
	e.press("c", "enter")
	if ls, _ := e.srv.IssueLabels(child.Ref); !slices.Contains(ls, "weight::4") || slices.Contains(ls, "weight::1") {
		t.Errorf("child labels = %v", ls)
	}
	if es := e.weightAudit(); len(es) != 2 || es[1].Params["child"] != true {
		t.Errorf("audit = %+v", es)
	}
}

func TestWeightDialogReadFailureIsWordedAndAudited(t *testing.T) {
	e := newWeightEnv(t)
	e.srv.Close()
	e.open(model.openWeight)
	if e.m.wt == nil || e.m.wt.stage != wtFailed {
		t.Fatalf("dialog = %+v", e.m.wt)
	}
	e.want("could not read the issue:", "press = again")
	e.wantNot("from the forge")
	if es := e.weightAudit(); len(es) != 1 || es[0].Outcome != "error" || es[0].Error == "" {
		t.Errorf("audit = %+v", es)
	}
}

func TestWeightAndNotesOpenOnlyOneDialogAtATime(t *testing.T) {
	e := newWeightEnv(t)
	e.open(model.openWeight)
	e.open(model.openNotes)
	if e.m.nt != nil {
		t.Error("notes opened over the weight dialog")
	}
	e.press("esc")
	e.open(model.openNotes)
	e.open(model.openWeight)
	if e.m.wt != nil {
		t.Error("weight opened over the notes dialog")
	}
}

// The synced items cover only the sprint window; the suggestion also uses the
// forge's closed issues (tracker#10 is closed in the seed). The dialog never
// waits for them: the first open starts the read in the background and the
// next one has them from the cache.
func TestWeightSuggestionUsesClosedIssuesFromTheCache(t *testing.T) {
	e := newWeightEnv(t, "type::story", "app::api")
	if err := gitlab.New(e.srv.URL, fake.Token).SetLabels(context.Background(), trk(10), []string{"weight::2"}, nil); err != nil {
		t.Fatal(err)
	}
	// Only the forge knows #10 ("Upgrade export worker dependencies"). The one
	// closed item on Home shares no word with the issue; it makes "export" rarer
	// than the words the two titles have in common, so one shared word counts.
	e.closedSample(23, 8, "Upgrade worker dependencies", "story", "api")
	e.m.rebuildHome("")
	e.open(model.openWeight)
	e.want("no weight suggested")
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.press("esc")
		e.open(model.openWeight)
		if strings.Contains(e.screen(), "suggested 2") {
			break
		}
		if time.Now().After(deadline) {
			e.want("suggested 2 · only one similar issue: " + trk(10).String() + " (2)")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.want("[ 2_ ]", "suggested 2 · only one similar issue: "+trk(10).String()+" (2)")
}

// `=` in the Classify dialog hands the value it just showed to the Weight
// dialog, though the issue has no type label yet to derive it from.
func TestClassifyHandsItsSuggestedWeightToTheWeightDialog(t *testing.T) {
	e := newClassifyEnv(t)
	e.m.results[0].Forge = "sandbox-gitlab"
	e.closedSample(20, 3, "Exports totals drift", "bug")
	e.m.rebuildHome("")
	e.selectRow(trk(11).String())
	e.press("c")
	e.want("Suggested weight", "3w")
	e.press("=")
	if e.m.wt == nil {
		t.Fatal("= did not open the Weight dialog")
	}
	e.want("Weight · "+trk(11).String(), "[ 3_ ]", "suggested 3")
}

func TestWeightDialogShowsWhyTheIssueCouldNotBeRead(t *testing.T) {
	e := newWeightEnv(t)
	it := e.item(1, "Elsewhere")
	it.Ref = forge.Ref{Project: "other/place", IID: 1, Kind: forge.KindIssue}
	e.m.results[0].Items = append(e.m.results[0].Items, it)
	e.m.rebuildHome("")
	e.selectRow(it.Ref.String())
	e.open(model.openWeight)
	if e.m.wt == nil || e.m.wt.stage != wtFailed {
		t.Fatalf("dialog = %+v", e.m.wt)
	}
	e.want("could not read the issue: no registered project serves other/place", "ade project list")
	e.wantNot("check the connection")
}

func TestWeightDialogOpensOnAnAppRepoRowAndSurvivesABrokenManifest(t *testing.T) {
	e := newWeightEnv(t)
	e.app.Config.Projects = append([]config.ProjectRef{{Source: filepath.Join(t.TempDir(), "gone.ade.toml")}}, e.app.Config.Projects...)
	it := e.item(1, "Rate-limit the export endpoint")
	it.Ref = forge.Ref{Project: grp + "/api", IID: 1, Kind: forge.KindIssue}
	e.m.results[0].Items = append(e.m.results[0].Items, it)
	e.m.rebuildHome("")
	e.selectRow(it.Ref.String())
	e.open(model.openWeight)
	if e.m.wt == nil || e.m.wt.stage != wtInput {
		t.Fatalf("dialog = %+v", e.m.wt)
	}
}
