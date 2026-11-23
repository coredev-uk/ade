package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A rate limit or a provider having a bad minute is not a failed answer: the
// HTTP drivers ask again, a bounded number of times, waiting as the provider
// says (Retry-After) or a short backoff. A refused request costs nothing, so a
// retry never adds spend; a budget stop (which some gateways answer with 429)
// is never retried.
const (
	maxHTTPAttempts = 3                // the first request and two more
	maxRetryWait    = 30 * time.Second // a longer Retry-After is the caller's to wait out
)

// retryBackoff is the wait before each further attempt when the provider gave
// no Retry-After.
var retryBackoff = []time.Duration{2 * time.Second, 5 * time.Second}

// retryWait sleeps for d or until ctx ends. Tests replace it.
var retryWait = func(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// transient reports a status worth asking again: rate limits and
// server-side trouble (529 is Anthropic's "overloaded").
func transient(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// retryAfter reads a Retry-After header: delta seconds or an HTTP date.
func retryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(0, t.Sub(now)), true
	}
	return 0, false
}

// exchange is one request's outcome after any retries.
type exchange struct {
	status int
	header http.Header
	data   []byte
	// sent is the request bytes that left the machine over every attempt: the
	// audit trail's "what was sent" (docs/PLAN.md §4.3), failed and repeated
	// requests included.
	sent int
}

// post sends the request build makes, asking again after a transient status.
// build is called per attempt and returns the request and its body size. A
// transport error ends the call at once (a timeout would only repeat).
// who and key name the driver and its credential for error text.
func post(ctx context.Context, hc *http.Client, who, key, stopSource string, build func() (*http.Request, int, error)) (exchange, error) {
	var ex exchange
	for attempt := 0; ; attempt++ {
		req, n, err := build()
		if err != nil {
			return ex, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			var oe *net.OpError
			if !errors.As(err, &oe) || oe.Op != "dial" { // a refused connection sent nothing
				ex.sent += n
			}
			return ex, fmt.Errorf("%s: %w", who, redactKey(err, key))
		}
		ex.sent += n
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if rerr != nil {
			return ex, fmt.Errorf("%s: read response: %w", who, rerr)
		}
		ex.status, ex.header, ex.data = resp.StatusCode, resp.Header, data
		if !transient(resp.StatusCode) || attempt == maxHTTPAttempts-1 || classifyStop(stopSource, data) != nil {
			return ex, nil
		}
		wait, ok := retryAfter(resp.Header, time.Now())
		if !ok {
			wait = retryBackoff[min(attempt, len(retryBackoff)-1)]
		}
		if wait > maxRetryWait {
			return ex, nil
		}
		if err := retryWait(ctx, wait); err != nil {
			return ex, err
		}
	}
}

// jsonRequest builds a POST with a JSON body.
func jsonRequest(ctx context.Context, url string, raw []byte, header func(*http.Request)) (*http.Request, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	header(req)
	return req, len(raw), nil
}
