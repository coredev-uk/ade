// Package gitlab is the GitLab driver for package forge. It speaks REST (API
// v4) and one small GraphQL query for capabilities REST cannot report. Nothing
// is cached here; callers cache.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/httpdebug"
)

const (
	apiPrefix = "/api/v4"
	// pageSize is the maximum GitLab allows; fewer round trips per list.
	pageSize = 100
	// maxInFlight caps concurrent requests per client: GitLab.com rate-limits
	// bursts per user, and 4 matches the per-host cap documented in the plan.
	maxInFlight       = 4
	defaultMaxRetries = 3
	// maxRetryWait bounds how long a 429 may make us wait. Some limits report
	// windows of up to 24h; waiting that long inside a TUI call is never right.
	maxRetryWait = 2 * time.Minute
	// maxErrorBody bounds how much of an error response is kept in messages.
	maxErrorBody = 512
	// retryPad is added to a server-given Retry-After before retrying.
	retryPad = time.Second
)

// Client implements forge.Forge against one GitLab instance.
type Client struct {
	base       string // scheme://host[/prefix], no trailing slash, no /api/v4
	token      string
	hc         *http.Client
	sem        chan struct{}
	inFlight   int
	missing    unavailableSet // features this instance answered 404 for (see Unavailable)
	maxRetries int
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error

	// rate is shared with every client of the same instance and token: the
	// 429 gate and the RateLimit-* budget (see ratelimit.go). The in-flight
	// cap above stays per client.
	rate *rateState
	// noBatch is set once the instance could not answer the MRDetails query:
	// merge requests are then read one by one.
	noBatch atomic.Bool
	// noHierarchy is set once the instance could not answer a work item
	// hierarchy query: Parenter then reports forge.ErrUnavailable.
	noHierarchy atomic.Bool
}

var _ forge.Forge = (*Client)(nil)

// Option customises a Client.
type Option func(*Client)

// WithHTTPClient replaces the default http.Client (30s timeout).
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// WithClock sets the time source used for iteration state and probe stamps.
func WithClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

// WithSleep replaces the wait used between 429 retries (tests observe it).
func WithSleep(sleep func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.sleep = sleep }
}

// WithMaxRetries sets how many times a 429 is retried per request.
func WithMaxRetries(n int) Option { return func(c *Client) { c.maxRetries = n } }

// WithMaxConcurrency caps concurrent requests of this client (default 4, the
// per-host cap of the plan). Self-managed instances set their own limits, so
// `[forges.x] max_concurrency` lets a user lower or raise it. n < 1 keeps the
// default.
func WithMaxConcurrency(n int) Option {
	return func(c *Client) {
		if n > 0 {
			c.inFlight = n
		}
	}
}

// New returns a client for the GitLab instance at baseURL (for example
// https://gitlab.com), authenticating with a personal, group or project
// access token.
func New(baseURL, token string, opts ...Option) *Client {
	c := &Client{
		base:       strings.TrimRight(baseURL, "/"),
		token:      token,
		hc:         &http.Client{Timeout: 30 * time.Second, Transport: httpdebug.Transport(nil)},
		inFlight:   maxInFlight,
		maxRetries: defaultMaxRetries,
		now:        time.Now,
		sleep:      sleepCtx,
	}
	for _, o := range opts {
		o(c)
	}
	c.sem = make(chan struct{}, c.inFlight)
	c.rate = sharedRate(c.base, c.token)
	return c
}

// Kind implements forge.Forge.
func (c *Client) Kind() string { return "gitlab" }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// APIError is a non-2xx answer from GitLab. It unwraps to one of forge's
// sentinel errors when the status maps to one. It never carries the token.
type APIError struct {
	Method  string
	Path    string
	Status  int
	Message string
	// RetryAfter is set for 429 answers that were not retried.
	RetryAfter time.Duration

	kind error
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("gitlab %s %s: %d", e.Method, e.Path, e.Status)
	if e.Message != "" {
		s += " " + e.Message
	}
	if e.kind != nil {
		s += ": " + e.kind.Error()
	}
	return s
}

func (e *APIError) Unwrap() error { return e.kind }

// request describes one HTTP call. path is relative to /api/v4 (or to the
// instance root when root is set) and already escaped; gated marks endpoints
// that licence tiers may withhold.
type request struct {
	method string
	path   string
	root   bool
	query  url.Values
	body   []byte
	gated  bool
	accept string // Accept header override; default application/json
}

// do sends r and returns the open response for a 2xx answer; the caller must
// close it. Other statuses become *APIError. 429 is retried honouring
// Retry-After, a bounded number of times.
func (c *Client) do(ctx context.Context, r request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.once(ctx, r, attempt > 0)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 == 2 {
			return resp, nil
		}
		aerr := c.statusError(r, resp)
		if resp.StatusCode != http.StatusTooManyRequests {
			return nil, aerr
		}
		wait := retryAfter(resp.Header, c.now(), attempt)
		aerr.RetryAfter = wait
		if attempt >= c.maxRetries || wait > maxRetryWait {
			return nil, aerr
		}
		if resp.Header.Get("Retry-After") != "" {
			// GitLab reports whole seconds, rounded down: retrying exactly
			// on time lands inside the closed window and is refused again
			// with a fresh, longer wait.
			wait += retryPad
		}
		c.rate.closeGate(wait)
		if err := c.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// once performs a single attempt. The in-flight slot is held until the
// response body is closed, so streaming bodies count against the cap.
func (c *Client) once(ctx context.Context, r request, retry bool) (*http.Response, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var once sync.Once
	release := func() { once.Do(func() { <-c.sem }) }
	if !retry {
		if err := c.waitGate(ctx); err != nil {
			release()
			return nil, err
		}
	}

	u := c.base + r.path
	if !r.root {
		u = c.base + apiPrefix + r.path
	}
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, u, body)
	if err != nil {
		release()
		return nil, fmt.Errorf("gitlab %s %s: %w", r.method, r.path, err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	accept := "application/json"
	if r.accept != "" {
		accept = r.accept
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "ade")
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		release()
		return nil, fmt.Errorf("gitlab %s %s: %w", r.method, r.path, err)
	}
	c.rate.observe(resp.Header, time.Now())
	resp.Body = &slotBody{ReadCloser: resp.Body, release: release}
	return resp, nil
}

// slotBody frees the in-flight slot when the body is closed.
type slotBody struct {
	io.ReadCloser
	release func()
}

func (b *slotBody) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
}

// statusError consumes and closes resp and maps it to an *APIError.
func (c *Client) statusError(r request, resp *http.Response) *APIError {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	e := &APIError{Method: r.method, Path: r.path, Status: resp.StatusCode, Message: errorMessage(raw)}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		e.kind = forge.ErrAuth
	case r.gated && (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound):
		e.kind = forge.ErrUnavailable
	case resp.StatusCode == http.StatusForbidden:
		e.kind = forge.ErrAuth
	case resp.StatusCode == http.StatusNotFound:
		e.kind = forge.ErrNotFound
	}
	return e
}

// errorMessage extracts GitLab's "message" or "error" string, falling back to
// the trimmed body for non-JSON answers (rate limiters reply "Retry later").
func errorMessage(raw []byte) string {
	var j struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(raw, &j) == nil {
		if s, ok := j.Message.(string); ok && s != "" {
			return s
		}
		if j.Error != "" {
			return j.Error
		}
		if j.Message != nil {
			b, _ := json.Marshal(j.Message)
			return string(b)
		}
	}
	return strings.TrimSpace(string(raw))
}

// retryAfter reads Retry-After (seconds or an HTTP date) and falls back to
// exponential backoff when GitLab sent none.
func retryAfter(h http.Header, now time.Time, attempt int) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return time.Second << min(attempt, 6)
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(max(secs, 0)) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0)
	}
	return time.Second << min(attempt, 6)
}

// getJSON GETs path and decodes the body into out.
func (c *Client) getJSON(ctx context.Context, path string, q url.Values, gated bool, out any) error {
	resp, err := c.do(ctx, request{method: http.MethodGet, path: path, query: q, gated: gated})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decode(resp, path, out)
}

func decode(resp *http.Response, path string, out any) error {
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("gitlab %s: decoding response: %w", path, err)
	}
	return nil
}

// getAll follows offset pagination to the end. It follows x-next-page (or the
// Link rel="next" page) and never reads X-Total, which GitLab omits for large
// result sets.
func getAll[T any](ctx context.Context, c *Client, path string, q url.Values, gated bool) ([]T, error) {
	q = cloneQuery(q)
	q.Set("per_page", strconv.Itoa(pageSize))
	var all []T
	for page := 1; ; {
		resp, err := c.do(ctx, request{method: http.MethodGet, path: path, query: q, gated: gated})
		if err != nil {
			return nil, err
		}
		var items []T
		err = decode(resp, path, &items)
		next := nextPage(resp.Header)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if next <= page {
			return all, nil
		}
		page = next
		q.Set("page", strconv.Itoa(page))
	}
}

func cloneQuery(q url.Values) url.Values {
	out := make(url.Values, len(q)+2)
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// nextPage returns the next page number, or 0 when the listing is finished.
func nextPage(h http.Header) int {
	if n, err := strconv.Atoi(strings.TrimSpace(h.Get("X-Next-Page"))); err == nil {
		return n
	}
	for part := range strings.SplitSeq(h.Get("Link"), ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.Contains(params, `rel="next"`) {
			continue
		}
		u, err := url.Parse(strings.Trim(strings.TrimSpace(target), "<>"))
		if err != nil {
			return 0
		}
		n, _ := strconv.Atoi(u.Query().Get("page"))
		return n
	}
	return 0
}

// concurrently runs fns in parallel and joins their errors. The client's
// in-flight cap still bounds the actual requests.
func concurrently(fns ...func() error) error {
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = fn()
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func projectPath(project string) string { return "/projects/" + url.PathEscape(project) }

func groupPath(group string) string { return "/groups/" + url.PathEscape(group) }
