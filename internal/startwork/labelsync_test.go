package startwork_test

import (
	"context"
	"errors"
	"slices"
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
		{"off", "workflow::in progress", 0, 0, false, false},
	} {
		t.Run(tc.sync, func(t *testing.T) {
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
				pending = pending || ev.Kind == "workflow.move.pending"
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
	applied, errs := startwork.ApplyMoves(context.Background(), h.forge, h.app, "demo-gl", "user", []startwork.Move{bad, good})
	if len(applied) != 1 || applied[0].Ref != good.Ref || len(errs) != 1 {
		t.Fatalf("applied %+v, errs %v", applied, errs)
	}
	if !errors.Is(errs[0], forge.ErrNotFound) {
		t.Errorf("err = %v", errs[0])
	}
}
