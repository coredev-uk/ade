package gitlab

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// foregroundReserve is the share of the server's rate-limit window kept for
// foreground requests: a sync (background) pauses once only this much is left,
// so a dialog the user opens mid-sync is never the request that finds the
// window closed and waits out a Retry-After.
const foregroundReserve = 0.3

// rateState is what every client of one instance and token shares about the
// server's rate limit. Clients are cheap and short-lived (a sync, a dialog),
// but the limit belongs to the user, so the window must not be tracked per
// client.
type rateState struct {
	mu sync.Mutex
	// gateUntil is when the last 429 said requests may resume.
	gateUntil time.Time
	// The RateLimit-* headers GitLab sends where its limits are on. known is
	// false until a response carried them (or on servers that send none).
	known     bool
	limit     int
	remaining int
	resetAt   time.Time
}

var rates sync.Map // base + "\x00" + token → *rateState

func sharedRate(base, token string) *rateState {
	v, _ := rates.LoadOrStore(base+"\x00"+token, &rateState{})
	return v.(*rateState)
}

// closeGate holds back requests that have not started yet until d from now:
// while a window is closed every further request is refused and still counts.
func (r *rateState) closeGate(d time.Duration) {
	until := time.Now().Add(d)
	r.mu.Lock()
	if until.After(r.gateUntil) {
		r.gateUntil = until
	}
	r.mu.Unlock()
}

// wait returns how long a request about to start must wait. A background
// request also waits for the next window once the foreground reserve is all
// that is left; it counts itself against the window so concurrent starters
// cannot all squeeze through the last slot.
func (r *rateState) wait(background bool, now time.Time) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.gateUntil.Sub(now)
	if background && r.known && now.Before(r.resetAt) && r.remaining <= int(float64(r.limit)*foregroundReserve) {
		d = max(d, r.resetAt.Sub(now))
	}
	if d <= 0 && r.known && r.remaining > 0 {
		r.remaining--
	}
	return max(d, 0)
}

// observe records the budget a response reported.
func (r *rateState) observe(h http.Header, now time.Time) {
	limit, err1 := strconv.Atoi(strings.TrimSpace(h.Get("RateLimit-Limit")))
	remaining, err2 := strconv.Atoi(strings.TrimSpace(h.Get("RateLimit-Remaining")))
	reset, err3 := strconv.ParseInt(strings.TrimSpace(h.Get("RateLimit-Reset")), 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || limit <= 0 {
		return
	}
	at := time.Unix(reset, 0)
	if !at.After(now) {
		return // a stale window says nothing about the current one
	}
	r.mu.Lock()
	r.known, r.limit, r.remaining, r.resetAt = true, limit, remaining, at
	r.mu.Unlock()
}

// waitGate blocks a request that is about to start until the shared window
// allows it. A retry has already slept for its own Retry-After and skips it.
func (c *Client) waitGate(ctx context.Context) error {
	d := c.rate.wait(forge.IsBackground(ctx), time.Now())
	if d <= 0 {
		return nil
	}
	return c.sleep(ctx, d)
}
