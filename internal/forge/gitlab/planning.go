package gitlab

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// Milestones implements forge.Forge. groupOrProject is tried as a group path
// first (the usual sprint home), then as a project path; a namespace cannot
// be both on GitLab, so the 404 from the wrong guess is unambiguous.
func (c *Client) Milestones(ctx context.Context, groupOrProject string) ([]forge.Milestone, error) {
	if groupOrProject == "" {
		return nil, errors.New("gitlab: Milestones needs a group or project")
	}
	q := url.Values{"include_ancestors": {"true"}}
	raw, err := getAll[wireMilestone](ctx, c, groupPath(groupOrProject)+"/milestones", q, false)
	if errors.Is(err, forge.ErrNotFound) {
		raw, err = getAll[wireMilestone](ctx, c, projectPath(groupOrProject)+"/milestones", q, false)
	}
	if err != nil {
		return nil, err
	}
	out := make([]forge.Milestone, len(raw))
	for i, w := range raw {
		out[i] = w.milestone()
	}
	slices.SortStableFunc(out, func(a, b forge.Milestone) int { return a.Start.Compare(b.Start.Time) })
	return out, nil
}

// iterationStates are the filter values GitLab accepts. They are queried one
// by one because the REST state field is an undocumented integer: the filter
// the iteration came back under is the only trustworthy state.
var iterationStates = []string{"upcoming", "current", "closed"}

// Iterations implements forge.Forge. cadence, when set, narrows to iterations
// whose cadence title matches (GitLab's `in=cadence_title` search); REST does
// not report an iteration's cadence, so Iteration.Cadence echoes the filter.
// Returns forge.ErrUnavailable where iterations are not licensed.
func (c *Client) Iterations(ctx context.Context, group, cadence string) ([]forge.Iteration, error) {
	if group == "" {
		return nil, errors.New("gitlab: Iterations needs a group")
	}
	var out []forge.Iteration
	now := c.now()
	for _, state := range iterationStates {
		q := url.Values{"state": {state}, "include_ancestors": {"true"}}
		if cadence != "" {
			q.Set("search", cadence)
			q.Set("in", "cadence_title")
		}
		raw, err := getAll[wireIteration](ctx, c, groupPath(group)+"/iterations", q, true)
		if err != nil {
			return nil, err
		}
		for _, w := range raw {
			out = append(out, w.iteration(state, cadence, now))
		}
	}
	slices.SortStableFunc(out, func(a, b forge.Iteration) int {
		if d := a.Start.Compare(b.Start.Time); d != 0 {
			return d
		}
		return strings.Compare(a.Title, b.Title)
	})
	return out, nil
}
