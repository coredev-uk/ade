package startwork_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/startwork"
)

// tracker#2 carries workflow::in progress; its MR just went ready.
func advancedItem(h *harness) (prev, cur domain.WorkItem) {
	ref := issueRef(tracker, 2)
	is, _ := h.forge.Issue(context.Background(), ref)
	prev = domain.WorkItem{Ref: ref, Issue: is, Stage: domain.StageDraft, Workflow: "in progress", WantWorkflow: "in progress"}
	cur = prev
	cur.Stage, cur.WantWorkflow = domain.StageReview, "in review"
	return prev, cur
}

func TestSyncWorkflowApplyAskOff(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		sync        string
		wantLabel   string
		applied     int
		pending     int
		wantAudit   bool
		wantPending bool
	}{
		{"auto", "workflow::in review", 1, 0, true, false},
		{"ask", "workflow::in progress", 0, 1, false, true},
		{"", "workflow::in progress", 0, 1, false, true}, // unset means ask
		{"off", "workflow::in progress", 0, 0, false, false},
	} {
		t.Run("sync="+tc.sync, func(t *testing.T) {
			h := newHarness(t, fake.Free)
			h.m.Labels.Workflow.Sync = tc.sync
			prev, cur := advancedItem(h)
			out := startwork.SyncWorkflow(ctx, h.forge, h.app, h.m, []domain.WorkItem{prev}, []domain.WorkItem{cur})
			if err := out.Err(); err != nil {
				t.Fatal(err)
			}
			if len(out.Applied) != tc.applied || len(out.Pending) != tc.pending {
				t.Errorf("applied %d pending %d, want %d / %d", len(out.Applied), len(out.Pending), tc.applied, tc.pending)
			}
			labels, _ := h.srv.IssueLabels(issueRef(tracker, 2))
			if !slices.Contains(labels, tc.wantLabel) {
				t.Errorf("labels = %v, want %s", labels, tc.wantLabel)
			}
			if tc.applied == 1 && slices.Contains(labels, "workflow::in progress") {
				t.Errorf("old workflow label was not removed: %v", labels)
			}
			if got := slices.Contains(h.auditActions(), "forge.issue.labels"); got != tc.wantAudit {
				t.Errorf("audited = %v, want %v", got, tc.wantAudit)
			}
			var pending bool
			for _, ev := range h.drain() {
				if ev.Kind == "workflow.move.pending" {
					pending = true
					if !strings.Contains(ev.Detail, "workflow::in review") || !strings.Contains(ev.Detail, `sync = "auto"`) {
						t.Errorf("pending detail = %q, want the label and how to apply it", ev.Detail)
					}
				}
			}
			if pending != tc.wantPending {
				t.Errorf("pending event = %v, want %v", pending, tc.wantPending)
			}
		})
	}
}

func TestApplyMovesReportsFailuresAndContinues(t *testing.T) {
	h := newHarness(t, fake.Free)
	good := startwork.Move{Ref: issueRef(tracker, 2), To: "in review", Add: "workflow::in review", Remove: []string{"workflow::in progress"}}
	bad := startwork.Move{Ref: issueRef(tracker, 999), To: "in review", Add: "workflow::in review"}
	applied, errs := startwork.ApplyMoves(context.Background(), h.forge, h.app, "demo-gl", "user", "workflow stage", []startwork.Move{bad, good})
	if len(applied) != 1 || applied[0].Ref != good.Ref || len(errs) != 1 {
		t.Fatalf("applied %+v, errs %v", applied, errs)
	}
	if !errors.Is(errs[0], forge.ErrNotFound) {
		t.Errorf("err = %v", errs[0])
	}
}

func TestPendingMoveJudgesTheItemAsItStandsAndSaysWhyNot(t *testing.T) {
	m := manifest(t, manifestTOML)
	mk := func(stage domain.Stage, wf string, labels ...string) domain.WorkItem {
		return domain.WorkItem{
			Ref: issueRef(tracker, 3), Stage: stage, Workflow: wf, WantWorkflow: domain.WantWorkflow(stage, m.Labels),
			Issue: forge.Issue{Ref: issueRef(tracker, 3), Labels: labels},
		}
	}
	// No earlier state is needed, and sync = "off" does not stop a move asked for.
	m.Labels.Workflow.Sync = "off"
	mv, why := startwork.MoveReason(mk(domain.StageReview, "in progress", "workflow::in progress"), m)
	if why != "" || mv.To != "in review" || mv.Add != "workflow::in review" || !slices.Equal(mv.Remove, []string{"workflow::in progress"}) {
		t.Fatalf("move = %+v, why %q", mv, why)
	}
	if got := mv.Writes(); got != "add workflow::in review, remove workflow::in progress" {
		t.Errorf("writes = %q", got)
	}
	for _, tc := range []struct {
		name string
		it   domain.WorkItem
		want string
	}{
		{"already there", mk(domain.StageReview, "in review", "workflow::in review"), "already says in review"},
		{"blocked", mk(domain.StageReview, "blocked", "workflow::blocked"), "blocked"},
		{"done label", mk(domain.StageReview, "done", "workflow::done"), "never moves it back"},
		{"finished item", mk(domain.StageDone, "in review", "workflow::in review"), "item is done"},
	} {
		if _, ok := startwork.PendingMove(tc.it, m); ok {
			t.Errorf("%s: moved", tc.name)
		}
		if _, why := startwork.MoveReason(tc.it, m); !strings.Contains(why, tc.want) {
			t.Errorf("%s: why = %q, want %q", tc.name, why, tc.want)
		}
	}
}
