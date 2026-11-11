package fake_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github/fake"
)

const (
	apiRoot = "/api/v3"
	tracker = apiRoot + "/repos/ade-demo/tracker"
	api     = apiRoot + "/repos/ade-demo/api"
	web     = apiRoot + "/repos/ade-demo/web"
)

// resp is a raw response, so the fake's wire behaviour is checked without
// the driver in between.
type resp struct {
	code int
	h    http.Header
	body []byte
}

func (r resp) json(t *testing.T) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("status %d: body is not JSON: %v\n%s", r.code, err, r.body)
	}
	return v
}

func (r resp) obj(t *testing.T) map[string]any {
	t.Helper()
	m, ok := r.json(t).(map[string]any)
	if !ok {
		t.Fatalf("status %d: want JSON object, got %s", r.code, r.body)
	}
	return m
}

func (r resp) arr(t *testing.T) []any {
	t.Helper()
	a, ok := r.json(t).([]any)
	if !ok {
		t.Fatalf("status %d: want JSON array, got %s", r.code, r.body)
	}
	return a
}

// doAs issues a request with an explicit Authorization header.
func doAs(t *testing.T, s *fake.Server, auth, method, path string, body any, hdr ...string) resp {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, s.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, res.Header, b}
}

func call(t *testing.T, s *fake.Server, method, path string, body any, hdr ...string) resp {
	t.Helper()
	return doAs(t, s, "Bearer "+fake.Token, method, path, body, hdr...)
}

func get(t *testing.T, s *fake.Server, path string) resp {
	t.Helper()
	return call(t, s, "GET", path, nil)
}

// expect fails unless the response has the wanted status.
func expect(t *testing.T, r resp, code int) resp {
	t.Helper()
	if r.code != code {
		t.Fatalf("status = %d, want %d: %s", r.code, code, r.body)
	}
	return r
}

// at walks a decoded JSON value: string keys into objects, ints into arrays.
func at(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		if v == nil { // absent or null: the caller compares the result
			return nil
		}
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("at %v: %v is not an object", path, v)
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				t.Fatalf("at %v: %v has no index %d", path, v, k)
			}
			v = a[k]
		}
	}
	return v
}

func str(t *testing.T, v any, path ...any) string {
	t.Helper()
	s, _ := at(t, v, path...).(string)
	return s
}

func num(t *testing.T, v any, path ...any) int {
	t.Helper()
	n, _ := at(t, v, path...).(float64)
	return int(n)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func has(m any, key string) bool {
	_, ok := m.(map[string]any)[key]
	return ok
}

// pluck maps every element of an array to one field.
func pluck[T any](t *testing.T, arr []any, path ...any) []T {
	t.Helper()
	out := make([]T, 0, len(arr))
	for _, e := range arr {
		v, _ := at(t, e, path...).(T)
		out = append(out, v)
	}
	return out
}

func numbers(t *testing.T, arr []any) []int {
	t.Helper()
	out := make([]int, 0, len(arr))
	for _, e := range arr {
		out = append(out, num(t, e, "number"))
	}
	return out
}

func newServer(t *testing.T, opts ...fake.Option) *fake.Server {
	t.Helper()
	s := fake.New(opts...)
	t.Cleanup(s.Close)
	return s
}

func issueRef(project string, n int) forge.Ref {
	return forge.Ref{Project: project, IID: n, Kind: forge.KindIssue}
}

func prRef(project string, n int) forge.Ref {
	return forge.Ref{Project: project, IID: n, Kind: forge.KindMR}
}

var clock = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func TestProtocol(t *testing.T) {
	s := newServer(t)

	t.Run("auth", func(t *testing.T) {
		for _, tc := range []struct {
			name, auth string
			want       int
		}{
			{"none", "", 401},
			{"wrong", "Bearer nope", 401},
			{"basic", "Basic " + fake.Token, 401},
			{"bearer", "Bearer " + fake.Token, 200},
			{"token scheme", "token " + fake.Token, 200},
		} {
			r := doAs(t, s, tc.auth, "GET", apiRoot+"/user", nil)
			if r.code != tc.want {
				t.Errorf("%s = %d, want %d", tc.name, r.code, tc.want)
			}
			if tc.want == 401 {
				if got := str(t, r.obj(t), "message"); got != "Bad credentials" {
					t.Errorf("%s message = %q", tc.name, got)
				}
			}
		}
	})

	t.Run("unknown routes", func(t *testing.T) {
		for _, p := range []string{apiRoot + "/nothing", "/user", apiRoot + "/repos/ade-demo/nope", apiRoot + "/repos/other/api"} {
			r := get(t, s, p)
			if r.code != 404 || str(t, r.obj(t), "message") != "Not Found" {
				t.Errorf("%s = %d %s", p, r.code, r.body)
			}
		}
	})

	t.Run("rate limit headers", func(t *testing.T) {
		r := get(t, s, apiRoot+"/user")
		if r.h.Get("X-RateLimit-Limit") == "" || r.h.Get("X-RateLimit-Remaining") == "" || r.h.Get("X-RateLimit-Reset") == "" {
			t.Errorf("missing rate limit headers: %v", r.h)
		}
	})

	t.Run("throttle", func(t *testing.T) {
		s.Throttle(2, "7")
		for i := range 2 {
			r := get(t, s, apiRoot+"/user")
			if r.code != 429 || r.h.Get("Retry-After") != "7" {
				t.Fatalf("throttled request %d = %d retry-after %q", i, r.code, r.h.Get("Retry-After"))
			}
		}
		expect(t, get(t, s, apiRoot+"/user"), 200)
		s.Throttle(1, "")
		if r := get(t, s, apiRoot+"/user"); r.code != 429 || r.h.Get("Retry-After") != "" {
			t.Errorf("throttle without header = %d %q", r.code, r.h.Get("Retry-After"))
		}
	})

	t.Run("primary rate limit", func(t *testing.T) {
		reset := clock.Add(time.Hour).Unix()
		s.RateLimit(1, reset)
		r := get(t, s, apiRoot+"/user")
		if r.code != 403 || r.h.Get("X-RateLimit-Remaining") != "0" || r.h.Get("X-RateLimit-Reset") != strconv.FormatInt(reset, 10) {
			t.Fatalf("rate limited = %d %v", r.code, r.h)
		}
		if !strings.Contains(str(t, r.obj(t), "message"), "API rate limit exceeded") {
			t.Errorf("message = %s", r.body)
		}
		expect(t, get(t, s, apiRoot+"/user"), 200)
	})

	t.Run("request log", func(t *testing.T) {
		s := newServer(t)
		get(t, s, apiRoot+"/repos/ade-demo/api/pulls/2/files?per_page=1")
		doAs(t, s, "", "GET", apiRoot+"/user", nil)
		get(t, s, tracker+"/issues/999")
		log := s.Requests()
		if len(log) != 3 {
			t.Fatalf("log = %+v", log)
		}
		want := []fake.Request{
			{"GET", api + "/pulls/2/files", "per_page=1", 200},
			{"GET", apiRoot + "/user", "", 401},
			{"GET", tracker + "/issues/999", "", 404},
		}
		if !reflect.DeepEqual(log, want) {
			t.Errorf("log = %+v, want %+v", log, want)
		}
	})

	t.Run("concurrency", func(t *testing.T) {
		s := newServer(t, fake.WithLatency(40*time.Millisecond))
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() { get(t, s, apiRoot+"/user") })
		}
		wg.Wait()
		if s.MaxInFlight() < 2 {
			t.Errorf("MaxInFlight = %d, want concurrent requests observed", s.MaxInFlight())
		}
	})
}

func TestUsersAndRepos(t *testing.T) {
	s := newServer(t)
	me := get(t, s, apiRoot+"/user").obj(t)
	if str(t, me, "login") != "demo-user" || num(t, me, "id") != 1001 || str(t, me, "name") != "Demo User" {
		t.Errorf("/user = %v", me)
	}
	for _, tc := range []struct {
		path, login, typ string
		code             int
	}{
		{apiRoot + "/user/1002", "teammate", "User", 200},
		{apiRoot + "/user/1003", "reviewer-bot", "Bot", 200},
		{apiRoot + "/user/4242", "", "", 404},
		{apiRoot + "/user/abc", "", "", 404},
		{apiRoot + "/users/teammate", "teammate", "User", 200},
		{apiRoot + "/users/ade-demo", "ade-demo", "Organization", 200},
		{apiRoot + "/users/ghost", "", "", 404},
	} {
		r := get(t, s, tc.path)
		if r.code != tc.code {
			t.Errorf("%s = %d, want %d", tc.path, r.code, tc.code)
			continue
		}
		if tc.code == 200 && (str(t, r.obj(t), "login") != tc.login || str(t, r.obj(t), "type") != tc.typ) {
			t.Errorf("%s = %s", tc.path, r.body)
		}
	}

	repo := get(t, s, api).obj(t)
	if str(t, repo, "full_name") != "ade-demo/api" || str(t, repo, "default_branch") != "main" || num(t, repo, "id") == 0 || str(t, repo, "node_id") == "" {
		t.Errorf("repo = %v", repo)
	}
	// Free tier: branch protection is not served.
	expect(t, get(t, s, api+"/branches/main/protection"), 404)
}

func TestMilestones(t *testing.T) {
	s := newServer(t)

	open := get(t, s, tracker+"/milestones").arr(t)
	if got := pluck[string](t, open, "title"); !slices.Equal(got, []string{"Sprint 42", "Sprint 43"}) {
		t.Errorf("open milestones = %v", got)
	}
	all := get(t, s, tracker+"/milestones?state=all&sort=due_on&direction=asc").arr(t)
	if got := pluck[string](t, all, "title"); !slices.Equal(got, []string{"Sprint 41", "Sprint 42", "Sprint 43"}) {
		t.Errorf("all milestones = %v", got)
	}
	desc := get(t, s, tracker+"/milestones?state=all&direction=desc").arr(t)
	if got := pluck[string](t, desc, "title"); !slices.Equal(got, []string{"Sprint 43", "Sprint 42", "Sprint 41"}) {
		t.Errorf("desc milestones = %v", got)
	}
	closed := get(t, s, tracker+"/milestones?state=closed").arr(t)
	if len(closed) != 1 || str(t, closed[0], "title") != "Sprint 41" {
		t.Errorf("closed = %v", closed)
	}

	for i, tc := range []struct {
		title, start, due string
	}{
		{"Sprint 41", "2026-10-19", "2026-10-30T12:00:00Z"},
		{"Sprint 42", "2026-11-02", "2026-11-13T12:00:00Z"},
		{"Sprint 43", "2026-11-16", "2026-11-27T12:00:00Z"},
	} {
		m := all[i]
		if str(t, m, "description") != "Start: "+tc.start+"\n\n"+tc.title || str(t, m, "due_on") != tc.due || num(t, m, "number") != i+1 {
			t.Errorf("milestone %s = %v", tc.title, m)
		}
	}
	// open_issues / closed_issues count issues and PRs: tracker #10 is the
	// one closed item in Sprint 42; #1-#6 and #11 are open.
	if num(t, all[1], "open_issues") != 7 || num(t, all[1], "closed_issues") != 1 {
		t.Errorf("Sprint 42 counts = %v/%v", all[1].(map[string]any)["open_issues"], all[1].(map[string]any)["closed_issues"])
	}

	// Ids are distinct across repos and kinds.
	seen := map[int]string{}
	for _, repo := range []string{tracker, api, web} {
		for _, m := range get(t, s, repo+"/milestones?state=all").arr(t) {
			id := num(t, m, "id")
			if prev, dup := seen[id]; dup {
				t.Errorf("milestone id %d reused: %s and %s", id, prev, repo)
			}
			seen[id] = repo
		}
		for _, it := range get(t, s, repo+"/issues?state=all").arr(t) {
			id := num(t, it, "id")
			if prev, dup := seen[id]; dup {
				t.Errorf("issue id %d reused: %s and %s", id, prev, repo)
			}
			seen[id] = repo
		}
	}

	t.Run("create", func(t *testing.T) {
		r := expect(t, call(t, s, "POST", tracker+"/milestones", map[string]any{
			"title": "Sprint 44", "description": "Start: 2026-11-30", "due_on": "2026-12-11T12:00:00Z",
		}), 201)
		m := r.obj(t)
		if num(t, m, "number") != 4 || str(t, m, "state") != "open" || str(t, m, "due_on") != "2026-12-11T12:00:00Z" {
			t.Errorf("created = %v", m)
		}
		ms := s.Milestones("ade-demo/tracker")
		if len(ms) != 4 || ms[3].Title != "Sprint 44" || ms[3].DueOn.Day() != 11 {
			t.Errorf("Milestones() = %+v", ms)
		}
		dup := expect(t, call(t, s, "POST", tracker+"/milestones", map[string]any{"title": "Sprint 44"}), 422).obj(t)
		if str(t, dup, "message") != "Validation Failed" || str(t, dup, "errors", 0, "code") != "already_exists" || str(t, dup, "errors", 0, "field") != "title" {
			t.Errorf("duplicate = %s", dup)
		}
		expect(t, call(t, s, "POST", tracker+"/milestones", map[string]any{"description": "no title"}), 422)
		expect(t, call(t, s, "POST", tracker+"/milestones", map[string]any{"title": "Bad", "due_on": "tomorrow"}), 422)
		expect(t, call(t, s, "POST", tracker+"/milestones", "{not json"), 400)
	})
}

func TestPagination(t *testing.T) {
	s := newServer(t, fake.WithPageSize(2))

	r := get(t, s, tracker+"/milestones?state=all&per_page=100")
	if got := len(r.arr(t)); got != 2 {
		t.Fatalf("per_page=100 under WithPageSize(2) returned %d items", got)
	}
	link := r.h.Get("Link")
	if !strings.Contains(link, `rel="next"`) || !strings.Contains(link, `rel="last"`) || strings.Contains(link, `rel="prev"`) {
		t.Fatalf("Link = %q", link)
	}
	next := linkTo(t, link, "next")
	if !strings.HasPrefix(next, s.URL+tracker+"/milestones?") {
		t.Errorf("next link %q is not absolute to the server", next)
	}
	last := linkTo(t, link, "last")
	if u, _ := url.Parse(last); u.Query().Get("page") != "2" {
		t.Errorf("last link = %q, want page=2", last)
	}
	r2 := call(t, s, "GET", strings.TrimPrefix(next, s.URL), nil)
	if got := len(r2.arr(t)); got != 1 || strings.Contains(r2.h.Get("Link"), `rel="next"`) {
		t.Errorf("last page: %d items, Link %q", got, r2.h.Get("Link"))
	}
	if !strings.Contains(r2.h.Get("Link"), `rel="prev"`) {
		t.Errorf("last page lacks rel=prev: %q", r2.h.Get("Link"))
	}

	// Following next links visits every item exactly once.
	var numbersSeen []int
	path := tracker + "/issues?state=all&per_page=100"
	for pages := 0; path != "" && pages < 20; pages++ {
		r := get(t, s, path)
		numbersSeen = append(numbersSeen, numbers(t, r.arr(t))...)
		path = ""
		if l := r.h.Get("Link"); strings.Contains(l, `rel="next"`) {
			path = strings.TrimPrefix(linkTo(t, l, "next"), s.URL)
		}
	}
	slices.Sort(numbersSeen)
	if want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}; !slices.Equal(numbersSeen, want) {
		t.Errorf("paged issues = %v", numbersSeen)
	}

	// Envelope endpoints paginate too and keep the overall total_count.
	cr := get(t, s, web+"/commits/1-paginate-the-audit-log-view/check-runs?per_page=2").obj(t)
	if num(t, cr, "total_count") != 3 || len(at(t, cr, "check_runs").([]any)) != 2 {
		t.Errorf("check-runs page 1 = %v", cr)
	}
}

func linkTo(t *testing.T, link, rel string) string {
	t.Helper()
	for part := range strings.SplitSeq(link, ",") {
		if strings.Contains(part, `rel="`+rel+`"`) {
			a, b, ok := strings.Cut(part, ">")
			if ok && b != "" {
				return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(a), "<"))
			}
		}
	}
	t.Fatalf("no rel=%s in %q", rel, link)
	return ""
}
