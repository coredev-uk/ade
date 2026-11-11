package gitlab

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

var _ forge.AgileReader = (*Client)(nil)

type wireEvent struct {
	ID          int64     `json:"id"`
	Action      string    `json:"action_name"`
	TargetIID   int       `json:"target_iid"`
	TargetType  string    `json:"target_type"`
	TargetTitle string    `json:"target_title"`
	CreatedAt   time.Time `json:"created_at"`
	Author      wireUser  `json:"author"`
	Note        *struct {
		Type string `json:"noteable_type"` //nolint:misspell // GitLab's field name
		IID  int    `json:"noteable_iid"`  //nolint:misspell // GitLab's field name
	} `json:"note"`
	PushData *struct {
		Ref string `json:"ref"`
	} `json:"push_data"`
}

func (w wireEvent) event(project string) forge.Event {
	e := forge.Event{
		ID: w.ID, Action: w.Action, TargetType: w.TargetType, Title: w.TargetTitle,
		Author: w.Author.user(), At: w.CreatedAt,
	}
	typ, iid := w.TargetType, w.TargetIID
	if w.Note != nil { // Note, DiffNote and DiscussionNote events all name what they comment on
		typ, iid = w.Note.Type, w.Note.IID
	}
	switch typ {
	case "Issue":
		e.Target = forge.Ref{Project: project, IID: iid, Kind: forge.KindIssue}
	case "MergeRequest":
		e.Target = forge.Ref{Project: project, IID: iid, Kind: forge.KindMR}
	}
	if w.PushData != nil {
		e.Branch = w.PushData.Ref
	}
	return e
}

// ProjectEvents implements forge.AgileReader (GET /projects/:id/events). The
// API's after and before are exclusive UTC dates.
func (c *Client) ProjectEvents(ctx context.Context, project string, after, before time.Time) ([]forge.Event, error) {
	q := url.Values{}
	if !after.IsZero() {
		q.Set("after", after.UTC().Format("2006-01-02"))
	}
	if !before.IsZero() {
		q.Set("before", before.UTC().Format("2006-01-02"))
	}
	raw, err := getAll[wireEvent](ctx, c, projectPath(project)+"/events", q, false)
	if err != nil {
		return nil, err
	}
	out := make([]forge.Event, len(raw))
	for i, w := range raw {
		out[i] = w.event(project)
	}
	return out, nil
}

type wireLabelEvent struct {
	CreatedAt time.Time `json:"created_at"`
	Action    string    `json:"action"`
	Label     *struct {
		Name string `json:"name"`
	} `json:"label"` // null once the label was deleted
}

// LabelEvents implements forge.AgileReader (resource_label_events, all tiers).
// Events whose label no longer exists carry no name and are dropped.
func (c *Client) LabelEvents(ctx context.Context, ref forge.Ref) ([]forge.LabelEvent, error) {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return nil, err
	}
	raw, err := getAll[wireLabelEvent](ctx, c, issuePath(ref)+"/resource_label_events", nil, false)
	if err != nil {
		return nil, err
	}
	var out []forge.LabelEvent
	for _, w := range raw {
		if w.Label != nil && w.Label.Name != "" {
			out = append(out, forge.LabelEvent{Label: w.Label.Name, Action: w.Action, At: w.CreatedAt})
		}
	}
	return out, nil
}

type wireSprintEvent struct {
	CreatedAt time.Time `json:"created_at"`
	Action    string    `json:"action"`
	Milestone *struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	} `json:"milestone"`
	Iteration *struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	} `json:"iteration"`
}

// SprintEvents implements forge.AgileReader: milestone events (every tier)
// and iteration events (Premium; unavailable is not an error).
func (c *Client) SprintEvents(ctx context.Context, ref forge.Ref) ([]forge.SprintEvent, error) {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return nil, err
	}
	var ms, its []wireSprintEvent
	err := concurrently(
		func() (err error) {
			ms, err = getAll[wireSprintEvent](ctx, c, issuePath(ref)+"/resource_milestone_events", nil, false)
			return err
		},
		func() error {
			var err error
			its, err = getAll[wireSprintEvent](ctx, c, issuePath(ref)+"/resource_iteration_events", nil, true)
			if isUnavailable(err) {
				return nil
			}
			return err
		},
	)
	if err != nil {
		return nil, err
	}
	var out []forge.SprintEvent
	for _, w := range ms {
		if w.Milestone != nil {
			out = append(out, forge.SprintEvent{Source: forge.SprintByMilestone, ID: w.Milestone.ID, Title: w.Milestone.Title, Action: w.Action, At: w.CreatedAt})
		}
	}
	for _, w := range its {
		if w.Iteration != nil {
			out = append(out, forge.SprintEvent{Source: forge.SprintByIteration, ID: w.Iteration.ID, Title: w.Iteration.Title, Action: w.Action, At: w.CreatedAt})
		}
	}
	sortSprintEvents(out)
	return out, nil
}

func isUnavailable(err error) bool { return errors.Is(err, forge.ErrUnavailable) }

func sortSprintEvents(evs []forge.SprintEvent) {
	slices.SortStableFunc(evs, func(a, b forge.SprintEvent) int { return a.At.Compare(b.At) })
}
