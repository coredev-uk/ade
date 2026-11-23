package gitlab

import (
	"context"
	"net/http"

	"github.com/coredev-uk/ade/internal/forge"
)

var _ forge.MRStateWriter = (*Client)(nil)

// CloseMR implements forge.MRStateWriter: PUT .../merge_requests/:iid with
// state_event=close.
func (c *Client) CloseMR(ctx context.Context, ref forge.Ref) error {
	return c.stateEvent(ctx, ref, "close")
}

// ReopenMR implements forge.MRStateWriter (state_event=reopen). Another open MR
// for the same source and target answers 409, returned as forge.ErrExists.
func (c *Client) ReopenMR(ctx context.Context, ref forge.Ref) error {
	return existsError(c.stateEvent(ctx, ref, "reopen"), "already exists")
}

func (c *Client) stateEvent(ctx context.Context, ref forge.Ref, event string) error {
	if err := wantKind(ref, forge.KindMR); err != nil {
		return err
	}
	return c.send(ctx, http.MethodPut, mrPath(ref), map[string]string{"state_event": event}, nil)
}
