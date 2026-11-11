package billing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/store"
)

func TestLiteLLMKeyInfoSnapshot(t *testing.T) {
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		io.WriteString(w, `{"key":"sk-x","info":{"spend":0.0081,"max_budget":0.01,"soft_budget":0.008,"budget_duration":"30d","budget_reset_at":"2026-12-01T00:00:00+00:00","team_id":null,"user_id":"u1"}}`)
	}))
	defer srv.Close()
	// A profile base_url ends in /v1; the key route lives at the proxy root.
	s, err := NewLiteLLM(srv.URL+"/v1/", "sk-x", srv.Client()).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/key/info" || auth != "Bearer sk-x" {
		t.Fatalf("request: %s %s", path, auth)
	}
	if *s.Spend != 0.0081 || *s.Limit != 0.01 || *s.SoftLimit != 0.008 || s.Window != "30d" || s.Scope != "key" || s.Source != SourceLiteLLM {
		t.Fatalf("snapshot = %+v", s)
	}
	if left, ok := s.RemainingUSD(); !ok || left < 0.0018 || left > 0.0020 {
		t.Fatalf("remaining = %v %v", left, ok)
	}
	if s.ResetAt == nil || !s.ResetAt.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("reset = %v", s.ResetAt)
	}
}

func TestLiteLLMFallsBackToTheBindingTeamBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/key/info":
			io.WriteString(w, `{"info":{"spend":0.5,"max_budget":null,"team_id":"t-1","user_id":"u"}}`)
		case "/team/info":
			if r.URL.Query().Get("team_id") != "t-1" {
				http.Error(w, "bad team", 400)
				return
			}
			io.WriteString(w, `{"team_info":{"spend":31.2,"max_budget":50,"budget_duration":"30d","budget_reset_at":"2026-12-01T00:00:00"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, err := NewLiteLLM(srv.URL, "k", srv.Client()).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Scope != "team" || *s.Spend != 31.2 || *s.Limit != 50 || s.ResetAt == nil {
		t.Fatalf("a capless key in a team is bound by the team budget: %+v", s)
	}
}

func TestLiteLLMKeepsKeyFiguresWhenTeamIsUnreadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/key/info" {
			io.WriteString(w, `{"info":{"spend":0.5,"max_budget":null,"team_id":"t-1"}}`)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	s, err := NewLiteLLM(srv.URL, "k", srv.Client()).Fetch(context.Background())
	if err != nil || s.Scope != "key" || s.Limit != nil || *s.Spend != 0.5 {
		t.Fatalf("%+v %v", s, err)
	}
	if _, ok := s.RemainingUSD(); ok {
		t.Fatal("no cap known: remaining must be unknown, not spend negated")
	}
}

func TestLiteLLMErrorsSurfaceStatusWithoutTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"Key not found in database"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := NewLiteLLM(srv.URL, "sk-secret", srv.Client()).Fetch(context.Background())
	var be *Error
	if !errors.As(err, &be) || be.Status != 404 {
		t.Fatalf("err = %v", err)
	}
	if got := err.Error(); strings.Contains(got, "sk-secret") {
		t.Fatalf("error leaks the key: %s", got)
	}
}

func TestMergeAddsOnlyWhatTheLedgerSpentAfterTheSnapshot(t *testing.T) {
	s := Snapshot{Spend: new(0.0081), Limit: new(0.01)}
	m := Merge(s, 0.004)
	if m.Spend == nil || *m.Spend < 0.0120 || *m.Spend > 0.0122 {
		t.Fatalf("spend = %v", m.Spend)
	}
	if m.Remaining == nil || *m.Remaining > -0.0020 || *m.Remaining < -0.0022 {
		t.Fatalf("remaining = %v: negative means over the cap", m.Remaining)
	}
	if m.LocalDelta != 0.004 {
		t.Fatalf("delta = %v", m.LocalDelta)
	}
	// A provider that reports no spend stays unreported.
	if m := Merge(Snapshot{Balance: new(12.0)}, 1); m.Spend != nil || m.Remaining != nil {
		t.Fatalf("%+v", m)
	}
	// An explicit remaining (openrouter) takes precedence over limit - spend.
	if m := Merge(Snapshot{Spend: new(1.0), Limit: new(10.0), Remaining: new(7.0)}, 1); *m.Remaining != 6 {
		t.Fatalf("%+v", m)
	}
}

func TestOMPSourceIsLedgerAgainstTheSoftWeeklyCap(t *testing.T) {
	monday := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	var from, to time.Time
	src := OMP{
		SoftWeeklyUSD: 10,
		WeekStart:     func(time.Time) time.Time { return monday },
		WeekSpend:     func(f, t time.Time) (float64, error) { from, to = f, t; return 3.25, nil },
		Now:           func() time.Time { return now },
	}
	s, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !from.Equal(monday) || !to.After(now) {
		t.Fatalf("window = %v..%v", from, to)
	}
	if *s.Spend != 3.25 || *s.SoftLimit != 10 || s.Limit != nil || s.Remaining != nil {
		t.Fatalf("a subscription has no hard cap: %+v", s)
	}
	if s.ResetAt == nil || !s.ResetAt.Equal(monday.AddDate(0, 0, 7)) || s.Window != "week" || s.Scope != "local" {
		t.Fatalf("%+v", s)
	}
	if src.Interval() != 0 {
		t.Fatal("local source needs no polling interval")
	}
	src.WeekSpend = func(_, _ time.Time) (float64, error) { return 0, errors.New("ledger down") }
	if _, err := src.Fetch(context.Background()); err == nil {
		t.Fatal("ledger errors must surface")
	}
}

func TestCacheRefreshHonoursIntervalAndServesStaleOnError(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := NewCache(st)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	src := &countingSource{snap: Snapshot{Source: "x", Spend: new(1.0)}}

	if s, err := c.Refresh(context.Background(), "p", src, false); err != nil || *s.Spend != 1 || src.n != 1 {
		t.Fatalf("first: %+v %v n=%d", s, err, src.n)
	}
	now = now.Add(30 * time.Second)
	src.snap.Spend = new(2.0)
	if s, _ := c.Refresh(context.Background(), "p", src, false); *s.Spend != 1 || src.n != 1 {
		t.Fatalf("within the interval the cached snapshot is served: %+v n=%d", s, src.n)
	}
	if s, _ := c.Refresh(context.Background(), "p", src, true); *s.Spend != 2 || src.n != 2 {
		t.Fatalf("force re-reads: %+v n=%d", s, src.n)
	}
	now = now.Add(2 * time.Minute)
	src.err = errors.New("gateway down")
	s, err := c.Refresh(context.Background(), "p", src, false)
	if err == nil || *s.Spend != 2 {
		t.Fatalf("a failed fetch returns the stale snapshot with the error: %+v %v", s, err)
	}
	if _, _, ok, _ := c.Get("never"); ok {
		t.Fatal("unknown profile")
	}
}

type countingSource struct {
	snap Snapshot
	err  error
	n    int
}

func (c *countingSource) Name() string            { return "x" }
func (c *countingSource) Interval() time.Duration { return time.Minute }
func (c *countingSource) Fetch(context.Context) (Snapshot, error) {
	c.n++
	return c.snap, c.err
}

func TestFactoryPicksSourcesAndDemandsCredentials(t *testing.T) {
	ctx := context.Background()
	local := Local{
		Spend:     func(_, _ time.Time) (float64, error) { return 0, nil },
		WeekStart: func(t time.Time) time.Time { return t },
	}
	t.Setenv("ADE_TEST_KEY", "k")
	cases := []struct {
		name string
		p    config.AIProfile
		want string
		err  bool
	}{
		{"omp default", config.AIProfile{Driver: "omp"}, "omp", false},
		{"none", config.AIProfile{Driver: "anthropic", Billing: config.AIBilling{Source: "none"}}, "", false},
		{"api without source", config.AIProfile{Driver: "anthropic"}, "", false},
		{"litellm", config.AIProfile{Driver: "openai-compat", BaseURL: "http://x/v1", KeyEnv: "ADE_TEST_KEY", Billing: config.AIBilling{Source: "litellm"}}, "litellm", false},
		{"litellm without key", config.AIProfile{Driver: "openai-compat", BaseURL: "http://x/v1", KeyEnv: "ADE_TEST_UNSET", Billing: config.AIBilling{Source: "litellm"}}, "", true},
		{"moonshot", config.AIProfile{KeyEnv: "ADE_TEST_KEY", Billing: config.AIBilling{Source: "moonshot"}}, "moonshot", false},
		{"openrouter", config.AIProfile{KeyEnv: "ADE_TEST_KEY", Billing: config.AIBilling{Source: "openrouter"}}, "openrouter", false},
		{"anthropic needs an admin key", config.AIProfile{KeyEnv: "ADE_TEST_KEY", Billing: config.AIBilling{Source: "anthropic"}}, "", true},
		{"anthropic with admin key", config.AIProfile{Billing: config.AIBilling{Source: "anthropic", AdminKeyEnv: "ADE_TEST_KEY"}}, "anthropic", false},
		{"openai with admin key", config.AIProfile{Billing: config.AIBilling{Source: "openai", AdminKeyEnv: "ADE_TEST_KEY"}}, "openai", false},
		{"unknown", config.AIProfile{Billing: config.AIBilling{Source: "bogus"}}, "", true},
	}
	for _, c := range cases {
		src, err := New(ctx, c.p, local)
		switch {
		case c.err && err == nil:
			t.Errorf("%s: want an error", c.name)
		case !c.err && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want == "" && src != nil:
			t.Errorf("%s: want no source, got %s", c.name, src.Name())
		case c.want != "" && (src == nil || src.Name() != c.want):
			t.Errorf("%s: source = %v, want %s", c.name, src, c.want)
		}
	}
}
