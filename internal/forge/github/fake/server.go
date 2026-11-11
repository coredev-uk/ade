// Package fake is an in-process GitHub for tests: an httptest server serving
// the subset of the REST (under /api/v3) and GraphQL (/api/graphql) APIs ADE
// reads and writes, the way GitHub Enterprise Server lays them out, over data
// mirroring the sandbox repos seeded by scripts/sandbox/github-seed.sh.
//
// The fake answers like a Free-tier github.com account: issues carry no weight
// or iteration fields and branch protection endpoints are not served. Handlers
// are grouped by resource (handlers_*.go); all of them run under one mutex and
// render while holding it, so every read reflects every earlier write.
package fake

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Token is the only credential the fake accepts.
const Token = "fake-token"

const (
	docURL   = "https://docs.github.com/rest"
	apiPath  = "/api/v3"
	rateSize = 5000
)

// Request is one request the fake served, for assertions.
type Request struct {
	Method string
	Path   string // escaped path, e.g. /api/v3/repos/ade-demo/api/pulls/2
	Query  string
	Status int
}

// Server is a running fake GitHub. The embedded httptest.Server provides URL
// and Close.
type Server struct {
	*httptest.Server

	opts options

	mu  sync.Mutex // guards st and the request bookkeeping below
	st  *state
	log []Request

	inflight, maxInflight int
	throttle              throttle
	rate                  rateLimit
}

type throttle struct {
	remaining  int
	retryAfter string
}

type rateLimit struct {
	remaining int
	reset     int64
}

type options struct {
	clock    time.Time
	pageSize int
	latency  time.Duration
}

// Option customises a Server.
type Option func(*options)

// WithClock fixes "now": seeded timestamps are laid out relative to it. The
// default is 2026-11-11 12:00 UTC.
func WithClock(t time.Time) Option { return func(o *options) { o.clock = t } }

// WithPageSize caps per_page below GitHub's 100, so small data sets paginate.
func WithPageSize(n int) Option { return func(o *options) { o.pageSize = n } }

// WithLatency delays every response, letting tests observe concurrency.
func WithLatency(d time.Duration) Option { return func(o *options) { o.latency = d } }

// New starts a fake GitHub. Close it when done.
func New(opts ...Option) *Server {
	o := options{clock: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), pageSize: 100}
	for _, f := range opts {
		f(&o)
	}
	s := &Server{opts: o, st: seed(o.clock)}
	s.Server = httptest.NewServer(s.routes())
	return s
}

// Requests returns a copy of the request log.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.log...)
}

// MaxInFlight is the highest number of simultaneously served requests.
func (s *Server) MaxInFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInflight
}

// Throttle makes the next n requests answer 429 with the given Retry-After
// header value (seconds, or "" for none), like GitHub's secondary rate limit.
func (s *Server) Throttle(n int, retryAfter string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.throttle = throttle{remaining: n, retryAfter: retryAfter}
}

// RateLimit makes the next n requests answer 403 "API rate limit exceeded"
// with X-RateLimit-Remaining 0 and X-RateLimit-Reset set to resetUnix, like
// GitHub's primary rate limit.
func (s *Server) RateLimit(n int, resetUnix int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rate = rateLimit{remaining: n, reset: resetUnix}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	s.userRoutes(mux)
	s.gitRoutes(mux)
	s.issueRoutes(mux)
	s.pullRoutes(mux)
	s.reviewRoutes(mux)
	s.checkRoutes(mux)
	s.discoverRoutes(mux)
	mux.HandleFunc("POST /api/graphql", s.graphql)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { notFound(w) })
	return http.HandlerFunc(s.serve(mux))
}

// handle registers a REST route under /api/v3. The handler runs with s.mu
// held, so it may read and write the state and render responses freely.
func (s *Server) handle(mux *http.ServeMux, pattern string, h func(w http.ResponseWriter, r *http.Request)) {
	method, path, _ := strings.Cut(pattern, " ")
	mux.HandleFunc(method+" "+apiPath+path, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		h(w, r)
	})
}

// serve wraps the mux with the cross-cutting behaviour of the real service:
// rate limiting, authentication, latency and request accounting.
func (s *Server) serve(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		s.begin()
		defer func() { s.end(r, rec.status) }()
		if s.opts.latency > 0 {
			time.Sleep(s.opts.latency)
		}
		switch {
		case s.throttled(rec):
		case s.rateLimited(rec):
		case !authorised(r):
			rec.Header().Set("X-RateLimit-Limit", "60")
			apiError(rec, http.StatusUnauthorized, "Bad credentials")
		default:
			rec.Header().Set("X-RateLimit-Limit", strconv.Itoa(rateSize))
			rec.Header().Set("X-RateLimit-Remaining", strconv.Itoa(rateSize-1))
			rec.Header().Set("X-RateLimit-Reset", strconv.FormatInt(s.opts.clock.Add(time.Hour).Unix(), 10))
			rec.Header().Set("X-RateLimit-Resource", "core")
			next.ServeHTTP(rec, r)
		}
	}
}

func authorised(r *http.Request) bool {
	scheme, cred, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	return (strings.EqualFold(scheme, "bearer") || strings.EqualFold(scheme, "token")) && cred == Token
}

func (s *Server) begin() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight++
	s.maxInflight = max(s.maxInflight, s.inflight)
}

func (s *Server) end(r *http.Request, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight--
	s.log = append(s.log, Request{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Status: status})
}

func (s *Server) throttled(w http.ResponseWriter) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.throttle.remaining == 0 {
		return false
	}
	s.throttle.remaining--
	if s.throttle.retryAfter != "" {
		w.Header().Set("Retry-After", s.throttle.retryAfter)
	}
	apiError(w, http.StatusTooManyRequests, "You have exceeded a secondary rate limit. Please wait a few minutes before you try again.")
	return true
}

func (s *Server) rateLimited(w http.ResponseWriter) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rate.remaining == 0 {
		return false
	}
	s.rate.remaining--
	h := w.Header()
	h.Set("X-RateLimit-Limit", strconv.Itoa(rateSize))
	h.Set("X-RateLimit-Remaining", "0")
	h.Set("X-RateLimit-Reset", strconv.FormatInt(s.rate.reset, 10))
	h.Set("X-RateLimit-Resource", "core")
	apiError(w, http.StatusForbidden, "API rate limit exceeded for user ID 1001.")
	return true
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"message": msg, "documentation_url": docURL})
}

func notFound(w http.ResponseWriter) { apiError(w, http.StatusNotFound, "Not Found") }

// fieldError is one entry of a 422 Validation Failed response.
type fieldError struct {
	Resource string `json:"resource,omitempty"`
	Field    string `json:"field,omitempty"`
	Code     string `json:"code"`
	Message  string `json:"message,omitempty"`
}

// validation answers 422 Validation Failed with the given error entries.
func validation(w http.ResponseWriter, errs ...fieldError) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"message": "Validation Failed", "errors": errs, "documentation_url": docURL,
	})
}

// custom answers 422 Validation Failed with one free-text error.
func custom(w http.ResponseWriter, resource, msg string) {
	validation(w, fieldError{Resource: resource, Code: "custom", Message: msg})
}

// decode reads a JSON request body into v, answering 400 on malformed JSON.
// An empty body leaves v untouched.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	if err != nil && !errors.Is(err, io.EOF) {
		apiError(w, http.StatusBadRequest, "Problems parsing JSON")
		return false
	}
	return true
}

// page applies GitHub's offset pagination to a result of n items: it sets the
// Link header and returns the slice bounds for the requested page.
func (s *Server) page(w http.ResponseWriter, r *http.Request, n int) (lo, hi int) {
	per := atoiOr(r.URL.Query().Get("per_page"), 30)
	per = max(1, min(per, s.opts.pageSize))
	pg := max(1, atoiOr(r.URL.Query().Get("page"), 1))
	lo = min((pg-1)*per, n)
	hi = min(lo+per, n)
	last := max(1, (n+per-1)/per)
	var links []string
	if pg > 1 {
		links = append(links, `<`+pageLink(r, pg-1, per)+`>; rel="prev"`)
	}
	if hi < n {
		links = append(links, `<`+pageLink(r, pg+1, per)+`>; rel="next"`, `<`+pageLink(r, last, per)+`>; rel="last"`)
	}
	if pg > 1 {
		links = append(links, `<`+pageLink(r, 1, per)+`>; rel="first"`)
	}
	if len(links) > 0 {
		w.Header().Set("Link", strings.Join(links, ", "))
	}
	return lo, hi
}

func pageLink(r *http.Request, page, per int) string {
	q := r.URL.Query()
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(per))
	return "http://" + r.Host + r.URL.EscapedPath() + "?" + q.Encode()
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// paged writes one page of items rendered by render.
func paged[T any](s *Server, w http.ResponseWriter, r *http.Request, items []T, render func(T) any) {
	lo, hi := s.page(w, r, len(items))
	out := make([]any, 0, hi-lo)
	for _, it := range items[lo:hi] {
		out = append(out, render(it))
	}
	writeJSON(w, http.StatusOK, out)
}

// pagedEnvelope writes a paginated {total_count, <key>: [...]} response.
func pagedEnvelope[T any](s *Server, w http.ResponseWriter, r *http.Request, key string, items []T, render func(T) any) {
	lo, hi := s.page(w, r, len(items))
	out := make([]any, 0, hi-lo)
	for _, it := range items[lo:hi] {
		out = append(out, render(it))
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(items), key: out})
}

// queryList reads a comma-separated array parameter such as labels=a,b.
func queryList(r *http.Request, name string) []string {
	var out []string
	for _, v := range r.URL.Query()[name] {
		for p := range strings.SplitSeq(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
