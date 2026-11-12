// Package fake is an in-process GitLab for tests: an httptest server serving
// the subset of the REST and GraphQL APIs ADE reads, over data mirroring the
// sandbox group seeded by scripts/sandbox/gitlab-seed.sh.
//
// A Tier switch decides what the instance licenses. Free answers like the
// verified gitlab.com Free group: iterations are 403, `weight`, `iteration`
// and `epic` are absent from issues, and approval rules are empty. Premium
// adds the gated data. Handlers are grouped by resource (handlers_*.go) so
// the write endpoints later phases need slot in beside the reads.
package fake

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tier selects the licence level the fake emulates.
type Tier int

const (
	Free Tier = iota
	Premium
)

func (t Tier) String() string {
	if t == Premium {
		return "premium"
	}
	return "free"
}

// Token is the only credential the fake accepts.
const Token = "fake-token"

// Request is one request the fake served, for assertions.
type Request struct {
	Method string
	Path   string // escaped path, e.g. /api/v4/projects/a%2Fb/issues
	Query  string
	Status int
}

// Server is a running fake GitLab. The embedded httptest.Server provides URL
// and Close.
type Server struct {
	*httptest.Server
	tier Tier

	opts options

	mu sync.Mutex // guards st and the request bookkeeping below
	st *state
	// extState holds per-resource state registered through ext.
	extState map[string]any
	log      []Request

	inflight, maxInflight int
	throttle              throttle
	rate                  rateWindow
}

// rateWindow is the fixed window WithRateLimit counts requests in.
type rateWindow struct {
	reset time.Time
	count int
}

type throttle struct {
	remaining  int
	retryAfter string
}

type options struct {
	clock    time.Time
	pageSize int
	latency  time.Duration
	gitRoot  string

	noMRDetails   bool
	userNamespace bool
	rateLimit     int
	rateWindow    time.Duration

	selfHosted
}

// Option customises a Server.
type Option func(*options)

// WithClock fixes "now": Premium iterations are laid out around it and
// timestamps are relative to it. The default is 2026-11-11 12:00 UTC.
func WithClock(t time.Time) Option { return func(o *options) { o.clock = t } }

// WithPageSize caps per_page below GitLab's 100, so small data sets paginate.
func WithPageSize(n int) Option { return func(o *options) { o.pageSize = n } }

// WithLatency delays every response, letting tests observe concurrency.
func WithLatency(d time.Duration) Option { return func(o *options) { o.latency = d } }

// WithRateLimit makes the fake rate-limit like GitLab.com: n requests per
// window, RateLimit-* headers on every answer, and a 429 with Retry-After for
// the requests past n until the window ends.
func WithRateLimit(n int, window time.Duration) Option {
	return func(o *options) { o.rateLimit, o.rateWindow = n, window }
}

// WithUserNamespace makes GroupPath a user's personal namespace instead of a
// group, as on GitLab: /namespaces/:id answers kind "user", every /groups/:id
// endpoint is 404, and milestones belong to the projects.
func WithUserNamespace() Option { return func(o *options) { o.userNamespace = true } }

// SetLatency changes the per-response delay of a running server.
func (s *Server) SetLatency(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.latency = d
}

// New starts a fake GitLab of the given tier. Close it when done.
func New(tier Tier, opts ...Option) *Server {
	o := options{clock: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), pageSize: 100}
	for _, f := range opts {
		f(&o)
	}
	s := &Server{tier: tier, opts: o, st: seed(o.clock, tier)}
	s.st.userNamespace = o.userNamespace
	s.Server = s.start()
	return s
}

// Tier reports the emulated licence level.
func (s *Server) Tier() Tier { return s.tier }

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
// header value (seconds, or "" for none).
func (s *Server) Throttle(n int, retryAfter string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.throttle = throttle{remaining: n, retryAfter: retryAfter}
}

// extraRoutes are route sets other files register from init(), so a new
// resource's handlers live in its own file without editing this router.
var extraRoutes []func(*Server, *http.ServeMux)

// registerRoutes adds a route set. Call it from an init() in the file that
// defines the handlers; the function receives the server and the mux of each
// new Server. Patterns must not overlap routes registered elsewhere.
func registerRoutes(fn func(*Server, *http.ServeMux)) { extraRoutes = append(extraRoutes, fn) }

// ext returns the per-server value stored under key, creating it with init on
// first use. It lets a resource keep its own state beside the shared one
// without adding fields to state. The caller holds s.mu, as every handler does.
func (s *Server) ext(key string, init func() any) any {
	if s.extState == nil {
		s.extState = map[string]any{}
	}
	v, ok := s.extState[key]
	if !ok {
		v = init()
		s.extState[key] = v
	}
	return v
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	s.userRoutes(mux)
	s.issueRoutes(mux)
	s.mrRoutes(mux)
	s.reviewRoutes(mux)
	s.planningRoutes(mux)
	s.graphqlRoutes(mux)
	for _, fn := range extraRoutes {
		fn(s, mux)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { notFound(w) })
	return http.HandlerFunc(s.serve(mux))
}

// serve wraps the mux with the cross-cutting behaviour of the real service:
// authentication, rate limiting, latency and request accounting.
func (s *Server) serve(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		s.begin()
		defer func() { s.end(r, rec.status) }()
		s.mu.Lock()
		latency := s.opts.latency
		s.mu.Unlock()
		if latency > 0 {
			time.Sleep(latency)
		}
		switch {
		case s.rateLimited(rec):
		case s.throttled(rec):
		case r.Header.Get("PRIVATE-TOKEN") != Token:
			writeJSON(rec, http.StatusUnauthorized, map[string]string{"message": "401 Unauthorized"})
		case s.opts.missing(r):
			notFound(rec)
		default:
			next.ServeHTTP(rec, r)
		}
	}
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

// rateLimited counts the request against the WithRateLimit window.
func (s *Server) rateLimited(w http.ResponseWriter) bool {
	if s.opts.rateLimit <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.After(s.rate.reset) {
		s.rate = rateWindow{reset: now.Add(s.opts.rateWindow)}
	}
	s.rate.count++
	secs := int64(math.Ceil(time.Until(s.rate.reset).Seconds()))
	h := w.Header()
	h.Set("RateLimit-Limit", strconv.Itoa(s.opts.rateLimit))
	h.Set("RateLimit-Remaining", strconv.Itoa(max(s.opts.rateLimit-s.rate.count, 0)))
	h.Set("RateLimit-Reset", strconv.FormatInt(now.Unix()+secs, 10))
	if s.rate.count <= s.opts.rateLimit {
		return false
	}
	h.Set("Retry-After", strconv.FormatInt(secs, 10))
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte("Retry later\n"))
	return true
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
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte("Retry later\n"))
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
}

func forbidden(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]string{"message": "403 Forbidden"})
}

// page applies GitLab's offset pagination to a result of n items: it sets
// the pagination headers (never X-Total, which GitLab omits for large sets)
// and returns the slice bounds for the requested page.
func (s *Server) page(w http.ResponseWriter, r *http.Request, n int) (lo, hi int) {
	per := atoiOr(r.URL.Query().Get("per_page"), 20)
	per = max(1, min(per, s.opts.pageSize))
	pg := max(1, atoiOr(r.URL.Query().Get("page"), 1))
	lo = min((pg-1)*per, n)
	hi = min(lo+per, n)
	h := w.Header()
	h.Set("X-Page", strconv.Itoa(pg))
	h.Set("X-Per-Page", strconv.Itoa(per))
	if pg > 1 {
		h.Set("X-Prev-Page", strconv.Itoa(pg-1))
	}
	if hi < n {
		h.Set("X-Next-Page", strconv.Itoa(pg+1))
		h.Set("Link", `<`+nextLink(r, pg+1)+`>; rel="next"`)
	}
	return lo, hi
}

func nextLink(r *http.Request, page int) string {
	q := r.URL.Query()
	q.Set("page", strconv.Itoa(page))
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

// queryList reads an array parameter in either `name[]=a` or `name=a,b` form.
func queryList(r *http.Request, name string) []string {
	q := r.URL.Query()
	var out []string
	out = append(out, q[name+"[]"]...)
	for _, v := range q[name] {
		out = append(out, strings.Split(v, ",")...)
	}
	return out
}
