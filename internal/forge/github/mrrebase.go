package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

var _ forge.MRRebaser = (*Client)(nil)

const (
	updatePoll  = 2 * time.Second
	updateTries = 30 // a minute of polling before the update is left running
)

// RebaseMR implements forge.MRRebaser with PUT .../pulls/{n}/update-branch,
// which merges the base branch into the head branch (GitHub's own "Update
// branch" button). The call answers 202 and works in the background, so this
// polls the pull request until its head moves. The head is pinned with
// expected_head_sha: a push since the caller looked is a 422. A conflict
// between the branches is a 422 too, and wraps forge.ErrRebaseFailed.
func (c *Client) RebaseMR(ctx context.Context, mr forge.Ref, o forge.RebaseOpts) (forge.RebaseResult, error) {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return forge.RebaseResult{}, err
	}
	sha := o.SHA
	if sha == "" {
		pr, err := c.getPull(ctx, mr)
		if err != nil {
			return forge.RebaseResult{}, err
		}
		sha = pr.Head.SHA
	}
	err := c.send(ctx, http.MethodPut, pullPath(mr)+"/update-branch", map[string]string{"expected_head_sha": sha}, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity {
		msg := strings.ToLower(ae.Message)
		switch {
		case strings.Contains(msg, "no new commits"):
			return forge.RebaseResult{Method: "merge", UpToDate: true, HeadSHA: sha}, nil
		case strings.Contains(msg, "merge conflict"):
			return forge.RebaseResult{}, fmt.Errorf("%w: %s", forge.ErrRebaseFailed, ae.Message)
		}
	}
	if err != nil {
		return forge.RebaseResult{}, fmt.Errorf("update %s: %w", mr, err)
	}
	for range updateTries {
		pr, err := c.getPull(ctx, mr)
		if err != nil {
			return forge.RebaseResult{}, err
		}
		if pr.Head.SHA != sha {
			return forge.RebaseResult{Method: "merge", HeadSHA: pr.Head.SHA}, nil
		}
		if err := c.sleep(ctx, updatePoll); err != nil {
			return forge.RebaseResult{}, err
		}
	}
	return forge.RebaseResult{}, fmt.Errorf("github: %s is still updating after %s: reload the pull request later", mr, updateTries*updatePoll)
}
