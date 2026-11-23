package startwork

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
)

// blockedValue is the workflow value a person sets deliberately; it is never
// overwritten by a derived move (the domain treats it as no drift too).
const blockedValue = "blocked"

// Move is one workflow label change: the tracker item's derived stage changed
// and its label no longer says so.
type Move struct {
	Ref    forge.Ref    `json:"ref"`
	Stage  domain.Stage `json:"stage"`
	From   string       `json:"from,omitempty"` // current workflow value
	To     string       `json:"to"`             // value the stage maps to
	Add    string       `json:"add,omitempty"`  // label to add (empty when already there)
	Remove []string     `json:"remove,omitempty"`
}

func (mv Move) String() string {
	from := mv.From
	if from == "" {
		from = "none"
	}
	return fmt.Sprintf("%s: workflow %s → %s", mv.Ref, from, mv.To)
}

// LabelMoves returns the label moves the change from prev to cur calls for
// (labels.workflow.sync, PLAN 3):
//
//   - edge-triggered: only items whose derived stage changed since prev move,
//     so a label somebody set by hand stays until the next transition and
//     meanwhile shows as drift;
//   - the old workflow labels are removed explicitly, since "::" labels are
//     not exclusive on Free;
//   - an item carrying labels.workflow.done never moves back, nor does a
//     deliberate "blocked".
//
// It is pure: callers apply the moves (auto) or offer them (ask).
func LabelMoves(prev, cur []domain.WorkItem, m *config.Manifest) []Move {
	wf := m.Labels.Workflow
	if wf.SyncMode() == "off" || wf.Pattern == "" {
		return nil
	}
	before := make(map[forge.Ref]domain.Stage, len(prev))
	for _, it := range prev {
		before[it.Ref] = it.Stage
	}
	var out []Move
	for _, it := range cur {
		old, seen := before[it.Ref]
		if !seen || old == it.Stage {
			continue
		}
		if mv, ok := PendingMove(it, m); ok {
			out = append(out, mv)
		}
	}
	return out
}

// PendingMove is the move that brings the item's workflow label in line with
// its derived stage, judged on the item as it stands: no earlier state is
// needed. It is what `ade issue stage` and Home's `m` offer, and what an
// edge-triggered sync move is made of. ok is false when there is nothing to
// move; MoveReason says why.
func PendingMove(it domain.WorkItem, m *config.Manifest) (Move, bool) {
	mv, _, ok := moveFor(it, m)
	return mv, ok
}

// MoveReason is PendingMove with the reason there is no move, worded for a
// person ("" when there is one).
func MoveReason(it domain.WorkItem, m *config.Manifest) (Move, string) {
	mv, why, _ := moveFor(it, m)
	return mv, why
}

func moveFor(it domain.WorkItem, m *config.Manifest) (Move, string, bool) {
	wf := m.Labels.Workflow
	switch {
	case wf.Pattern == "":
		return Move{}, "the manifest sets no labels.workflow.pattern, so ADE has no workflow label to move", false
	case it.KeptOpen || it.Stage == domain.StageDone:
		return Move{}, "the item is done: ADE never moves a finished item's label", false
	}
	want := it.WantWorkflow
	switch {
	case want == "":
		return Move{}, fmt.Sprintf("labels.workflow.stage maps no workflow value to stage %s", it.Stage), false
	case strings.EqualFold(it.Workflow, want):
		return Move{}, fmt.Sprintf("the workflow label already says %s", want), false
	case wf.Done != "" && strings.EqualFold(it.Workflow, wf.Done):
		return Move{}, fmt.Sprintf("the workflow label says %s, the team's done value: ADE never moves it back", it.Workflow), false
	case strings.EqualFold(it.Workflow, blockedValue):
		return Move{}, "the workflow label says blocked, which somebody set deliberately: ADE never overwrites it", false
	}
	wantLabel := domain.WorkflowLabel(want, m.Labels)
	mv := Move{Ref: it.Ref, Stage: it.Stage, From: it.Workflow, To: want}
	has := false
	for _, l := range it.Issue.Labels {
		v, ok := facetValue(wf.Pattern, l)
		switch {
		case !ok:
		case strings.EqualFold(v, want):
			has = true
		default:
			mv.Remove = append(mv.Remove, l)
		}
	}
	if !has {
		mv.Add = wantLabel
	}
	return mv, "", true
}

// Outcome is what SyncWorkflow did.
type Outcome struct {
	Applied []Move `json:"applied,omitempty"`
	// Pending are moves left alone because sync is "ask" (the default); they
	// are reported to the user; `ade issue stage` and Home's `m` apply one on request.
	Pending []Move  `json:"pending,omitempty"`
	Errs    []error `json:"-"`
}

// SyncWorkflow is the hook forgesync calls after a project sync: it derives
// the moves between the previous and the new items and, per
// labels.workflow.sync, applies them ("auto"), raises a pending event ("ask",
// also when unset) or does nothing ("off"). Every applied move is audited;
// failures are returned in Outcome.Errs, never fatal to the sync.
func SyncWorkflow(ctx context.Context, w forge.IssueWriter, a *app.App, m *config.Manifest, prev, cur []domain.WorkItem) Outcome {
	moves := LabelMoves(prev, cur, m)
	var out Outcome
	if len(moves) == 0 {
		return out
	}
	if m.Labels.Workflow.SyncMode() == "ask" {
		out.Pending = moves
		for _, mv := range moves {
			ev := events.New("workflow.move.pending", events.Info, mv.String()+" (not moved)")
			ev.Project, ev.Subject = m.Name, mv.Ref.String()
			ev.Detail = PendingAdvice(mv, m.Labels)
			a.Bus.Publish(ev)
		}
		return out
	}
	out.Applied, out.Errs = ApplyMoves(ctx, w, a, m.Name, "ade:sync", "workflow sync", moves)
	return out
}

// PendingAdvice says how to make a move that was left alone: `ade issue stage`
// or Home's `m` applies it (preview first), or the manifest switches to
// "auto" to move labels on sync.
func PendingAdvice(mv Move, labels config.Labels) string {
	return fmt.Sprintf("labels.workflow.sync is %q: run `ade issue stage %s` (or press m on Home) to set %s, or set sync = \"auto\" in the manifest to move labels on sync",
		labels.Workflow.SyncMode(), mv.Ref, domain.WorkflowLabel(mv.To, labels))
}

// Writes names the label changes of the move ("add workflow::in review,
// remove workflow::in progress").
func (mv Move) Writes() string {
	var parts []string
	if mv.Add != "" {
		parts = append(parts, "add "+mv.Add)
	}
	if len(mv.Remove) > 0 {
		parts = append(parts, "remove "+strings.Join(mv.Remove, ", "))
	}
	return strings.Join(parts, ", ")
}

// ApplyMoves writes the moves, one audited update each (reason says who asked:
// "workflow sync" or "workflow stage"), and publishes an event for every move
// made. It carries on past a failing move.
func ApplyMoves(ctx context.Context, w forge.IssueWriter, a *app.App, project, actor, reason string, moves []Move) (applied []Move, errs []error) {
	for _, mv := range moves {
		var add []string
		if mv.Add != "" {
			add = []string{mv.Add}
		}
		err := w.SetLabels(ctx, mv.Ref, add, mv.Remove)
		ent := audit.Entry{
			Actor: actor, Action: "forge.issue.labels", Target: mv.Ref.String(), Item: mv.Ref.String(), Project: project,
			Params:  map[string]any{"add": add, "remove": mv.Remove, "reason": reason, "stage": mv.Stage.String()},
			Outcome: "ok",
		}
		if err != nil {
			ent.Outcome, ent.Error = "error", err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", mv, err))
		}
		_ = a.Audit.Append(ent)
		if err != nil {
			continue
		}
		applied = append(applied, mv)
		ev := events.New("workflow.moved", events.Info, mv.String())
		ev.Project, ev.Subject = project, mv.Ref.String()
		ev.Transient = true // status, not news: the board column follows the work
		a.Bus.Publish(ev)
	}
	return applied, errs
}

// Err joins the errors of an Outcome.
func (o Outcome) Err() error { return errors.Join(o.Errs...) }
