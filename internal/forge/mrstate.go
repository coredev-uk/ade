package forge

import "context"

// MRStateWriter closes and reopens a merge request. A driver that has not
// implemented it does not satisfy it; callers say so and stop.
//
// A merged MR can do neither. ReopenMR returns an error wrapping ErrExists
// when the branch already has another open MR to the same target.
type MRStateWriter interface {
	CloseMR(ctx context.Context, ref Ref) error
	ReopenMR(ctx context.Context, ref Ref) error
}
