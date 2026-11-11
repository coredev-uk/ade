package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// Me implements forge.Forge.
func (c *Client) Me(ctx context.Context) (forge.User, error) {
	var u wireUser
	if err := c.getJSON(ctx, "/user", nil, false, &u); err != nil {
		return forge.User{}, err
	}
	return u.user(), nil
}

// ListIssues implements forge.Forge. q.Project is required.
func (c *Client) ListIssues(ctx context.Context, q forge.IssueQuery) ([]forge.Issue, error) {
	if q.Project == "" {
		return nil, errors.New("gitlab: ListIssues needs a project")
	}
	return c.listIssues(ctx, projectPath(q.Project)+"/issues", q, q.Project)
}

// ListAssignedIssues lists issues assigned to the token's user across every
// project they can see (GET /issues?scope=assigned_to_me). q.Project and
// q.Assignee are ignored: GitLab.com rejects scope=all on the global
// endpoint, so a username filter cannot be expressed there.
func (c *Client) ListAssignedIssues(ctx context.Context, q forge.IssueQuery) ([]forge.Issue, error) {
	q.Assignee = ""
	vals := issueFilters(q)
	vals.Set("scope", "assigned_to_me")
	return c.listIssuesWith(ctx, "/issues", vals, "")
}

func (c *Client) listIssues(ctx context.Context, path string, q forge.IssueQuery, project string) ([]forge.Issue, error) {
	vals := issueFilters(q)
	vals.Set("scope", "all")
	return c.listIssuesWith(ctx, path, vals, project)
}

func (c *Client) listIssuesWith(ctx context.Context, path string, vals url.Values, project string) ([]forge.Issue, error) {
	raw, err := getAll[wireIssue](ctx, c, path, vals, false)
	if err != nil {
		return nil, err
	}
	now := c.now()
	out := make([]forge.Issue, len(raw))
	for i, w := range raw {
		out[i] = w.issue(project, now)
	}
	return out, nil
}

func issueFilters(q forge.IssueQuery) url.Values {
	v := url.Values{}
	v.Set("state", stateOr(q.State, "opened"))
	if q.Assignee != "" {
		v.Add("assignee_username[]", q.Assignee)
	}
	if q.Milestone != "" {
		v.Set("milestone", q.Milestone)
	}
	if q.IterationID != 0 {
		v.Set("iteration_id", strconv.FormatInt(q.IterationID, 10))
	}
	if len(q.Labels) > 0 {
		v.Set("labels", strings.Join(q.Labels, ","))
	}
	setUpdatedAfter(v, q.UpdatedAfter)
	return v
}

func stateOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// setUpdatedAfter sends UTC so the timestamp never contains a "+", which
// GitLab would read as a space unless escaped.
func setUpdatedAfter(v url.Values, t time.Time) {
	if !t.IsZero() {
		v.Set("updated_after", t.UTC().Format(time.RFC3339))
	}
}

// Issue implements forge.Forge.
func (c *Client) Issue(ctx context.Context, ref forge.Ref) (forge.Issue, error) {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return forge.Issue{}, err
	}
	var w wireIssue
	if err := c.getJSON(ctx, issuePath(ref), nil, false, &w); err != nil {
		return forge.Issue{}, err
	}
	return w.issue(ref.Project, c.now()), nil
}

// Links implements forge.Forge. GitLab reports link_type relative to the
// queried issue; the issue-links list is Free, only creating blocks links is
// licensed, so a missing issue is ErrNotFound rather than ErrUnavailable.
func (c *Client) Links(ctx context.Context, ref forge.Ref) ([]forge.Link, error) {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return nil, err
	}
	raw, err := getAll[wireIssue](ctx, c, issuePath(ref)+"/links", nil, false)
	if err != nil {
		return nil, err
	}
	out := make([]forge.Link, len(raw))
	for i, w := range raw {
		out[i] = w.link(ref.Project)
	}
	return out, nil
}

func issuePath(ref forge.Ref) string {
	return projectPath(ref.Project) + "/issues/" + strconv.Itoa(ref.IID)
}

func wantKind(ref forge.Ref, k forge.Kind) error {
	if ref.Kind != k || ref.Project == "" || ref.IID <= 0 {
		return fmt.Errorf("gitlab: invalid reference %q", ref)
	}
	return nil
}
