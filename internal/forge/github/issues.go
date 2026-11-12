package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// Me implements forge.Forge. The answer is kept: it cannot change for a token.
func (c *Client) Me(ctx context.Context) (forge.User, error) {
	c.mu.Lock()
	me := c.me
	c.mu.Unlock()
	if me != nil {
		return *me, nil
	}
	var u wireUser
	if err := c.getJSON(ctx, "/user", nil, false, &u); err != nil {
		return forge.User{}, err
	}
	out := u.user()
	c.mu.Lock()
	c.me = &out
	c.users[out.ID] = out.Username
	c.mu.Unlock()
	return out, nil
}

// Capabilities implements forge.Forge. GitHub's free features are the same
// for every owner: no issue weight, iterations, cadences, epics, approval
// rules or blocks links. The owner is read so an unknown or unreachable one
// is an error rather than an empty answer.
func (c *Client) Capabilities(ctx context.Context, group, _ string) (forge.Caps, error) {
	if group == "" {
		return forge.Caps{}, errors.New("github: Capabilities needs an owner")
	}
	var owner struct{}
	if err := c.getJSON(ctx, "/users/"+url.PathEscape(group), nil, false, &owner); err != nil {
		return forge.Caps{}, err
	}
	return forge.Caps{ProbedAt: c.now()}, nil
}

// ListIssues implements forge.Forge. q.Project is required. GitHub lists pull
// requests as issues; they are dropped here. q.IterationID has no meaning on
// GitHub and is refused rather than ignored: ignoring it would return every
// issue.
func (c *Client) ListIssues(ctx context.Context, q forge.IssueQuery) ([]forge.Issue, error) {
	if q.Project == "" {
		return nil, errors.New("github: ListIssues needs a project")
	}
	if q.IterationID != 0 {
		return nil, fmt.Errorf("github: iterations: %w", forge.ErrUnavailable)
	}
	v := url.Values{"sort": {"updated"}, "direction": {"desc"}}
	switch s := stateOr(q.State, "opened"); s {
	case "opened":
		v.Set("state", "open")
	case "closed", "all":
		v.Set("state", s)
	default:
		return nil, fmt.Errorf("github: unknown issue state %q", s)
	}
	if q.Assignee != "" {
		v.Set("assignee", q.Assignee)
	}
	if len(q.Labels) > 0 {
		v.Set("labels", strings.Join(q.Labels, ","))
	}
	if !q.UpdatedAfter.IsZero() {
		v.Set("since", q.UpdatedAfter.UTC().Format("2006-01-02T15:04:05Z"))
	}
	if q.Milestone != "" {
		n, ok, err := c.milestoneNumber(ctx, q.Project, q.Milestone)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil // no such milestone, so nothing is in it
		}
		v.Set("milestone", strconv.Itoa(n))
	}
	raw, err := getAll[wireIssue](ctx, c, repoPath(q.Project)+"/issues", v, false)
	if err != nil {
		return nil, err
	}
	out := make([]forge.Issue, 0, len(raw))
	for _, w := range raw {
		if w.PullRequest != nil {
			continue
		}
		is := w.issue(q.Project)
		c.noteMilestone(is.Milestone)
		out = append(out, is)
	}
	return out, nil
}

// Issue implements forge.Forge. A pull request's number is not an issue.
func (c *Client) Issue(ctx context.Context, ref forge.Ref) (forge.Issue, error) {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return forge.Issue{}, err
	}
	var w wireIssue
	if err := c.getJSON(ctx, issuePath(ref), nil, false, &w); err != nil {
		return forge.Issue{}, err
	}
	if w.PullRequest != nil {
		return forge.Issue{}, fmt.Errorf("github: %s is a pull request, not an issue: %w", ref, forge.ErrNotFound)
	}
	is := w.issue(ref.Project)
	c.noteMilestone(is.Milestone)
	return is, nil
}

// Links implements forge.Forge: the issue's sub-issues ("sub_issue") and its
// parent ("parent"), which GitHub keeps natively and across repositories.
// Plain issue-to-issue references are not links on GitHub.
func (c *Client) Links(ctx context.Context, ref forge.Ref) ([]forge.Link, error) {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return nil, err
	}
	var (
		subs   []wireIssue
		parent *wireIssue
	)
	err := concurrently(
		func() (err error) {
			subs, err = getAll[wireIssue](ctx, c, issuePath(ref)+"/sub_issues", nil, false)
			return err
		},
		func() error {
			var w wireIssue
			err := c.getJSON(ctx, issuePath(ref)+"/parent", nil, false, &w)
			switch {
			case errors.Is(err, forge.ErrNotFound):
				return nil // no parent; a missing issue fails the sub-issues call
			case err != nil:
				return err
			}
			parent = &w
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	var out []forge.Link
	if parent != nil {
		out = append(out, forge.Link{Ref: parent.issue(ref.Project).Ref, Type: "parent"})
	}
	for _, s := range subs {
		out = append(out, forge.Link{Ref: s.issue(ref.Project).Ref, Type: "sub_issue"})
	}
	return out, nil
}

// Milestones implements forge.Forge. Milestones belong to one repository on
// GitHub, so groupOrProject must be "owner/repo" (the tracker); an owner alone
// has none to list. The start of each sprint comes from its description (see
// the package comment).
func (c *Client) Milestones(ctx context.Context, groupOrProject string) ([]forge.Milestone, error) {
	if owner, repo := splitProject(groupOrProject); owner == "" || repo == "" {
		return nil, fmt.Errorf("github: milestones belong to a repository: want owner/repo, got %q", groupOrProject)
	}
	raw, err := c.repoMilestones(ctx, groupOrProject)
	if err != nil {
		return nil, err
	}
	out := make([]forge.Milestone, len(raw))
	for i, w := range raw {
		out[i] = w.milestone()
		c.noteMilestone(&out[i])
	}
	slices.SortStableFunc(out, func(a, b forge.Milestone) int {
		if d := a.Start.Compare(b.Start.Time); d != 0 {
			return d
		}
		return a.Due.Compare(b.Due.Time)
	})
	return out, nil
}

func (c *Client) repoMilestones(ctx context.Context, project string) ([]wireMilestone, error) {
	q := url.Values{"state": {"all"}, "sort": {"due_on"}, "direction": {"asc"}}
	return getAll[wireMilestone](ctx, c, repoPath(project)+"/milestones", q, false)
}

// Iterations implements forge.Forge. GitHub has no iterations outside
// Projects v2 fields, which ADE does not read.
func (c *Client) Iterations(context.Context, string, string) ([]forge.Iteration, error) {
	return nil, fmt.Errorf("github: iterations: %w", forge.ErrUnavailable)
}

// noteMilestone remembers m for milestoneFor.
func (c *Client) noteMilestone(m *forge.Milestone) {
	if m == nil {
		return
	}
	c.mu.Lock()
	c.milestones[m.ID] = *m
	c.mu.Unlock()
}

// milestoneNumber finds the number of project's milestone called title.
func (c *Client) milestoneNumber(ctx context.Context, project, title string) (n int, ok bool, err error) {
	raw, err := c.repoMilestones(ctx, project)
	if err != nil {
		return 0, false, err
	}
	for _, m := range raw {
		c.noteMilestone(new(m.milestone()))
		if m.Title == title {
			return m.Number, true, nil
		}
	}
	return 0, false, nil
}

// accountLogin resolves an account id to its login.
func (c *Client) accountLogin(ctx context.Context, id int64) (string, error) {
	c.mu.Lock()
	login, ok := c.users[id]
	c.mu.Unlock()
	if ok {
		return login, nil
	}
	var u wireUser
	if err := c.getJSON(ctx, "/user/"+strconv.FormatInt(id, 10), nil, false, &u); err != nil {
		return "", fmt.Errorf("github: resolving account %d: %w", id, err)
	}
	c.mu.Lock()
	c.users[id] = u.Login
	c.mu.Unlock()
	return u.Login, nil
}

// isStatus reports whether err is an APIError with that HTTP status.
func isStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}
