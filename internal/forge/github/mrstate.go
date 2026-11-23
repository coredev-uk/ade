package github

import (
	"context"
	"net/http"

	"github.com/coredev-uk/ade/internal/forge"
)

var _ forge.MRStateWriter = (*Client)(nil)

// CloseMR implements forge.MRStateWriter: PATCH /pulls/{n} with state=closed.
func (c *Client) CloseMR(ctx context.Context, ref forge.Ref) error {
	return c.setPullState(ctx, ref, "closed")
}

// ReopenMR implements forge.MRStateWriter (state=open). GitHub answers 422 when
// another open pull request has the same head and base, returned as
// forge.ErrExists.
func (c *Client) ReopenMR(ctx context.Context, ref forge.Ref) error {
	return existsError(c.setPullState(ctx, ref, "open"), "already exists")
}

func (c *Client) setPullState(ctx context.Context, ref forge.Ref, state string) error {
	if err := wantKind(ref, forge.KindMR); err != nil {
		return err
	}
	return c.send(ctx, http.MethodPatch, pullPath(ref), map[string]string{"state": state}, nil)
}
