package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/agile"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/sprintmove"
)

// newMoveEnv is Home with tracker#1 (child api#1), #3 and #7 added; the fake
// forge's clock and the dialog's are 2026-11-11, inside Sprint 42.
func newMoveEnv(t *testing.T) *swEnvT {
	t.Helper()
	e := newClassifyEnv(t)
	e.m.sync.Now = func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }
	for _, n := range []int{1, 7} {
		e.m.results[0].Items = append(e.m.results[0].Items, e.item(n, "Item "+string(rune('0'+n))))
	}
	e.m.rebuildHome("")
	e.selectRow(trk(1).String())
	return e
}

func (e *swEnvT) milestoneOf(project string, iid int) string {
	e.t.Helper()
	is, err := gitlab.New(e.srv.URL, fake.Token).Issue(context.Background(), forge.Ref{Project: project, IID: iid, Kind: forge.KindIssue})
	if err != nil {
		e.t.Fatal(err)
	}
	if is.Milestone == nil {
		return ""
	}
	return is.Milestone.Title
}

// puts counts the issue updates the forge has seen.
func (e *swEnvT) puts() int {
	n := 0
	for _, r := range e.srv.Requests() {
		if r.Method == "PUT" {
			n++
		}
	}
	return n
}

func (e *swEnvT) sprintAudit() []audit.Entry {
	e.t.Helper()
	es, err := e.app.Audit.Query(audit.Filter{Action: sprintmove.Action})
	if err != nil {
		e.t.Fatal(err)
	}
	return es
}

func TestSprintMoveDialogListsTheWritesThenMovesTheItemAndItsChild(t *testing.T) {
	e := newMoveEnv(t)
	e.press(">")
	if e.m.sm == nil || e.m.sm.stage != smConfirm {
		t.Fatalf("dialog = %+v", e.m.sm)
	}
	e.want("Move to the next sprint · "+trk(1).String(), "Moves", "Sprint 43 · milestone",
		trk(1).String()+": Sprint 42 → Sprint 43", grp+"/api#1 (child): Sprint 42 → Sprint 43",
		"child issues that carry the same sprint follow", "c children", "enter move")
	if got := e.milestoneOf(grp+"/tracker", 1); got != "Sprint 42" {
		t.Fatalf("showing the plan wrote: milestone = %q", got)
	}

	// c leaves the child alone and the list says so.
	e.press("c")
	e.want("child issues are left alone")
	e.wantNot("(child)")
	e.press("c")
	e.want("(child)")

	before := e.puts()
	e.press("enter")
	if e.m.sm.stage != smDone {
		t.Fatalf("stage = %v err %v", e.m.sm.stage, e.m.sm.err)
	}
	e.want("done", "ADE moves issues only: the project lead closes them.")
	for _, ref := range []struct {
		p   string
		iid int
	}{{grp + "/tracker", 1}, {grp + "/api", 1}} {
		if got := e.milestoneOf(ref.p, ref.iid); got != "Sprint 43" {
			t.Errorf("%s#%d milestone = %q, want Sprint 43", ref.p, ref.iid, got)
		}
	}
	if e.puts() != before+2 {
		t.Errorf("issue updates %d -> %d, want the issue and its child", before, e.puts())
	}
	if es := e.sprintAudit(); len(es) != 2 || es[0].Target != trk(1).String() || es[0].Params["to"] != "Sprint 43" || es[0].Outcome != "ok" {
		t.Errorf("audit = %+v", es)
	}
	e.press("esc")
	if e.m.sm != nil {
		t.Fatal("esc closes")
	}
}

func TestSprintMoveWithNoNextSprintSaysWhatToCreate(t *testing.T) {
	e := newMoveEnv(t)
	e.selectRow(trk(7).String()) // Sprint 43: the last one
	before := e.puts()
	e.press(">")
	e.want("no sprint starts after Sprint 43")
	if err := e.m.sm.entries[0].err; err == nil || !strings.Contains(err.Error(), "create the next milestone in group "+grp) || !strings.Contains(err.Error(), "ADE creates no sprints") {
		t.Errorf("entry error = %v, want the fix", err)
	}
	e.press("enter")
	e.want("nothing to move")
	e.press("esc")
	if e.m.sm != nil || e.puts() != before || len(e.sprintAudit()) != 0 {
		t.Errorf("dialog %v, updates %d -> %d, audit %v", e.m.sm, before, e.puts(), e.sprintAudit())
	}
}

func TestSprintMoveEscapeWritesNothing(t *testing.T) {
	e := newMoveEnv(t)
	before := e.puts()
	e.press(">", "esc")
	if e.m.sm != nil || e.puts() != before || len(e.sprintAudit()) != 0 {
		t.Errorf("dialog %v, updates %d -> %d", e.m.sm, before, e.puts())
	}
}

func reviewModel(e *swEnvT) {
	ref := trk
	e.m.ag = agileUI{kind: agileReview, review: &agile.Review{
		Project: "demo-gl", Basis: agile.BasisCount,
		Sprint: domain.Sprint{Title: "Sprint 42", Start: forge.NewDate(2026, 11, 2), Due: forge.NewDate(2026, 11, 13)},
		Items: []agile.ReviewItem{
			{Ref: ref(4), Title: "Finished item", Done: true, Assigned: true},
			{Ref: ref(1), Title: "Carried first", Assigned: true},
			{Ref: ref(3), Title: "Carried second", Assigned: true},
			{Ref: ref(5), Title: "Someone else's", Assigned: false},
		},
	}}
}

func TestSprintReviewMovesTheSelectedCarriedItemAndAllOfMine(t *testing.T) {
	e := newMoveEnv(t)
	reviewModel(e)
	e.want("▸ carried") // the first carried item is selected from the start
	if sel, _ := e.m.selectedCarried(); sel.Ref != trk(1) {
		t.Fatalf("selected = %v, want tracker#1", sel.Ref)
	}
	e.press("J")
	if sel, _ := e.m.selectedCarried(); sel.Ref != trk(3) {
		t.Fatalf("after J selected = %v, want tracker#3", sel.Ref)
	}
	e.press("J", "J", "J") // clamps on the last carried item
	if sel, _ := e.m.selectedCarried(); sel.Ref != trk(5) {
		t.Fatalf("after more J selected = %v, want tracker#5", sel.Ref)
	}
	e.press("K")

	// > moves the selected one only.
	e.press(">")
	if e.m.sm == nil || len(e.m.sm.refs) != 1 || e.m.sm.refs[0] != trk(3) {
		t.Fatalf("dialog = %+v, want tracker#3 alone", e.m.sm)
	}
	e.want(trk(3).String() + ": Sprint 42 → Sprint 43")
	e.press("esc")

	// M lists every unfinished item of mine (not the done one, not someone else's).
	e.press("M")
	if e.m.sm == nil || len(e.m.sm.refs) != 2 {
		t.Fatalf("dialog = %+v, want two items", e.m.sm)
	}
	e.want("2 unfinished items of yours in Sprint 42", trk(1).String()+": Sprint 42 → Sprint 43", trk(3).String()+": Sprint 42 → Sprint 43")
	e.wantNot(trk(4).String()+": ", trk(5).String()+": ")
	e.press("enter")
	if e.m.sm.stage != smDone {
		t.Fatalf("stage = %v err %v", e.m.sm.stage, e.m.sm.err)
	}
	for _, iid := range []int{1, 3} {
		if got := e.milestoneOf(grp+"/tracker", iid); got != "Sprint 43" {
			t.Errorf("tracker#%d milestone = %q, want Sprint 43", iid, got)
		}
	}
	if got := e.milestoneOf(grp+"/tracker", 5); got != "Sprint 42" {
		t.Errorf("someone else's item moved: %q", got)
	}
	e.press("esc")
}

func TestSprintMoveKeysDoNothingOnPagesWithoutItems(t *testing.T) {
	e := newMoveEnv(t)
	e.m.ag = agileUI{kind: agileStandup}
	e.press(">")
	if e.m.sm != nil {
		t.Error("> on Standup opened the dialog")
	}
	e.m.ag = agileUI{kind: agileReview, review: &agile.Review{Project: "demo-gl", Items: []agile.ReviewItem{{Ref: trk(4), Done: true}}}}
	e.press(">", "M")
	if e.m.sm != nil {
		t.Error("the review page with nothing carried opened the dialog")
	}
	if strings.Contains(e.screen(), "▸ carried") {
		t.Error("a selection is shown with nothing carried")
	}
}
