package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"

	"github.com/coredev-uk/ade/internal/forge"
)

// Capabilities implements forge.Forge. Each gated feature is probed with
// read-only calls; nothing is created. Results were verified against a Free
// gitlab.com group; the licensed side is derived from GitLab's docs and is
// confirmed per instance by `ade doctor --probe`.
//
//   - Iterations: GET /groups/:id/iterations (403 on Free).
//   - Weight: whether an issue of the group carries the `weight` key (absent
//     on Free). A group without issues cannot say, so it follows Iterations,
//     which is licensed together with weight.
//   - ApprovalRules: GET /projects/:id/approval_rules answers 200 [] on Free,
//     so the status alone proves nothing. GitLab's own licence flag on an MR's
//     approvals (merge_request_approvers_available) decides; a group without
//     merge requests follows Iterations.
//   - BlocksLinks: blocks/is_blocked_by links are Premium like the two above.
//     Probing would mean creating a link, which a probe must never do, so it
//     is inferred from them.
//   - Cadences, Epics: GraphQL; a null gated field means unavailable.
func (c *Client) Capabilities(ctx context.Context, group string) (forge.Caps, error) {
	if group == "" {
		return forge.Caps{}, errors.New("gitlab: Capabilities needs a group")
	}
	caps := forge.Caps{ProbedAt: c.now()}
	var err error
	if caps.Iterations, err = c.probeIterations(ctx, group); err != nil {
		return forge.Caps{}, err
	}
	if caps.Weight, err = c.probeWeight(ctx, group, caps.Iterations); err != nil {
		return forge.Caps{}, err
	}
	if caps.ApprovalRules, err = c.probeApprovalRules(ctx, group, caps.Iterations); err != nil {
		return forge.Caps{}, err
	}
	caps.BlocksLinks = caps.Iterations || caps.ApprovalRules
	if caps.Cadences, caps.Epics, err = c.probeGraphQL(ctx, group); err != nil {
		return forge.Caps{}, err
	}
	return caps, nil
}

// probe reports whether a gated GET answered at all. Only 403/404 mean
// "not offered"; auth failures and transport errors are the probe failing.
func (c *Client) probe(ctx context.Context, path string, q url.Values) (bool, error) {
	var ignored json.RawMessage
	err := c.getJSON(ctx, path, q, true, &ignored)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, forge.ErrUnavailable):
		return false, nil
	default:
		return false, err
	}
}

func (c *Client) probeIterations(ctx context.Context, group string) (bool, error) {
	return c.probe(ctx, groupPath(group)+"/iterations", url.Values{"per_page": {"1"}})
}

func (c *Client) probeWeight(ctx context.Context, group string, iterations bool) (bool, error) {
	var sample []map[string]json.RawMessage
	q := url.Values{"per_page": {"1"}, "state": {"all"}}
	if err := c.getJSON(ctx, groupPath(group)+"/issues", q, false, &sample); err != nil {
		return false, err
	}
	if len(sample) == 0 {
		return iterations, nil
	}
	_, has := sample[0]["weight"]
	return has, nil
}

func (c *Client) probeApprovalRules(ctx context.Context, group string, iterations bool) (bool, error) {
	var mrs []struct {
		IID       int   `json:"iid"`
		ProjectID int64 `json:"project_id"`
	}
	q := url.Values{"per_page": {"1"}, "state": {"all"}, "scope": {"all"}}
	if err := c.getJSON(ctx, groupPath(group)+"/merge_requests", q, false, &mrs); err != nil {
		return false, err
	}
	if len(mrs) == 0 {
		return iterations, nil
	}
	project := strconv.FormatInt(mrs[0].ProjectID, 10)
	var appr wireApprovals
	err := c.getJSON(ctx, "/projects/"+project+"/merge_requests/"+strconv.Itoa(mrs[0].IID)+"/approvals", nil, false, &appr)
	if err != nil {
		return false, err
	}
	if appr.ApproversAvailable == nil || !*appr.ApproversAvailable {
		return false, nil
	}
	return c.probe(ctx, "/projects/"+project+"/approval_rules", url.Values{"per_page": {"1"}})
}

// probeGraphQL reads iteration cadences and the Epic work-item type in one
// request. Group.epics is deprecated and answers an empty list on Free, so it
// cannot tell the tiers apart; the Epic type is only listed where licensed.
func (c *Client) probeGraphQL(ctx context.Context, group string) (cadences, epics bool, err error) {
	var out struct {
		Group *struct {
			IterationCadences *struct {
				Nodes []json.RawMessage `json:"nodes"`
			} `json:"iterationCadences"`
			WorkItemTypes *struct {
				Nodes []json.RawMessage `json:"nodes"`
			} `json:"workItemTypes"`
		} `json:"group"`
	}
	if err := c.graphql(ctx, capabilitiesQuery, map[string]any{"path": group}, &out); err != nil {
		return false, false, err
	}
	if out.Group == nil {
		return false, false, forge.ErrNotFound
	}
	g := out.Group
	return g.IterationCadences != nil, g.WorkItemTypes != nil && len(g.WorkItemTypes.Nodes) > 0, nil
}
