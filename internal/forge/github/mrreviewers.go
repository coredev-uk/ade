package github

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

var _ forge.MRReviewers = (*Client)(nil)

// Reviewers implements forge.MRReviewers. GitHub has no reviewer list: the
// people with a pending request (requested_reviewers) and the authors of
// submitted reviews are put together. A pending request wins, as it means
// the review was asked for again; otherwise the latest review decides (a
// plain comment never undoes an approval or a request for changes, as in
// approvals). Teams and the PR's author are left out.
func (c *Client) Reviewers(ctx context.Context, ref forge.Ref) ([]forge.Reviewer, error) {
	if err := wantKind(ref, forge.KindMR); err != nil {
		return nil, err
	}
	var (
		pr      wirePull
		reviews []wireReview
	)
	err := concurrently(
		func() error { return c.getJSON(ctx, pullPath(ref), nil, false, &pr) },
		func() (err error) {
			reviews, err = getAll[wireReview](ctx, c, pullPath(ref)+"/reviews", nil, false)
			return
		},
	)
	if err != nil {
		return nil, err
	}
	var (
		out   []forge.Reviewer
		index = map[int64]int{}
	)
	put := func(u wireUser, state string, override bool) {
		if u.ID == pr.User.ID {
			return
		}
		if i, ok := index[u.ID]; ok {
			if override || out[i].State == forge.ReviewerCommented {
				out[i].State = state
			}
			return
		}
		index[u.ID] = len(out)
		out = append(out, forge.Reviewer{User: u.user(), State: state})
	}
	for _, r := range reviews {
		switch r.State {
		case "APPROVED":
			put(r.User, forge.ReviewerApproved, true)
		case "CHANGES_REQUESTED":
			put(r.User, forge.ReviewerChangesRequested, true)
		case "COMMENTED", "DISMISSED":
			put(r.User, forge.ReviewerCommented, false)
		}
	}
	for _, u := range pr.RequestedReviewers {
		put(u, forge.ReviewerRequested, true)
	}
	return out, nil
}

// RequestReview implements forge.MRReviewers: POST .../requested_reviewers.
// Naming someone who already reviewed asks them again, which is how GitHub
// does "re-request review".
func (c *Client) RequestReview(ctx context.Context, ref forge.Ref, usernames []string) error {
	if err := wantKind(ref, forge.KindMR); err != nil {
		return err
	}
	logins := make([]string, 0, len(usernames))
	for _, u := range usernames {
		if u = strings.TrimPrefix(strings.TrimSpace(u), "@"); u != "" {
			logins = append(logins, u)
		}
	}
	if len(logins) == 0 {
		return errors.New("github: RequestReview needs at least one user")
	}
	return c.send(ctx, http.MethodPost, pullPath(ref)+"/requested_reviewers", map[string][]string{"reviewers": logins}, nil)
}
