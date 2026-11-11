package budget

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/billing"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/ledger"
	"github.com/coredev-uk/ade/internal/store"
)

type stubSource struct {
	name string
	snap billing.Snapshot
	err  error
	n    *int
}

func (s stubSource) Name() string            { return s.name }
func (s stubSource) Interval() time.Duration { return time.Minute }
func (s stubSource) Fetch(context.Context) (billing.Snapshot, error) {
	*s.n++
	return s.snap, s.err
}

func fixture(t *testing.T) (Deps, *audit.Log, *int) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "data"), "user")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.AI.Mode, cfg.AI.Profile = "auto", "gw"
	cfg.Budget = config.Budget{DailyUSD: 1, WeeklyUSD: 4}
	cfg.AI.Profiles["gw"] = config.AIProfile{Driver: "openai-compat", Models: map[string]string{"explain": "echo", "draft": "echo-2"}, Billing: config.AIBilling{Source: "litellm"}}
	cfg.AI.Profiles["sub"] = config.AIProfile{Driver: "omp", Models: map[string]string{"explain": "haiku"}}
	n := new(int)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	d := Deps{
		Cfg: &cfg, Ledger: ledger.New(st, log), Audit: log, Billing: billing.NewCache(st),
		Now: func() time.Time { return now }, Loc: time.UTC,
		Source: func(_ context.Context, name string, _ config.AIProfile) (billing.Source, error) {
			if name != "gw" {
				return nil, nil
			}
			return stubSource{name: "litellm", n: n, snap: billing.Snapshot{
				Source: "litellm", Scope: "key", AsOf: now, Spend: new(0.0081), Limit: new(0.01),
			}}, nil
		},
	}
	return d, log, n
}

func call(at time.Time, feature, profile, model string, cost float64, item string, hit bool) audit.Entry {
	return audit.Entry{
		TS: at, Action: "ai." + feature, Item: item,
		AI: &audit.AIInfo{Profile: profile, Model: model, CostUSD: cost, CostSource: "reported", CacheHit: hit},
	}
}

func TestReportSumsCapsProfilesAndBuckets(t *testing.T) {
	d, log, _ := fixture(t)
	now := d.Now()
	log.Append(call(now.Add(-time.Hour), "summarize", "gw", "echo", 0.3, "g/a!1", false))
	log.Append(call(now.Add(-30*time.Minute), "summarize", "gw", "echo", 0, "g/a!1", true))
	log.Append(call(now.Add(-26*time.Hour), "review", "sub", "haiku", 0.5, "g/a!2", false)) // yesterday, same week
	log.Append(call(now.Add(-10*24*time.Hour), "classify", "sub", "haiku", 0.2, "", false)) // last week
	log.Append(audit.Entry{
		TS: now, Action: ledger.StopAction, Item: "g/a!3", Outcome: "error", Error: "Budget has been exceeded",
		Params: map[string]any{"feature": "review", "profile": "gw", "source": "gateway"},
	})

	r, err := Build(context.Background(), d, Options{})
	if err != nil {
		t.Fatal(err)
	}
	day, week := r.Caps[0], r.Caps[1]
	if day.Period != "day" || day.SpentUSD != 0.3 || day.LimitUSD != 1 || day.Remaining != 0.7 {
		t.Fatalf("day cap = %+v", day)
	}
	if week.SpentUSD != 0.8 || week.Remaining != 3.2 {
		t.Fatalf("week cap = %+v", week)
	}
	if sp := r.Caps[2]; sp.Known || sp.LimitUSD != 0 {
		t.Fatalf("sprint cap with no sprint known: %+v", sp)
	}
	if len(r.ByDay) != 7 || r.ByDay[6].Key != "2026-11-11" || r.ByDay[6].CostUSD != 0.3 || r.ByDay[5].CostUSD != 0.5 || r.ByDay[0].Calls != 0 {
		t.Fatalf("by day (zero days present, today last): %+v", r.ByDay)
	}
	if r.Calls != 4 || r.CacheHits != 1 {
		t.Fatalf("calls %d hits %d", r.Calls, r.CacheHits)
	}
	if len(r.ByFeature) != 3 || len(r.ByModel) != 2 {
		t.Fatalf("features %+v models %+v", r.ByFeature, r.ByModel)
	}
	if len(r.TopItems) == 0 || r.TopItems[0].Key != "g/a!2" {
		t.Fatalf("top items: %+v", r.TopItems)
	}
	if len(r.Stops) != 1 || r.Stops[0].Profile != "gw" || r.Stops[0].Feature != "review" {
		t.Fatalf("stops: %+v", r.Stops)
	}
	// Profiles: sorted, the active one marked, review resolved through draft.
	if len(r.Profiles) != 2 || r.Profiles[0].Name != "gw" || !r.Profiles[0].Active || r.Profiles[1].Active {
		t.Fatalf("profiles: %+v", r.Profiles)
	}
	if m := r.Profiles[0].Models; m["explain"] != "echo" || m["review"] != "echo-2" || m["plan"] != "echo" {
		t.Fatalf("models: %+v", m)
	}
	if r.Profiles[1].WeekUSD != 0.5 {
		t.Fatalf("sub week spend = %v", r.Profiles[1].WeekUSD)
	}
}

func TestProviderFigureIsSnapshotPlusLedgerSinceIt(t *testing.T) {
	d, log, _ := fixture(t)
	now := d.Now()
	snapAt := now.Add(-time.Minute)
	d.Source = func(_ context.Context, name string, _ config.AIProfile) (billing.Source, error) {
		if name != "gw" {
			return nil, nil
		}
		return stubSource{name: "litellm", n: new(int), snap: billing.Snapshot{Source: "litellm", Scope: "key", AsOf: snapAt, Spend: new(0.0081), Limit: new(0.01)}}, nil
	}
	log.Append(call(snapAt.Add(-time.Hour), "summarize", "gw", "echo", 0.0081, "", false)) // already inside the snapshot
	log.Append(call(now.Add(-10*time.Second), "summarize", "gw", "echo", 0.002, "", false))
	r, err := Build(context.Background(), d, Options{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	p := r.Profiles[0].Provider
	if p == nil || p.LocalDelta != 0.002 {
		t.Fatalf("only spend after the snapshot is added: %+v", p)
	}
	if *p.Spend < 0.0100 || *p.Spend > 0.0102 || *p.Remaining > 0 || *p.Remaining < -0.0003 {
		t.Fatalf("merged spend %v remaining %v", *p.Spend, *p.Remaining)
	}
	if r.Profiles[1].Provider != nil {
		t.Fatal("a profile without a source has no provider figure")
	}
}

func TestOfflineNeverCallsTheProviderAndStaleSnapshotIsLabelled(t *testing.T) {
	d, _, n := fixture(t)
	ctx := context.Background()
	r, _ := Build(ctx, d, Options{Offline: true})
	if *n != 0 || r.Profiles[0].Provider != nil || r.Profiles[0].ProviderErr != "not read yet" {
		t.Fatalf("offline with nothing cached: n=%d %+v", *n, r.Profiles[0])
	}
	if _, err := Build(ctx, d, Options{Refresh: true}); err != nil || *n != 1 {
		t.Fatalf("refresh: n=%d %v", *n, err)
	}
	r, _ = Build(ctx, d, Options{Offline: true})
	if *n != 1 || r.Profiles[0].Provider == nil || r.Profiles[0].Stale {
		t.Fatalf("offline serves the cached snapshot: n=%d %+v", *n, r.Profiles[0])
	}
	// The provider goes away: the old snapshot stays, marked stale, with the reason.
	d.Source = func(context.Context, string, config.AIProfile) (billing.Source, error) {
		return stubSource{name: "litellm", n: n, err: errors.New("gateway unreachable")}, nil
	}
	r, _ = Build(ctx, d, Options{Refresh: true})
	pv := r.Profiles[0]
	if pv.Provider == nil || !pv.Stale || pv.ProviderErr != "gateway unreachable" {
		t.Fatalf("stale handling: %+v", pv)
	}
}

func TestSourceConfigErrorsAreReportedPerProfile(t *testing.T) {
	d, _, _ := fixture(t)
	d.Source = func(context.Context, string, config.AIProfile) (billing.Source, error) {
		return nil, errors.New("no key_cmd or key_env configured")
	}
	r, err := Build(context.Background(), d, Options{})
	if err != nil || r.Profiles[0].ProviderErr == "" {
		t.Fatalf("%v %+v", err, r.Profiles[0])
	}
}
