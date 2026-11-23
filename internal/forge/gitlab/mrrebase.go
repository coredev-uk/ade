package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

var _ forge.MRRebaser = (*Client)(nil)

const (
	rebasePoll  = 2 * time.Second
	rebaseTries = 90 // three minutes of polling before the rebase is left running
)

// wireRebase is the part of the single-MR payload that tells whether a rebase
// is still running (GET with include_rebase_in_progress=true) and how it ended.
type wireRebase struct {
	InProgress bool    `json:"rebase_in_progress"`
	MergeError *string `json:"merge_error"`
	SHA        string  `json:"sha"`
}

// RebaseMR implements forge.MRRebaser: PUT .../rebase answers 202 and works in
// the background, so this polls the MR until rebase_in_progress is false. A
// merge_error then means GitLab could not rebase ("Rebase failed. Please
// rebase locally"), which is a conflict in practice. GitLab has no head pin on
// this endpoint, so RebaseOpts.SHA is not sent.
func (c *Client) RebaseMR(ctx context.Context, mr forge.Ref, o forge.RebaseOpts) (forge.RebaseResult, error) {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return forge.RebaseResult{}, err
	}
	var in map[string]any
	if o.SkipCI {
		in = map[string]any{"skip_ci": true}
	}
	if err := c.send(ctx, http.MethodPut, mrPath(mr)+"/rebase", in, nil); err != nil {
		return forge.RebaseResult{}, fmt.Errorf("rebase %s: %w", mr, err)
	}
	q := url.Values{"include_rebase_in_progress": {"true"}}
	for range rebaseTries {
		var w wireRebase
		if err := c.getJSON(ctx, mrPath(mr), q, false, &w); err != nil {
			return forge.RebaseResult{}, err
		}
		if !w.InProgress {
			if w.MergeError != nil && *w.MergeError != "" {
				return forge.RebaseResult{}, fmt.Errorf("%w: %s", forge.ErrRebaseFailed, *w.MergeError)
			}
			return forge.RebaseResult{Method: "rebase", HeadSHA: w.SHA}, nil
		}
		if err := c.sleep(ctx, rebasePoll); err != nil {
			return forge.RebaseResult{}, err
		}
	}
	return forge.RebaseResult{}, fmt.Errorf("gitlab: %s is still rebasing after %s: reload the merge request later", mr, rebaseTries*rebasePoll)
}
