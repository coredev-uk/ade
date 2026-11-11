// Package github is the GitHub driver for package forge. It speaks REST (API
// version 2022-11-28) and GraphQL (review threads, thread resolution, draft
// toggling and the pending-review mutations REST cannot express). Nothing is
// cached beyond the few lookups documented on Client; callers cache.
//
// Mapping to ADE's forge-neutral model:
//
//   - A project is "owner/repo"; an issue or pull request is its number.
//     GitHub numbers issues and PRs from one counter, so Ref.Kind decides which
//     the number means.
//   - Sub-issues are the child link (Link types "sub_issue" and "parent").
//   - A merge request is a pull request; a pipeline is the newest Actions
//     workflow run of the head commit (its jobs are the run's jobs), with the
//     status aggregated over every check run and commit status of the head.
//   - Milestones are the sprints. GitHub milestones have a due date but no
//     start date: ADE reads the start from a "Start: YYYY-MM-DD" line in the
//     milestone description.
//   - Weight, iterations, cadences, epics and approval rules do not exist on
//     the free GitHub tier; Caps reports them off and weights ride on labels.
package github

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
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

const (
	// publicAPI and publicGraphQL are github.com's endpoints; every other
	// base URL is GitHub Enterprise Server, which serves under /api.
	publicAPI     = "https://api.github.com"
	publicGraphQL = "https://api.github.com/graphql"
	publicWeb     = "https://github.com"
	apiVersion    = "2022-11-28"

	// pageSize is the maximum GitHub allows.
	pageSize = 100
	// maxInFlight caps concurrent requests per client: GitHub rate-limits
	// bursts of concurrent requests as abuse ("secondary rate limit").
	maxInFlight       = 4
	defaultMaxRetries = 3
	// maxRetryWait bounds how long a rate limit may make us wait inside one
	// call; primary limits reset hourly, and waiting that long is never right.
	maxRetryWait = 2 * time.Minute
	// maxErrorBody bounds how much of an error response is kept in messages.
	maxErrorBody = 512
)

// Client implements forge.Forge, forge.MRWriter, forge.IssueWriter and
// forge.AgileReader against one GitHub instance.
type Client struct {
	rest       string // REST base, no trailing slash
	gql        string // GraphQL endpoint
	token      string
	hc         *http.Client
	sem        chan struct{}
	maxRetries int
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error

	mu sync.Mutex
	// me is the authenticated user, read once: approvals and pending reviews
	// are "mine", and assignees are sent by login.
	me *forge.User
	// milestones remembers every milestone seen by id. A milestone id names
	// one repository's milestone, but start work copies the tracker's id onto
	// a child issue in another repository; the title carries it across.
	milestones map[int64]forge.Milestone
	// users maps account ids to logins for assignee lists.
	users map[int64]string
	// overviews holds each MR's unpublished review overview (see AddDraftNote).
	overviews map[forge.Ref]string
}

var (
	_ forge.Forge       = (*Client)(nil)
	_ forge.MRWriter    = (*Client)(nil)
	_ forge.IssueWriter = (*Client)(nil)
	_ forge.AgileReader = (*Client)(nil)
)

// Option customises a Client.
type Option func(*Client)

// WithHTTPClient replaces the default http.Client (30s timeout).
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// WithClock sets the time source used for retry waits.
func WithClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

// WithSleep replaces the wait used between rate-limit retries (tests observe it).
func WithSleep(sleep func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.sleep = sleep }
}

// WithMaxRetries sets how many times a rate-limited request is retried.
func WithMaxRetries(n int) Option { return func(c *Client) { c.maxRetries = n } }

// New returns a client for the GitHub instance whose web address is baseURL,
// authenticating with a personal access token or `gh auth token`. An empty
// baseURL or https://github.com selects github.com (api.github.com); any
// other address is GitHub Enterprise Server (<base>/api/v3, <base>/api/graphql).
func New(baseURL, token string, opts ...Option) *Client {
	base := strings.TrimRight(baseURL, "/")
	c := &Client{
		rest:       publicAPI,
		gql:        publicGraphQL,
		token:      token,
		hc:         &http.Client{Timeout: 30 * time.Second},
		sem:        make(chan struct{}, maxInFlight),
		maxRetries: defaultMaxRetries,
		now:        time.Now,
		sleep:      sleepCtx,
		milestones: map[int64]forge.Milestone{},
		users:      map[int64]string{},
		overviews:  map[forge.Ref]string{},
	}
	if base != "" && base != publicWeb {
		c.rest, c.gql = base+"/api/v3", base+"/api/graphql"
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Kind implements forge.Forge.
func (c *Client) Kind() string { return "github" }

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

// APIError is a non-2xx answer from GitHub. It unwraps to one of forge's
// sentinel errors when the status maps to one. It never carries the token.
type APIError struct {
	Method  string
	Path    string
	Status  int
	Message string
	// RetryAfter is set for rate-limit answers that were not retried.
	RetryAfter time.Duration

	kind error
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("github %s %s: %d", e.Method, e.Path, e.Status)
	if e.Message != "" {
		s += " " + e.Message
	}
	if e.kind != nil {
		s += ": " + e.kind.Error()
	}
	return s
}

func (e *APIError) Unwrap() error { return e.kind }

// request describes one HTTP call. path is relative to the REST base and
// already escaped; url overrides the base entirely (GraphQL). gated marks
// endpoints that plans may withhold.
type request struct {
	method string
	path   string
	url    string
	query  url.Values
	body   []byte
	gated  bool
	accept string // Accept header override; default application/vnd.github+json
}

func (r request) label() string {
	if r.url != "" {
		return r.url
	}
	return r.path
}

// do sends r and returns the open response for a 2xx answer; the caller must
// close it. Other statuses become *APIError. Rate limits are retried
// honouring Retry-After or the reset time, a bounded number of times.
func (c *Client) do(ctx context.Context, r request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.once(ctx, r)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 == 2 {
			return resp, nil
		}
		wait, limited := rateLimitWait(resp, c.now(), attempt)
		aerr := c.statusError(r, resp, limited)
		if !limited {
			return nil, aerr
		}
		aerr.RetryAfter = wait
		if attempt >= c.maxRetries || wait > maxRetryWait {
			return nil, aerr
		}
		if err := c.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// once performs a single attempt. The in-flight slot is held until the
// response body is closed, so streaming bodies count against the cap.
func (c *Client) once(ctx context.Context, r request) (*http.Response, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var once sync.Once
	release := func() { once.Do(func() { <-c.sem }) }

	u := r.url
	if u == "" {
		u = c.rest + r.path
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
		return nil, fmt.Errorf("github %s %s: %w", r.method, r.label(), err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	accept := "application/vnd.github+json"
	if r.accept != "" {
		accept = r.accept
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "ade")
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		release()
		return nil, fmt.Errorf("github %s %s: %w", r.method, r.label(), err)
	}
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

// rateLimitWait reports whether resp is a rate-limit answer and how long to
// wait before retrying: 429 or 403 with Retry-After (secondary limits), or
// 403 with an exhausted X-RateLimit-Remaining (primary limit, wait for the
// reset). Without a hint it backs off exponentially.
func rateLimitWait(resp *http.Response, now time.Time, attempt int) (time.Duration, bool) {
	h := resp.Header
	backoff := time.Second << min(attempt, 6)
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden) {
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(max(secs, 0)) * time.Second, true
		}
		if t, err := http.ParseTime(v); err == nil {
			return max(t.Sub(now), 0), true
		}
		return backoff, true
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return backoff, true
	case resp.StatusCode == http.StatusForbidden && strings.TrimSpace(h.Get("X-RateLimit-Remaining")) == "0":
		if reset, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil {
			return max(time.Unix(reset, 0).Sub(now), 0), true
		}
		return backoff, true
	}
	return 0, false
}

// statusError consumes and closes resp and maps it to an *APIError.
func (c *Client) statusError(r request, resp *http.Response, limited bool) *APIError {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	e := &APIError{Method: r.method, Path: r.label(), Status: resp.StatusCode, Message: errorMessage(raw)}
	switch {
	case limited:
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

// errorMessage extracts GitHub's "message", appending the first validation
// error's own message (422 answers carry the useful part there), falling back
// to the trimmed body for non-JSON answers.
func errorMessage(raw []byte) string {
	var j struct {
		Message string            `json:"message"`
		Errors  []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(raw, &j) != nil {
		return strings.TrimSpace(string(raw))
	}
	msg := j.Message
	for _, e := range j.Errors {
		if detail := errorDetail(e); detail != "" {
			msg += ": " + detail
			break
		}
	}
	return msg
}

// errorDetail reads one entry of GitHub's errors array: an object with a
// message (or code and field) or, on some endpoints, a plain string.
func errorDetail(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var e struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Field   string `json:"field"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return strings.TrimSpace(e.Field + " " + e.Code)
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
		return fmt.Errorf("github %s: decoding response: %w", path, err)
	}
	return nil
}

// send issues a write with an optional JSON body and decodes the answer into
// out when there is one (204 answers carry no body).
func (c *Client) send(ctx context.Context, method, path string, in, out any) error {
	r := request{method: method, path: path}
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("github %s %s: encoding request: %w", method, path, err)
		}
		r.body = b
	}
	resp, err := c.do(ctx, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return decode(resp, path, out)
}

// getAll follows Link-header pagination to the end of an array endpoint.
func getAll[T any](ctx context.Context, c *Client, path string, q url.Values, gated bool) ([]T, error) {
	return getPages(ctx, c, path, q, gated, nil, decodeList[T])
}

func decodeList[T any](b []byte) ([]T, error) {
	var items []T
	err := json.Unmarshal(b, &items)
	return items, err
}

// getAllIn is getAll for endpoints that wrap the array in an object under key
// (check runs, workflow runs, jobs).
func getAllIn[T any](ctx context.Context, c *Client, path string, q url.Values, gated bool, key string) ([]T, error) {
	return getPages(ctx, c, path, q, gated, nil, func(b []byte) ([]T, error) {
		var wrap map[string]json.RawMessage
		if err := json.Unmarshal(b, &wrap); err != nil {
			return nil, err
		}
		var items []T
		if raw, ok := wrap[key]; ok {
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, err
			}
		}
		return items, nil
	})
}

// getPages fetches page after page until the Link header has no rel="next" or
// stop (when set) says the items so far are enough. It re-issues the request
// with the next page number instead of following the absolute URL, so a
// hostile Link header can never redirect the token.
func getPages[T any](ctx context.Context, c *Client, path string, q url.Values, gated bool, stop func(page []T) bool, parse func([]byte) ([]T, error)) ([]T, error) {
	q = cloneQuery(q)
	if q.Get("per_page") == "" {
		q.Set("per_page", strconv.Itoa(pageSize))
	}
	var all []T
	for page := 1; ; {
		resp, err := c.do(ctx, request{method: http.MethodGet, path: path, query: q, gated: gated})
		if err != nil {
			return nil, err
		}
		b, rerr := io.ReadAll(resp.Body)
		next := nextPage(resp.Header)
		resp.Body.Close()
		if rerr != nil {
			return nil, fmt.Errorf("github %s: reading response: %w", path, rerr)
		}
		items, err := parse(b)
		if err != nil {
			return nil, fmt.Errorf("github %s: decoding response: %w", path, err)
		}
		all = append(all, items...)
		if next <= page || (stop != nil && stop(items)) {
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

// nextPage returns the page number of the Link header's rel="next", or 0 when
// the listing is finished.
func nextPage(h http.Header) int {
	for _, line := range h.Values("Link") {
		for part := range strings.SplitSeq(line, ",") {
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

// repoPath is the REST path of project ("owner/repo"); the slash separates
// two path segments and must stay one.
func repoPath(project string) string {
	owner, repo, _ := strings.Cut(project, "/")
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

func issuePath(ref forge.Ref) string {
	return repoPath(ref.Project) + "/issues/" + strconv.Itoa(ref.IID)
}

func pullPath(ref forge.Ref) string {
	return repoPath(ref.Project) + "/pulls/" + strconv.Itoa(ref.IID)
}

// splitProject splits "owner/repo" for GraphQL variables.
func splitProject(project string) (owner, name string) {
	owner, name, _ = strings.Cut(project, "/")
	return owner, name
}

func wantKind(ref forge.Ref, k forge.Kind) error {
	owner, repo := splitProject(ref.Project)
	if ref.Kind != k || owner == "" || repo == "" || strings.Contains(repo, "/") || ref.IID <= 0 {
		return fmt.Errorf("github: invalid reference %q", ref)
	}
	return nil
}

func stateOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
