package startwork

import (
	"context"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/store"
)

// CacheTTL is how long a forge's identity and capabilities stay cached: what a
// dialog needs of them does not change within a day, and re-probing
// capabilities costs half a dozen requests.
const CacheTTL = 24 * time.Hour

// MeKey and CapsKey are the kv keys of the cached identity and capabilities;
// a sync fills them and dialogs read them, so they must agree.
func MeKey(forgeName string) string { return "forgesync:me:" + forgeName }

func CapsKey(forgeName, group string) string {
	return "forgesync:caps:" + forgeName + ":" + group
}

// CachedMe reads the user from the cache, asking the forge only on a miss.
func CachedMe(ctx context.Context, a *app.App, forgeName string, f forge.Forge) (forge.User, error) {
	fetch := func() (forge.User, error) { return f.Me(ctx) }
	st, err := a.Cache()
	if err != nil {
		return fetch()
	}
	return store.Cached(st, MeKey(forgeName), CacheTTL, time.Now(), fetch)
}

// CachedCaps reads a group's capabilities from the cache, probing the forge
// only on a miss.
func CachedCaps(ctx context.Context, a *app.App, forgeName, group string, f forge.Forge) (forge.Caps, error) {
	fetch := func() (forge.Caps, error) { return f.Capabilities(ctx, group) }
	st, err := a.Cache()
	if err != nil {
		return fetch()
	}
	return store.Cached(st, CapsKey(forgeName, group), CacheTTL, time.Now(), fetch)
}
