package forge

import (
	"context"
	"errors"
)

// ErrParentRefused means the forge understood a request to set an issue's
// parent and said no: the pair of work item types may not nest (GitLab allows
// an issue under an epic and a task under an issue, not an issue under an
// issue). The request is not worth repeating.
var ErrParentRefused = errors.New("parent not allowed")

// Parenter is the optional native parent/child hierarchy of a forge (GitLab's
// work item hierarchy, which only GraphQL exposes). A forge without it simply
// does not implement the interface, and callers fall back to Parent: lines and
// relates_to links.
type Parenter interface {
	// Parents returns the hierarchy parent of each issue in refs that has one;
	// issues without a parent are absent from the map. A parent that is not an
	// issue (an epic) is absent too. ErrUnavailable where the instance has no
	// hierarchy.
	Parents(ctx context.Context, refs []Ref) (map[Ref]Ref, error)
	// Children returns the issues placed under parent in the hierarchy.
	Children(ctx context.Context, parent Ref) ([]Ref, error)
	// SetParent places child under parent. ErrParentRefused when the forge does
	// not allow that pair; ErrUnavailable where the instance has no hierarchy.
	SetParent(ctx context.Context, child, parent Ref) error
}
