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
// (with the tracker project as the sample source) only on a miss.
func CachedCaps(ctx context.Context, a *app.App, forgeName, group, project string, f forge.Forge) (forge.Caps, error) {
	fetch := func() (forge.Caps, error) { return f.Capabilities(ctx, group, project) }
	st, err := a.Cache()
	if err != nil {
		return fetch()
	}
	return CachedCapsIn(st, forgeName, group, time.Now(), fetch)
}

// CachedCapsIn is CachedCaps over an open store. A user namespace without the
// weight field is not stored: it may only mean the tracker had no issue to
// sample yet, and a day-long cache would hide the field once one exists.
func CachedCapsIn(st *store.Store, forgeName, group string, now time.Time, fetch func() (forge.Caps, error)) (forge.Caps, error) {
	key := CapsKey(forgeName, group)
	if caps, ok := store.Peek[forge.Caps](st, key, CacheTTL, now); ok {
		return caps, nil
	}
	caps, err := fetch()
	if err != nil {
		return forge.Caps{}, err
	}
	if !caps.UserNamespace || caps.Weight {
		store.Put(st, key, now, caps)
	}
	return caps, nil
}
