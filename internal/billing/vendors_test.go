package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type recorded struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (r *recorded) add(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req.Clone(context.Background()))
}

func fixedNow(s string) func() time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return t }
}

func near(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %v", name, want)
	}
	if math.Abs(*got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", name, *got, want)
	}
}

func TestAnthropicFetchPaginatesAndConvertsCents(t *testing.T) {
	var rec recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		if r.URL.Query().Get("page") == "" {
			fmt.Fprint(w, `{"data":[{"cost":{"amount":"41280.000000"}},{"cost":{"amount":"0.100000"}}],"has_more":true,"next_page":"p2"}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"cost":{"amount":"20.200000"}}],"has_more":false,"next_page":null,"data_refreshed_at":"2026-11-11T09:30:00Z"}`)
	}))
	defer srv.Close()

	src := NewAnthropic(srv.URL+"/", "sk-admin-secret", srv.Client(), fixedNow("2026-11-11T13:45:10Z"))
	if src.Name() != "anthropic" || src.Interval() != 6*time.Hour {
		t.Fatalf("name/interval = %s/%v", src.Name(), src.Interval())
	}
	snap, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	near(t, "spend", snap.Spend, 413.003) // (41280 + 0.1 + 20.2) cents / 100
	if snap.Limit != nil || snap.Remaining != nil {
		t.Fatalf("limit/remaining must stay nil: %+v", snap)
	}
	if snap.Scope != "org" || snap.Window != "month" || snap.Source != "anthropic" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if want := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC); snap.ResetAt == nil || !snap.ResetAt.Equal(want) {
		t.Fatalf("reset = %v, want %v", snap.ResetAt, want)
	}
	if want := time.Date(2026, 11, 11, 9, 30, 0, 0, time.UTC); !snap.AsOf.Equal(want) {
		t.Fatalf("as_of = %v, want data_refreshed_at %v", snap.AsOf, want)
	}
	if !strings.Contains(snap.Note, "Admin key") {
		t.Fatalf("note = %q", snap.Note)
	}

	if len(rec.reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(rec.reqs))
	}
	for i, r := range rec.reqs {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/organizations/analytics/cost" {
			t.Fatalf("req %d: %s %s", i, r.Method, r.URL.Path)
		}
		for k, v := range map[string]string{
			"x-api-key":         "sk-admin-secret",
			"anthropic-version": "2026-10-21",
			"anthropic-beta":    "analytics-usage-cost-2026-08-24",
			"User-Agent":        "ade",
		} {
			if got := r.Header.Get(k); got != v {
				t.Fatalf("req %d header %s = %q, want %q", i, k, got, v)
			}
		}
		q := r.URL.Query()
		if q.Get("starting_at") != "2026-11-01T00:00:00Z" || q.Get("ending_at") != "2026-11-11T13:00:00Z" || q.Get("limit") != "1000" {
			t.Fatalf("req %d query = %v", i, q)
		}
		if q.Has("time_bucket_width") {
			t.Fatalf("req %d must not bucket", i)
		}
	}
	if rec.reqs[0].URL.Query().Has("page") || rec.reqs[1].URL.Query().Get("page") != "p2" {
		t.Fatalf("page cursors: %q then %q", rec.reqs[0].URL.RawQuery, rec.reqs[1].URL.RawQuery)
	}
}

func TestAnthropicAsOfFallsBackToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"cost":{"amount":"100.000000"}}],"has_more":false,"data_refreshed_at":null}`)
	}))
	defer srv.Close()
	snap, err := NewAnthropic(srv.URL, "k", srv.Client(), fixedNow("2026-11-11T13:45:10Z")).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	near(t, "spend", snap.Spend, 1)
	if want := time.Date(2026, 11, 11, 13, 0, 0, 0, time.UTC); !snap.AsOf.Equal(want) {
		t.Fatalf("as_of = %v, want %v", snap.AsOf, want)
	}
}

func TestAnthropicFirstHourOfMonthSkipsCall(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	snap, err := NewAnthropic(srv.URL, "k", srv.Client(), fixedNow("2026-11-01T00:42:00Z")).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	near(t, "spend", snap.Spend, 0)
	if called {
		t.Fatal("no request expected in the first hour of the month")
	}
	if want := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC); snap.ResetAt == nil || !snap.ResetAt.Equal(want) {
		t.Fatalf("reset = %v", snap.ResetAt)
	}
}

func TestAnthropicBadAmountIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"cost":{"amount":"abc"}}]}`)
	}))
	defer srv.Close()
	_, err := NewAnthropic(srv.URL, "k", srv.Client(), fixedNow("2026-11-11T13:45:10Z")).Fetch(context.Background())
	var be *Error
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want *Error", err)
	}
}

func TestOpenAIFetchCompleteDaysAndPagination(t *testing.T) {
	var rec recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		if r.URL.Query().Get("page") == "" {
			fmt.Fprint(w, `{"data":[{"results":[{"amount":{"value":1.25,"currency":"usd"}},{"amount":{"value":0.06,"currency":"usd"}}]},{"results":[]}],"has_more":true,"next_page":"cur2"}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"results":[{"amount":{"value":3.5,"currency":"usd"}}]}],"has_more":false,"next_page":null}`)
	}))
	defer srv.Close()

	src := NewOpenAI(srv.URL, "sk-admin-oa", srv.Client(), fixedNow("2026-11-11T13:45:10Z"))
	if src.Name() != "openai" || src.Interval() != 6*time.Hour {
		t.Fatalf("name/interval = %s/%v", src.Name(), src.Interval())
	}
	snap, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	near(t, "spend", snap.Spend, 4.81)
	if want := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC); !snap.AsOf.Equal(want) {
		t.Fatalf("as_of = %v, want start of today %v", snap.AsOf, want)
	}
	if want := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC); snap.ResetAt == nil || !snap.ResetAt.Equal(want) {
		t.Fatalf("reset = %v", snap.ResetAt)
	}
	if snap.Scope != "org" || snap.Window != "month" || snap.Source != "openai" || snap.Limit != nil {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(rec.reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(rec.reqs))
	}
	for i, r := range rec.reqs {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/organization/costs" {
			t.Fatalf("req %d: %s %s", i, r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-admin-oa" {
			t.Fatalf("auth = %q", got)
		}
		q := r.URL.Query()
		wantStart := fmt.Sprint(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Unix())
		wantEnd := fmt.Sprint(time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC).Unix())
		if q.Get("start_time") != wantStart || q.Get("end_time") != wantEnd || q.Get("bucket_width") != "1d" || q.Get("limit") != "31" {
			t.Fatalf("req %d query = %v", i, q)
		}
	}
	if rec.reqs[1].URL.Query().Get("page") != "cur2" {
		t.Fatalf("second page cursor = %q", rec.reqs[1].URL.Query().Get("page"))
	}
}

func TestOpenAIFirstDayOfMonthHasNoCompleteDays(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	snap, err := NewOpenAI(srv.URL, "k", srv.Client(), fixedNow("2026-11-01T18:00:00Z")).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	near(t, "spend", snap.Spend, 0)
	if want := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC); !snap.AsOf.Equal(want) {
		t.Fatalf("as_of = %v, want %v", snap.AsOf, want)
	}
	if called {
		t.Fatal("no request expected on the first day of the month")
	}
}

func TestMoonshotFetch(t *testing.T) {
	var rec recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		fmt.Fprint(w, `{"code":0,"data":{"available_balance":49.58,"voucher_balance":46.58,"cash_balance":3.0},"scode":"0x0","status":true}`)
	}))
	defer srv.Close()
	src := NewMoonshot(srv.URL+"/", "sk-moon", srv.Client())
	if src.Name() != "moonshot" || src.Interval() != 5*time.Minute {
		t.Fatalf("name/interval = %s/%v", src.Name(), src.Interval())
	}
	snap, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := rec.reqs[0]
	if r.Method != http.MethodGet || r.URL.Path != "/v1/users/me/balance" || r.Header.Get("Authorization") != "Bearer sk-moon" {
		t.Fatalf("request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
	}
	near(t, "balance", snap.Balance, 49.58)
	near(t, "remaining", snap.Remaining, 49.58)
	if snap.Spend != nil || snap.Limit != nil {
		t.Fatalf("spend/limit must stay nil: %+v", snap)
	}
	if snap.Scope != "account" || snap.Source != "moonshot" || snap.AsOf.IsZero() {
		t.Fatalf("snapshot = %+v", snap)
	}
	if !strings.Contains(snap.Note, "46.58") || strings.Contains(snap.Note, "sk-moon") {
		t.Fatalf("note = %q", snap.Note)
	}
}

func TestMoonshotProviderErrorCode(t *testing.T) {
	for name, body := range map[string]string{
		"code":   `{"code":4001,"data":{},"scode":"0x4001","status":true}`,
		"status": `{"code":0,"data":{},"scode":"0x5","status":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			_, err := NewMoonshot(srv.URL, "sk-moon", srv.Client()).Fetch(context.Background())
			var be *Error
			if !errors.As(err, &be) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if !strings.Contains(err.Error(), "0x") || strings.Contains(err.Error(), "sk-moon") {
				t.Fatalf("error = %q", err)
			}
		})
	}
}

func TestOpenRouterNullLimitStaysNil(t *testing.T) {
	var rec recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		fmt.Fprint(w, `{"data":{"limit":null,"limit_remaining":null,"limit_reset":null,"usage":12.5,"usage_daily":1,"usage_weekly":2,"usage_monthly":3,"effective_budget":null}}`)
	}))
	defer srv.Close()
	src := NewOpenRouter(srv.URL+"/", "sk-or", srv.Client())
	if src.Name() != "openrouter" || src.Interval() != 60*time.Second {
		t.Fatalf("name/interval = %s/%v", src.Name(), src.Interval())
	}
	snap, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := rec.reqs[0]
	if r.Method != http.MethodGet || r.URL.Path != "/api/v1/key" || r.Header.Get("Authorization") != "Bearer sk-or" {
		t.Fatalf("request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
	}
	if snap.Limit != nil || snap.Remaining != nil {
		t.Fatalf("null limit must stay nil: limit=%v remaining=%v", snap.Limit, snap.Remaining)
	}
	near(t, "spend", snap.Spend, 12.5)
	if snap.Scope != "key" || snap.Window != "" || snap.ResetAt != nil {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestOpenRouterLimitWithoutEffectiveBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"limit":50,"limit_remaining":37.5,"limit_reset":"monthly","usage":12.5}}`)
	}))
	defer srv.Close()
	snap, err := NewOpenRouter(srv.URL, "k", srv.Client()).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	near(t, "limit", snap.Limit, 50)
	near(t, "remaining", snap.Remaining, 37.5)
	near(t, "spend", snap.Spend, 12.5)
	if snap.Window != "monthly" {
		t.Fatalf("window = %q", snap.Window)
	}
}

func TestOpenRouterEffectiveBudgetTakesPrecedence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"limit":50,"limit_remaining":37.5,"limit_reset":"monthly","usage":12.5,
			"effective_budget":{"limit_usd":20,"spend_usd":15,"remaining_usd":5,"reset_interval":"daily","resets_at":"2026-11-12T00:00:00Z"}}}`)
	}))
	defer srv.Close()
	snap, err := NewOpenRouter(srv.URL, "k", srv.Client()).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	near(t, "limit", snap.Limit, 20)
	near(t, "spend", snap.Spend, 15)
	near(t, "remaining", snap.Remaining, 5)
	if snap.Window != "daily" {
		t.Fatalf("window = %q", snap.Window)
	}
	if want := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC); snap.ResetAt == nil || !snap.ResetAt.Equal(want) {
		t.Fatalf("reset = %v", snap.ResetAt)
	}
}

func TestVendorAuthFailureSurfacesStatus(t *testing.T) {
	now := fixedNow("2026-11-11T13:45:10Z")
	cases := map[string]func(url string, hc *http.Client) Source{
		"anthropic":  func(u string, hc *http.Client) Source { return NewAnthropic(u, "sk-secret-key", hc, now) },
		"openai":     func(u string, hc *http.Client) Source { return NewOpenAI(u, "sk-secret-key", hc, now) },
		"moonshot":   func(u string, hc *http.Client) Source { return NewMoonshot(u, "sk-secret-key", hc) },
		"openrouter": func(u string, hc *http.Client) Source { return NewOpenRouter(u, "sk-secret-key", hc) },
	}
	for name, mk := range cases {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			t.Run(fmt.Sprintf("%s/%d", name, status), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, `{"error":"nope"}`, status)
				}))
				defer srv.Close()
				_, err := mk(srv.URL, srv.Client()).Fetch(context.Background())
				var be *Error
				if !errors.As(err, &be) {
					t.Fatalf("err = %v, want *Error", err)
				}
				if be.Status != status || be.Source != name {
					t.Fatalf("error = %+v", be)
				}
				if strings.Contains(err.Error(), "sk-secret-key") {
					t.Fatalf("error leaks key: %q", err)
				}
			})
		}
	}
}
