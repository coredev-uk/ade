package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/coredev-uk/ade/internal/forge"
)

// Capabilities implements forge.Forge. Each gated feature is probed with
// read-only calls; nothing is created. Results were verified against a Free
// gitlab.com group; the licensed side is derived from GitLab's docs and is
// confirmed per instance by `ade doctor --probe`.
//
// A user (personal) namespace has no group endpoints: it is detected first
// (GET /namespaces/:id), and then Weight and ApprovalRules are sampled from
// project, the tracker project's full path, which may be empty. Iterations,
// Cadences and Epics are false there.
//
//   - Iterations: GET /groups/:id/iterations (403 on Free).
//   - Weight: whether an issue of the group (or project) carries the `weight`
//     key (absent on Free). Without issues it cannot say, so it follows
//     Iterations, which is licensed together with weight; a project has no
//     such probe and answers false.
//   - ApprovalRules: GET /projects/:id/approval_rules answers 200 [] on Free,
//     so the status alone proves nothing. GitLab's own licence flag on an MR's
//     approvals (merge_request_approvers_available) decides; without merge
//     requests it follows Iterations, or is false for a project.
//   - BlocksLinks: blocks/is_blocked_by links are Premium like the two above.
//     Probing would mean creating a link, which a probe must never do, so it
//     is inferred from them.
//   - Cadences, Epics: GraphQL; a null gated field means unavailable.
func (c *Client) Capabilities(ctx context.Context, group, project string) (forge.Caps, error) {
	if group == "" {
		return forge.Caps{}, errors.New("gitlab: Capabilities needs a group")
	}
	caps := forge.Caps{ProbedAt: c.now()}
	user, err := c.isUserNamespace(ctx, group)
	if err != nil {
		return forge.Caps{}, err
	}
	if user {
		return c.userCapabilities(ctx, caps, project)
	}
	if caps.Iterations, err = c.probeIterations(ctx, group); err != nil {
		return forge.Caps{}, err
	}
	if caps.Weight, err = c.probeWeight(ctx, groupPath(group), caps.Iterations); err != nil {
		return forge.Caps{}, err
	}
	if caps.ApprovalRules, err = c.probeApprovalRules(ctx, groupPath(group), caps.Iterations); err != nil {
		return forge.Caps{}, err
	}
	caps.BlocksLinks = caps.Iterations || caps.ApprovalRules
	if caps.Cadences, caps.Epics, err = c.probeGraphQL(ctx, group); err != nil {
		if !graphqlMissing(err) {
			return forge.Caps{}, err
		}
		// An instance without (a usable) GraphQL schema has no cadences and
		// no Epic work item type: say so once and carry on.
		c.markUnavailable(FeatureCadences)
		c.markUnavailable(FeatureWorkItems)
		caps.Cadences, caps.Epics = false, false
	}
	return caps, nil
}

// isUserNamespace reads the kind of a namespace. A namespace the lookup does
// not know is reported as a group, so the group probes fail on it as they
// always did.
func (c *Client) isUserNamespace(ctx context.Context, path string) (bool, error) {
	var ns struct {
		Kind string `json:"kind"`
	}
	err := c.getJSON(ctx, "/namespaces/"+url.PathEscape(path), nil, false, &ns)
	switch {
	case errors.Is(err, forge.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return ns.Kind == "user", nil
}

// userCapabilities probes a user namespace through its tracker project. The
// licence shows on the project's own issues and merge requests, so no sample
// means false rather than a guess from iterations. A project with issues or
// merge requests turned off answers 403/404 to the sample, which is the same.
func (c *Client) userCapabilities(ctx context.Context, caps forge.Caps, project string) (forge.Caps, error) {
	caps.UserNamespace = true
	if project == "" {
		return caps, nil
	}
	var err error
	if caps.Weight, err = c.probeWeight(ctx, projectPath(project), false); err != nil && !noSample(err) {
		return forge.Caps{}, err
	}
	if caps.ApprovalRules, err = c.probeApprovalRules(ctx, projectPath(project), false); err != nil && !noSample(err) {
		return forge.Caps{}, err
	}
	caps.BlocksLinks = caps.ApprovalRules
	return caps, nil
}

// noSample reports whether err says the project offers nothing to sample
// (feature disabled or hidden), as opposed to a failing token or transport.
func noSample(err error) bool {
	var api *APIError
	return errors.As(err, &api) && (api.Status == http.StatusForbidden || api.Status == http.StatusNotFound)
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

// probeWeight and probeApprovalRules sample the issues or merge requests under
// scope, a group or project API path. fallback is the answer when there is
// nothing to sample.
func (c *Client) probeWeight(ctx context.Context, scope string, fallback bool) (bool, error) {
	var sample []map[string]json.RawMessage
	q := url.Values{"per_page": {"1"}, "state": {"all"}}
	if err := c.getJSON(ctx, scope+"/issues", q, false, &sample); err != nil {
		return false, err
	}
	if len(sample) == 0 {
		return fallback, nil
	}
	_, has := sample[0]["weight"]
	return has, nil
}

func (c *Client) probeApprovalRules(ctx context.Context, scope string, fallback bool) (bool, error) {
	var mrs []struct {
		IID       int   `json:"iid"`
		ProjectID int64 `json:"project_id"`
	}
	q := url.Values{"per_page": {"1"}, "state": {"all"}, "scope": {"all"}}
	if err := c.getJSON(ctx, scope+"/merge_requests", q, false, &mrs); err != nil {
		return false, err
	}
	if len(mrs) == 0 {
		return fallback, nil
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
