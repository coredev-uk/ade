package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// newStageEnv is Home with tracker#1 in review while its label still says
// "in progress", as a sync under labels.workflow.sync = "ask" leaves it.
func newStageEnv(t *testing.T) *swEnvT {
	t.Helper()
	e := newClassifyEnv(t)
	api := gitlab.New(e.srv.URL, fake.Token)
	var drop []string
	for _, l := range e.issueLabels(1) {
		if strings.HasPrefix(l, "workflow::") {
			drop = append(drop, l)
		}
	}
	if err := api.SetLabels(context.Background(), trk(1), []string{"workflow::in progress"}, drop); err != nil {
		t.Fatal(err)
	}
	it := e.item(1, "Rate-limit exports")
	it.Stage, it.Workflow, it.WantWorkflow, it.Drift, it.Movable = domain.StageReview, "in progress", "in review", true, true
	e.m.results[0].Items = append(e.m.results[0].Items, it)
	e.m.rebuildHome("")
	e.selectRow(trk(1).String())
	return e
}

func (e *swEnvT) labelAudit() []audit.Entry {
	e.t.Helper()
	es, err := e.app.Audit.Query(audit.Filter{Action: "forge.issue.labels"})
	if err != nil {
		e.t.Fatal(err)
	}
	return es
}

func TestHomeStageMoveShowsTheMoveThenMakesIt(t *testing.T) {
	e := newStageEnv(t)
	e.want("stage says", "in review", "m moves it")

	e.press("m")
	if e.m.act == nil || e.m.act.stage != adConfirm {
		t.Fatalf("dialog = %+v", e.m.act)
	}
	e.want("Move the workflow label · "+trk(1).String(), "Rate-limit exports",
		"workflow in progress → in review", "add     workflow::in review", "remove  workflow::in progress", "enter move")
	if got := e.issueLabels(1); !slices.Contains(got, "workflow::in progress") || len(e.labelAudit()) != 0 {
		t.Fatalf("showing the move wrote: %v, audit %v", got, e.labelAudit())
	}

	e.press("enter")
	if e.m.act.stage != adDone {
		t.Fatalf("stage = %v err %v", e.m.act.stage, e.m.act.err)
	}
	e.want("moved " + trk(1).String() + ": workflow in progress → in review")
	got := e.issueLabels(1)
	if !slices.Contains(got, "workflow::in review") || slices.Contains(got, "workflow::in progress") {
		t.Fatalf("labels after the move: %v", got)
	}
	if es := e.labelAudit(); len(es) != 1 || es[0].Target != trk(1).String() || es[0].Params["reason"] != "workflow stage" || es[0].Outcome != "ok" {
		t.Fatalf("audit = %+v", es)
	}
	e.press("esc")
	if e.m.act != nil {
		t.Fatal("esc closes")
	}
}

func TestHomeStageMoveEscapeWritesNothingAndAMatchingLabelSaysSo(t *testing.T) {
	e := newStageEnv(t)
	before := e.writes()
	e.press("m", "esc")
	if e.m.act != nil || e.writes() != before || len(e.labelAudit()) != 0 {
		t.Fatalf("dialog %v, writes %d -> %d, audit %v", e.m.act, before, e.writes(), e.labelAudit())
	}

	// Somebody set the label by hand since the last sync: the dialog reads the
	// issue again and finds nothing to move.
	api := gitlab.New(e.srv.URL, fake.Token)
	if err := api.SetLabels(context.Background(), trk(1), []string{"workflow::in review"}, []string{"workflow::in progress"}); err != nil {
		t.Fatal(err)
	}
	before = e.writes()
	e.press("m")
	e.want("Nothing to move: the workflow label already says in review.")
	e.wantNot("enter move")
	e.press("enter", "esc")
	if e.m.act != nil || e.writes() != before || len(e.labelAudit()) != 0 {
		t.Fatalf("dialog %v, writes %d -> %d, audit %v", e.m.act, before, e.writes(), e.labelAudit())
	}
}

func TestHomeStageMoveOffersTheKeyOnlyWhenAMoveApplies(t *testing.T) {
	e := newStageEnv(t)
	e.want("m moves it")
	// Drift ADE will not fix (the label is the team's done value) shows the drift without the key.
	for i := range e.m.results[0].Items {
		if e.m.results[0].Items[i].Ref == trk(1) {
			e.m.results[0].Items[i].Movable = false
		}
	}
	e.m.rebuildHome("")
	e.selectRow(trk(1).String())
	e.want("stage says")
	e.wantNot("m moves it")
}

func TestActionDialogClosedWhileWritingStillRefreshes(t *testing.T) {
	e := newStageEnv(t)
	e.press("m")
	d := e.m.act
	refreshed := false
	d.plan.after = func(model) tea.Cmd { refreshed = true; return nil }
	d.stage = adApplying
	e.press("esc")
	if e.m.act != nil {
		t.Fatal("esc closes the dialog")
	}
	next, _ := e.m.Update(adAppliedMsg{d: d, lines: []string{"moved"}})
	e.m = next.(model)
	if !refreshed {
		t.Fatal("a change made after the dialog was closed was not read again")
	}
}
