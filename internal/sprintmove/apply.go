package sprintmove

import (
	"context"
	"fmt"
	"strconv"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/startwork"
)

// Action is the audit action of a sprint move.
const Action = "forge.issue.sprint"

// Result is a plan after Apply.
type Result struct {
	Plan    Plan   `json:"plan"`
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
	// Warning is a problem after a good write (the cache could not be updated).
	Warning string `json:"warning,omitempty"`
}

// Apply writes the plans in order, one audited forge.issue.sprint entry per
// write. For every tracker issue moved it raises issue.iteration_changed on the
// bus at once and records the move in the sync cache, so the next sync does not
// raise it again. An unchanged plan writes and audits nothing. It stops at the
// first failure: the results so far, and the failing one, come back with the
// error.
func Apply(ctx context.Context, w startwork.Forge, a *app.App, project, actor string, plans []Plan) ([]Result, error) {
	var out []Result
	for _, p := range plans {
		r := Result{Plan: p}
		if p.Unchanged {
			out = append(out, r)
			continue
		}
		err := write(ctx, w, p)
		ent := audit.Entry{
			Actor: actor, Action: Action, Target: p.Ref.String(), Item: p.Ref.String(), Project: project,
			Params: map[string]any{
				"from": SprintName(p.From), "to": p.To.Title, "mode": p.Mode,
				"add": p.Add, "remove": p.Remove, "child": p.Child,
			},
			Outcome: "ok",
		}
		if err != nil {
			ent.Outcome, ent.Error = "error", err.Error()
			r.Error = err.Error()
		} else {
			r.Applied = true
			r.Warning = announce(a, project, p)
		}
		if a != nil && a.Audit != nil {
			if aerr := a.Audit.Append(ent); aerr != nil && err == nil {
				err = fmt.Errorf("sprint written, but the audit entry failed: %w", aerr)
			}
		}
		out = append(out, r)
		if err != nil {
			return out, fmt.Errorf("%s: %w", p.Ref, err)
		}
	}
	return out, nil
}

// announce raises the event and patches the cache for a moved tracker issue.
// Children are not work items of their own, so they have neither. It returns a
// warning when the cache could not be patched.
func announce(a *app.App, project string, p Plan) string {
	if a == nil || p.Child {
		return ""
	}
	var warn string
	if a.Paths.CacheDir == "" {
		// no cache location (a bare App): nothing to patch
	} else if st, err := a.Cache(); err != nil {
		warn = "the sync cache could not be opened: " + err.Error()
	} else if err := forgesync.RecordSprintMove(st, project, p.Ref, p.To); err != nil {
		warn = err.Error()
	}
	if a.Bus != nil {
		e := events.New(forgesync.EvIterationMoved, events.Info, fmt.Sprintf("%s moved to %s", p.Title, p.To.Title))
		e.Project, e.Subject, e.Link = project, p.Ref.String(), p.URL
		a.Bus.Publish(e)
	}
	return warn
}

func write(ctx context.Context, w startwork.Forge, p Plan) error {
	switch p.Mode {
	case ModeLabel:
		return w.SetLabels(ctx, p.Ref, p.Add, p.Remove)
	case ModeIteration:
		id, err := strconv.ParseInt(p.To.Key, 10, 64)
		if err != nil {
			return fmt.Errorf("iteration key %q is not an id", p.To.Key)
		}
		return w.SetIteration(ctx, p.Ref, id)
	case ModeMilestone:
		mw, ok := w.(forge.MilestoneWriter)
		if !ok {
			return fmt.Errorf("forge %s: %w", w.Kind(), errNoMilestoneWriter)
		}
		id, err := strconv.ParseInt(p.To.Key, 10, 64)
		if err != nil {
			return fmt.Errorf("milestone key %q is not an id", p.To.Key)
		}
		return mw.SetMilestone(ctx, p.Ref, id)
	}
	return fmt.Errorf("unknown sprint mode %q", p.Mode)
}
