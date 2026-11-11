package github

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

type wireEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Actor     wireUser  `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
	Payload   struct {
		Action string `json:"action"`
		Ref    string `json:"ref"`
		Issue  *struct {
			Number      int       `json:"number"`
			Title       string    `json:"title"`
			PullRequest *struct{} `json:"pull_request"`
		} `json:"issue"`
		PullRequest *struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			Merged bool   `json:"merged"`
		} `json:"pull_request"`
		Review *struct {
			State string `json:"state"`
		} `json:"review"`
	} `json:"payload"`
}

// event converts one activity-feed entry. ok is false for event types ADE has
// no word for (watching, forking, wiki edits, ...). Actions use GitLab's
// vocabulary so consumers read both forges alike: a merged pull request is
// "accepted".
func (w wireEvent) event(project string) (e forge.Event, ok bool) {
	id, _ := strconv.ParseInt(w.ID, 10, 64)
	e = forge.Event{ID: id, Author: w.Actor.user(), At: w.CreatedAt}
	p := w.Payload
	issueRef := func() {
		if p.Issue == nil {
			return
		}
		kind := forge.KindIssue
		if p.Issue.PullRequest != nil {
			kind = forge.KindMR
		}
		e.Target = forge.Ref{Project: project, IID: p.Issue.Number, Kind: kind}
		e.Title = p.Issue.Title
	}
	switch w.Type {
	case "IssuesEvent":
		if p.Issue == nil {
			return e, false
		}
		e.Action, e.TargetType = p.Action, "Issue"
		e.Target = forge.Ref{Project: project, IID: p.Issue.Number, Kind: forge.KindIssue}
		e.Title = p.Issue.Title
	case "PullRequestEvent":
		if p.PullRequest == nil {
			return e, false
		}
		e.Action, e.TargetType = p.Action, "MergeRequest"
		if p.Action == "closed" && p.PullRequest.Merged {
			e.Action = "accepted"
		}
		e.Target = forge.Ref{Project: project, IID: p.PullRequest.Number, Kind: forge.KindMR}
		e.Title = p.PullRequest.Title
	case "PushEvent":
		e.Action, e.Branch = "pushed to", strings.TrimPrefix(p.Ref, "refs/heads/")
	case "IssueCommentEvent":
		e.Action, e.TargetType = "commented on", "Note"
		issueRef()
	case "PullRequestReviewCommentEvent", "PullRequestReviewEvent":
		e.Action, e.TargetType = "commented on", "Note"
		if w.Type == "PullRequestReviewEvent" && p.Review != nil && strings.EqualFold(p.Review.State, "approved") {
			e.Action, e.TargetType = "approved", "MergeRequest"
		}
		if p.PullRequest != nil {
			e.Target = forge.Ref{Project: project, IID: p.PullRequest.Number, Kind: forge.KindMR}
			e.Title = p.PullRequest.Title
		}
	default:
		return e, false
	}
	return e, true
}

// ProjectEvents implements forge.AgileReader through the repository events
// feed. GitHub keeps only the last 90 days (and at most 300 events) there; the
// feed is newest first and listing stops once it is older than after. after
// and before are exclusive UTC calendar days, as for GitLab.
func (c *Client) ProjectEvents(ctx context.Context, project string, after, before time.Time) ([]forge.Event, error) {
	day := func(t time.Time) time.Time {
		u := t.UTC()
		return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	}
	var stop func([]wireEvent) bool
	if !after.IsZero() {
		stop = func(page []wireEvent) bool {
			return len(page) > 0 && !day(page[len(page)-1].CreatedAt).After(day(after))
		}
	}
	raw, err := getPages(ctx, c, repoPath(project)+"/events", nil, false, stop, decodeList[wireEvent])
	if err != nil {
		return nil, err
	}
	var out []forge.Event
	for _, w := range raw {
		d := day(w.CreatedAt)
		if (!after.IsZero() && !d.After(day(after))) || (!before.IsZero() && !d.Before(day(before))) {
			continue
		}
		if e, ok := w.event(project); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

type wireIssueEvent struct {
	Event     string    `json:"event"`
	CreatedAt time.Time `json:"created_at"`
	Label     *struct {
		Name string `json:"name"`
	} `json:"label"`
	Milestone *struct {
		Title string `json:"title"`
	} `json:"milestone"`
}

func (c *Client) issueEvents(ctx context.Context, ref forge.Ref) ([]wireIssueEvent, error) {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return nil, err
	}
	return getAll[wireIssueEvent](ctx, c, issuePath(ref)+"/events", nil, false)
}

// LabelEvents implements forge.AgileReader (the issue events feed).
func (c *Client) LabelEvents(ctx context.Context, ref forge.Ref) ([]forge.LabelEvent, error) {
	raw, err := c.issueEvents(ctx, ref)
	if err != nil {
		return nil, err
	}
	var out []forge.LabelEvent
	for _, w := range raw {
		if w.Label == nil || w.Label.Name == "" {
			continue
		}
		switch w.Event {
		case "labeled":
			out = append(out, forge.LabelEvent{Label: w.Label.Name, Action: "add", At: w.CreatedAt})
		case "unlabeled":
			out = append(out, forge.LabelEvent{Label: w.Label.Name, Action: "remove", At: w.CreatedAt})
		}
	}
	return out, nil
}

// SprintEvents implements forge.AgileReader: milestone changes. The feed names
// the milestone by title only, so ids are looked up among the issue's
// repository's milestones (0 for one that was deleted since).
func (c *Client) SprintEvents(ctx context.Context, ref forge.Ref) ([]forge.SprintEvent, error) {
	raw, err := c.issueEvents(ctx, ref)
	if err != nil {
		return nil, err
	}
	var evs []wireIssueEvent
	for _, w := range raw {
		if w.Milestone != nil && (w.Event == "milestoned" || w.Event == "demilestoned") {
			evs = append(evs, w)
		}
	}
	if len(evs) == 0 {
		return nil, nil
	}
	ms, err := c.repoMilestones(ctx, ref.Project)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]int64, len(ms))
	for _, m := range ms {
		ids[m.Title] = m.ID
	}
	out := make([]forge.SprintEvent, len(evs))
	for i, w := range evs {
		action := "add"
		if w.Event == "demilestoned" {
			action = "remove"
		}
		out[i] = forge.SprintEvent{
			Source: forge.SprintByMilestone, ID: ids[w.Milestone.Title], Title: w.Milestone.Title, Action: action, At: w.CreatedAt,
		}
	}
	return out, nil
}
