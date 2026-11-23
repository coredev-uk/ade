package gitlab

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

var _ forge.MRReviewers = (*Client)(nil)

//go:embed queries/reviewer_rereview.graphql
var reviewerRereviewMutation string

// reviewerStates maps GET .../reviewers states (the GraphQL
// MergeRequestReviewState values, lower case) to forge.Reviewer states.
var reviewerStates = map[string]string{
	"unreviewed":        forge.ReviewerRequested,
	"review_started":    forge.ReviewerReviewing,
	"reviewed":          forge.ReviewerCommented,
	"approved":          forge.ReviewerApproved,
	"requested_changes": forge.ReviewerChangesRequested,
}

// Reviewers implements forge.MRReviewers. An instance without the reviewers
// endpoint still lists the MR's reviewers, all as requested.
func (c *Client) Reviewers(ctx context.Context, ref forge.Ref) ([]forge.Reviewer, error) {
	if err := wantKind(ref, forge.KindMR); err != nil {
		return nil, err
	}
	var raw []wireReviewer
	err := c.getJSON(ctx, mrPath(ref)+"/reviewers", nil, true, &raw)
	if unavailable(err) {
		c.markUnavailable(FeatureReviewers)
		var w wireMR
		if err := c.getJSON(ctx, mrPath(ref), nil, false, &w); err != nil {
			return nil, err
		}
		out := make([]forge.Reviewer, len(w.Reviewers))
		for i, u := range w.Reviewers {
			out[i] = forge.Reviewer{User: u.user(), State: forge.ReviewerRequested}
		}
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]forge.Reviewer, len(raw))
	for i, r := range raw {
		state := reviewerStates[r.State]
		if state == "" {
			state = forge.ReviewerRequested
		}
		out[i] = forge.Reviewer{User: r.User.user(), State: state}
	}
	return out, nil
}

// RequestReview implements forge.MRReviewers. Users new to the MR are added
// with PUT reviewer_ids, which replaces the whole set, so the MR's current
// reviewers are sent along with them. Users who already review get
// mergeRequestReviewerRereview, GitLab's "re-request a review": REST has no
// call for it (see docs/gitlab-api/reviewers.md).
func (c *Client) RequestReview(ctx context.Context, ref forge.Ref, usernames []string) error {
	if err := wantKind(ref, forge.KindMR); err != nil {
		return err
	}
	if len(usernames) == 0 {
		return errors.New("gitlab: RequestReview needs at least one user")
	}
	var w wireMR
	if err := c.getJSON(ctx, mrPath(ref), nil, false, &w); err != nil {
		return err
	}
	set := make([]int64, len(w.Reviewers))
	for i, r := range w.Reviewers {
		set[i] = r.ID
	}
	var fresh, again []wireUser
	seen := map[int64]bool{}
	for _, name := range usernames {
		u, err := c.userByUsername(ctx, name)
		if err != nil {
			return err
		}
		switch {
		case seen[u.ID]:
		case slices.Contains(set, u.ID):
			again = append(again, u)
		default:
			fresh = append(fresh, u)
		}
		seen[u.ID] = true
	}
	if len(fresh) > 0 {
		for _, u := range fresh {
			set = append(set, u.ID)
		}
		if err := c.send(ctx, http.MethodPut, mrPath(ref), map[string][]int64{"reviewer_ids": set}, nil); err != nil {
			return err
		}
	}
	for _, u := range again {
		if err := c.rereview(ctx, ref, u); err != nil {
			return err
		}
	}
	return nil
}

// rereview asks a reviewer again through the mutation. An instance without it
// is forge.ErrUnavailable.
func (c *Client) rereview(ctx context.Context, ref forge.Ref, u wireUser) error {
	vars := map[string]any{
		"projectPath": ref.Project,
		"iid":         strconv.Itoa(ref.IID),
		"userId":      "gid://gitlab/User/" + strconv.FormatInt(u.ID, 10),
	}
	var out struct {
		Rereview *struct {
			Errors []string `json:"errors"`
		} `json:"mergeRequestReviewerRereview"`
	}
	err := c.graphql(ctx, reviewerRereviewMutation, vars, &out)
	if unavailable(err) {
		return fmt.Errorf("gitlab: this instance cannot re-request a review (mergeRequestReviewerRereview) from %s on %s: %w", u.Username, ref, forge.ErrUnavailable)
	}
	if err != nil {
		return err
	}
	if out.Rereview == nil {
		return fmt.Errorf("gitlab: mergeRequestReviewerRereview on %s: %w", ref, forge.ErrUnavailable)
	}
	if errs := out.Rereview.Errors; len(errs) > 0 {
		return fmt.Errorf("gitlab: re-requesting review from %s on %s: %s", u.Username, ref, strings.Join(errs, "; "))
	}
	return nil
}

// userByUsername resolves a username to its user (GET /users?username=, an
// exact, case-insensitive lookup).
func (c *Client) userByUsername(ctx context.Context, name string) (wireUser, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "@")
	var found []wireUser
	if err := c.getJSON(ctx, "/users", url.Values{"username": {name}}, false, &found); err != nil {
		return wireUser{}, err
	}
	for _, u := range found {
		if strings.EqualFold(u.Username, name) {
			return u, nil
		}
	}
	return wireUser{}, fmt.Errorf("gitlab: no user %q: %w", name, forge.ErrNotFound)
}
