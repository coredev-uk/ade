package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

func TestNextPage(t *testing.T) {
	link := func(rels ...string) string { return strings.Join(rels, ", ") }
	tests := []struct {
		name string
		h    http.Header
		want int
	}{
		{"x-next-page", http.Header{"X-Next-Page": {"3"}}, 3},
		{"x-next-page empty on the last page", http.Header{"X-Next-Page": {""}, "X-Page": {"3"}}, 0},
		{"no headers", http.Header{}, 0},
		{"link only", http.Header{"Link": {link(
			`<https://gl.example/api/v4/projects/1/issues?page=1&per_page=20>; rel="first"`,
			`<https://gl.example/api/v4/projects/1/issues?id=1&page=2&per_page=20>; rel="next"`,
			`<https://gl.example/api/v4/projects/1/issues?page=9&per_page=20>; rel="last"`)}}, 2},
		{"link without next", http.Header{"Link": {`<https://gl.example/x?page=1>; rel="first", <https://gl.example/x?page=9>; rel="last"`}}, 0},
		{"keyset link has no page", http.Header{"Link": {`<https://gl.example/x?cursor=abc>; rel="next"`}}, 0},
		{"garbage", http.Header{"X-Next-Page": {"soon"}, "Link": {"???"}}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextPage(tc.h); got != tc.want {
				t.Errorf("nextPage = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		header  string
		attempt int
		want    time.Duration
	}{
		{"seconds", "7", 0, 7 * time.Second},
		{"padded", " 7 ", 0, 7 * time.Second},
		{"zero", "0", 2, 0},
		{"negative clamps", "-5", 0, 0},
		{"http date", now.Add(90 * time.Second).UTC().Format(http.TimeFormat), 0, 90 * time.Second},
		{"past http date", now.Add(-time.Minute).UTC().Format(http.TimeFormat), 0, 0},
		{"missing: exponential", "", 0, time.Second},
		{"missing: exponential, later", "", 3, 8 * time.Second},
		{"missing: exponential is bounded", "", 40, 64 * time.Second},
		{"unparseable falls back", "soon", 1, 2 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.header != "" {
				h.Set("Retry-After", tc.header)
			}
			if got := retryAfter(h, now, tc.attempt); got != tc.want {
				t.Errorf("retryAfter = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIterationState(t *testing.T) {
	d := func(day int) forge.Date { return forge.NewDate(2026, 11, day) }
	now := time.Date(2026, 11, 11, 23, 59, 0, 0, time.UTC)
	tests := []struct {
		name       string
		start, due forge.Date
		want       string
	}{
		{"starts later", d(12), d(20), "upcoming"},
		{"started today", d(11), d(20), "current"},
		{"due today is still current", d(1), d(11), "current"},
		{"ended yesterday", d(1), d(10), "closed"},
		{"single day, today", d(11), d(11), "current"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := iterationState(tc.start, tc.due, now); got != tc.want {
				t.Errorf("state = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWireDate(t *testing.T) {
	var v struct {
		D wireDate `json:"d"`
	}
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{`{"d":"2026-11-02"}`, "2026-11-02", false},
		{`{"d":null}`, "", false},
		{`{"d":""}`, "", false},
		{`{}`, "", false},
		{`{"d":"2026-13-45"}`, "", true},
		{`{"d":"2026-11-02T00:00:00Z"}`, "", true},
		{`{"d":7}`, "", true},
	} {
		v.D = wireDate{}
		err := json.Unmarshal([]byte(tc.in), &v)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if got := v.D.date().String(); got != tc.want {
			t.Errorf("%s: date = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestErrorMessage(t *testing.T) {
	tests := []struct{ body, want string }{
		{`{"message":"404 Project Not Found"}`, "404 Project Not Found"},
		{`{"error":"insufficient_scope"}`, "insufficient_scope"},
		{`{"message":{"base":["is invalid"]}}`, `{"base":["is invalid"]}`},
		{"Retry later\n", "Retry later"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := errorMessage([]byte(tc.body)); got != tc.want {
			t.Errorf("errorMessage(%q) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

func TestStatusMapping(t *testing.T) {
	tests := []struct {
		status int
		gated  bool
		want   error // nil: an *APIError without a sentinel
	}{
		{http.StatusUnauthorized, false, forge.ErrAuth},
		{http.StatusUnauthorized, true, forge.ErrAuth},
		{http.StatusForbidden, false, forge.ErrAuth},
		{http.StatusForbidden, true, forge.ErrUnavailable},
		{http.StatusNotFound, false, forge.ErrNotFound},
		{http.StatusNotFound, true, forge.ErrUnavailable},
		{http.StatusBadRequest, false, nil},
		{http.StatusInternalServerError, true, nil},
		{http.StatusBadGateway, false, nil},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d gated=%v", tc.status, tc.gated), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"nope"}`))
			}))
			defer srv.Close()
			c := New(srv.URL, "secret-token")
			err := c.getJSON(context.Background(), "/x", nil, tc.gated, &struct{}{})
			var aerr *APIError
			if !errors.As(err, &aerr) || aerr.Status != tc.status || aerr.Message != "nope" {
				t.Fatalf("err = %v, want *APIError %d", err, tc.status)
			}
			for _, s := range []error{forge.ErrAuth, forge.ErrNotFound, forge.ErrUnavailable} {
				if got, want := errors.Is(err, s), s == tc.want; got != want {
					t.Errorf("errors.Is(%v) = %v, want %v", s, got, want)
				}
			}
			if strings.Contains(err.Error(), "secret-token") {
				t.Error("error leaks the token")
			}
		})
	}
}

// 5xx is a server fault, not a rate limit: it must not be retried.
func TestServerErrorsAreNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := New(srv.URL, "t", WithSleep(func(context.Context, time.Duration) error { return nil }))
	if _, err := c.Me(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("%d calls, want 1", n)
	}
}

// Pagination must work from the Link header alone and must stop on a server
// that keeps announcing the same page.
func TestPaginationEdgeCases(t *testing.T) {
	t.Run("link header only", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("page") {
			case "", "1":
				w.Header().Set("Link", `<http://elsewhere.invalid/x?page=2>; rel="next"`)
				_, _ = w.Write([]byte(`[{"id":1}]`))
			default:
				_, _ = w.Write([]byte(`[{"id":2}]`))
			}
		}))
		defer srv.Close()
		got, err := getAll[struct{ ID int }](context.Background(), New(srv.URL, "t"), "/x", nil, false)
		if err != nil || len(got) != 2 || got[1].ID != 2 {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("stuck server", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("X-Next-Page", "1")
			_, _ = w.Write([]byte(`[{"id":1}]`))
		}))
		defer srv.Close()
		got, err := getAll[struct{ ID int }](context.Background(), New(srv.URL, "t"), "/x", nil, false)
		if err != nil || len(got) != 1 || calls.Load() != 1 {
			t.Errorf("got %+v after %d calls, %v", got, calls.Load(), err)
		}
	})
	t.Run("does not mutate the caller's query", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) }))
		defer srv.Close()
		q := map[string][]string{"state": {"all"}}
		if _, err := getAll[struct{}](context.Background(), New(srv.URL, "t"), "/x", q, false); err != nil {
			t.Fatal(err)
		}
		if len(q) != 1 {
			t.Errorf("query mutated: %v", q)
		}
	})
}

func TestMalformedBodyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) }))
	defer srv.Close()
	_, err := New(srv.URL, "t").Me(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decoding response") {
		t.Errorf("err = %v", err)
	}
}

func TestGraphQLFailures(t *testing.T) {
	serve := func(body string) *Client {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		t.Cleanup(srv.Close)
		return New(srv.URL, "t")
	}
	var out struct{}
	ctx := context.Background()

	err := serve(`{"errors":[{"message":"Field 'x' doesn't exist"},{"message":"second"}]}`).graphql(ctx, "q", nil, &out)
	if err == nil || !strings.Contains(err.Error(), "Field 'x' doesn't exist; second") {
		t.Errorf("errors without data: %v", err)
	}
	if err := serve(`{"data":null}`).graphql(ctx, "q", nil, &out); err == nil {
		t.Error("null data without errors must fail")
	}
	if err := serve(`{"data":{"group":null},"errors":[{"message":"denied"}]}`).graphql(ctx, "q", nil, &out); err != nil {
		t.Errorf("partial data with field errors is not a failure: %v", err)
	}
	if err := serve(`{"data":[1]}`).graphql(ctx, "q", nil, &struct{ A string }{}); err == nil {
		t.Error("data of the wrong shape must fail")
	}
}

func TestGraphQLGroupNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"group":null}}`))
	}))
	defer srv.Close()
	_, _, err := New(srv.URL, "t").probeGraphQL(context.Background(), "ghost")
	if !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestApproverLicenceDecidesRequired(t *testing.T) {
	yes, no := true, false
	two := 2
	tests := []struct {
		name string
		in   wireApprovals
		want int
	}{
		{"licensed", wireApprovals{Required: &two, ApproversAvailable: &yes}, 2},
		{"unlicensed reports 0, means unknown", wireApprovals{Required: new(0), ApproversAvailable: &no}, -1},
		{"flag absent, trust the number", wireApprovals{Required: &two}, 2},
		{"CE: no number at all", wireApprovals{ApproversAvailable: &yes}, -1},
	}
	for _, tc := range tests {
		if got := tc.in.approvals().Required; got != tc.want {
			t.Errorf("%s: Required = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestThreadCounts(t *testing.T) {
	ds := []forge.Discussion{
		{Resolvable: true},
		{Resolvable: true, Resolved: true},
		{Resolvable: true},
		{Resolvable: false, Resolved: false}, // system or plain comment
	}
	if u, r := threadCounts(ds); u != 2 || r != 1 {
		t.Errorf("counts = %d/%d, want 2/1", u, r)
	}
}

func TestDiscussionResolvedNeedsEveryResolvableNote(t *testing.T) {
	d := wireDiscussion{ID: "d", Notes: []wireNote{
		{Resolvable: true, Resolved: true},
		{Resolvable: true, Resolved: false},
		{Resolvable: false}, // a non-resolvable reply does not decide
	}}.discussion()
	if !d.Resolvable || d.Resolved {
		t.Errorf("discussion = %+v, want resolvable and unresolved", d)
	}
	d = wireDiscussion{Notes: []wireNote{{Resolvable: true, Resolved: true}, {}}}.discussion()
	if !d.Resolved {
		t.Error("all resolvable notes resolved: want Resolved")
	}
}

func TestRefOfFallsBack(t *testing.T) {
	if got := refOf("g/p!4", "x/y", 4, forge.KindMR); got != (forge.Ref{Project: "g/p", IID: 4, Kind: forge.KindMR}) {
		t.Errorf("full ref ignored: %v", got)
	}
	// A reference of the wrong kind or an absent one must not be trusted.
	for _, full := range []string{"", "g/p#4", "garbage"} {
		if got := refOf(full, "x/y", 4, forge.KindMR); got != (forge.Ref{Project: "x/y", IID: 4, Kind: forge.KindMR}) {
			t.Errorf("refOf(%q) = %v", full, got)
		}
	}
}
