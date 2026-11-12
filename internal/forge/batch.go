package forge

import "context"

// MRDetailer is implemented by drivers that read the detail of many merge
// requests in a handful of requests instead of several per MR. A sync prefers
// it to calling MR once per request; drivers without it (or MRs it cannot
// batch) go through MR.
type MRDetailer interface {
	// MRDetails returns every MR of mrs (as a list returned them) with
	// pipeline, approvals, thread counts and reviewer verdicts filled in, the
	// way MR reads them, in the order given. caps says which fields the
	// instance licenses (approval rules), since a batch cannot probe that
	// per MR. Any MR the batch could not read comes back through MR, so an
	// error means a failure MR itself would report too.
	MRDetails(ctx context.Context, mrs []MR, caps Caps) ([]MR, error)
}

type backgroundKey struct{}

// Background marks ctx as background work (a sync): a driver that knows the
// server's rate-limit budget keeps a reserve of it for foreground requests (a
// dialog the user just opened) by pausing background requests early.
func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

// IsBackground reports whether ctx was marked with Background.
func IsBackground(ctx context.Context) bool {
	v, _ := ctx.Value(backgroundKey{}).(bool)
	return v
}
