package forge

import (
	"context"
	"time"
)

// Event is one entry of a project's activity feed: who did what to which
// issue or merge request, and when. Action is the forge's own word
// ("opened", "closed", "accepted" for a merge, "commented on", "pushed to").
type Event struct {
	ID         int64     `json:"id"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type,omitempty"` // Issue | MergeRequest | Note | ...
	Target     Ref       `json:"target,omitzero"`       // the issue or MR it concerns, when known
	Title      string    `json:"title,omitempty"`
	Branch     string    `json:"branch,omitempty"` // pushes
	Author     User      `json:"author"`
	At         time.Time `json:"at"`
}

// LabelEvent records a label being added to or removed from an issue.
type LabelEvent struct {
	Label  string    `json:"label"`
	Action string    `json:"action"` // add | remove
	At     time.Time `json:"at"`
}

// Sprint scope sources of a SprintEvent, matching tracker.iterations.source.
const (
	SprintByMilestone = "milestones"
	SprintByIteration = "gitlab-iterations"
)

// SprintEvent records an issue entering or leaving a milestone or iteration.
type SprintEvent struct {
	Source string    `json:"source"` // SprintByMilestone | SprintByIteration
	ID     int64     `json:"id"`     // the milestone or iteration id (domain.Sprint.Key)
	Title  string    `json:"title,omitempty"`
	Action string    `json:"action"` // add | remove
	At     time.Time `json:"at"`
}

// AgileReader is the history a standup and a sprint review need beyond the
// current state of issues: the activity feed and the label and sprint-scope
// timelines of an issue. Drivers implement it next to Forge.
type AgileReader interface {
	// ProjectEvents lists the activity of project strictly between the
	// calendar days of after and before (both UTC dates; zero = unbounded).
	ProjectEvents(ctx context.Context, project string, after, before time.Time) ([]Event, error)
	// LabelEvents lists label changes of an issue, oldest first.
	LabelEvents(ctx context.Context, ref Ref) ([]LabelEvent, error)
	// SprintEvents lists milestone and (where licensed) iteration changes of
	// an issue, oldest first.
	SprintEvents(ctx context.Context, ref Ref) ([]SprintEvent, error)
}
