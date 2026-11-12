package ai

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/ledger"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/store"
)

// fakeDriver answers every call with a fixed response and counts them.
type fakeDriver struct {
	calls  int
	resp   Response
	err    error
	prices map[string]config.Price
	last   Call
}

func (f *fakeDriver) Name() string { return "fake" }
func (f *fakeDriver) Complete(_ context.Context, c Call) (Response, error) {
	f.calls++
	f.last = c
	return f.resp, f.err
}

func (f *fakeDriver) Prices(context.Context) (map[string]config.Price, error) { return f.prices, nil }
func (f *fakeDriver) Models(context.Context) ([]string, error)                { return nil, nil }

type env struct {
	t    *testing.T
	dir  string
	st   *store.Store
	log  *audit.Log
	svc  *Service
	drv  *fakeDriver
	bus  *events.Bus
	conf *config.Config
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{t: t, dir: dir, drv: &fakeDriver{resp: Response{
		Text: "- adds a rate limiter\nRisk: low", Model: "haiku-test", Usage: Usage{In: 1000, Out: 100},
		CostUSD: 0.002, CostSource: CostReported,
	}}, bus: events.NewBus()}
	cfg := config.Defaults()
	cfg.AI.Mode = "auto"
	cfg.AI.Profile = "p"
	cfg.AI.Profiles["p"] = config.AIProfile{
		Driver: "omp", Models: map[string]string{"explain": "haiku-test", "review": "haiku-test"},
		Prices: map[string]config.Price{"haiku-test": {Input: 1, Output: 5}},
	}
	e.conf = &cfg
	e.open()
	return e
}

// open (re)opens cache.db and the service, as a fresh process would.
func (e *env) open() {
	e.t.Helper()
	if e.st != nil {
		e.st.Close()
	}
	st, err := store.Open(filepath.Join(e.dir, "cache", "cache.db"))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { st.Close() })
	e.st = st
	log, err := audit.Open(filepath.Join(e.dir, "data"), "user")
	if err != nil {
		e.t.Fatal(err)
	}
	e.log = log
	rec := records.New(filepath.Join(e.dir, "data"))
	e.svc = &Service{
		Cfg: e.conf, Audit: log, Records: rec, Ledger: ledger.New(st, log), Cache: NewCache(st, rec),
		DB: st.DB, Bus: e.bus, Now: time.Now, Loc: time.UTC,
		Open: func(context.Context, config.AIProfile) (Driver, error) { return e.drv, nil },
	}
}

func summarizeJob(head string) Job {
	mr := forge.MR{Ref: forge.Ref{Project: "grp/api", IID: 1, Kind: forge.KindMR}, Title: "Add limiter", SourceBranch: "f", TargetBranch: "main"}
	d := forge.Diff{HeadSHA: head, Files: []forge.DiffFile{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1,1 +1,2 @@\n x\n+y\n"}}}
	return SummarizeJob("sandbox", mr, d, nil)
}

func (e *env) entries(action string) []audit.Entry {
	e.t.Helper()
	es, err := e.log.Query(audit.Filter{Action: action})
	if err != nil {
		e.t.Fatal(err)
	}
	return es
}

func TestSecondRunOfUnchangedInputIsFreeCacheHit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first, err := e.svc.Run(ctx, summarizeJob("abc123"))
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheHit || first.CostUSD != 0.002 {
		t.Fatalf("first run: %+v", first)
	}
	second, err := e.svc.Run(ctx, summarizeJob("abc123"))
	if err != nil {
		t.Fatal(err)
	}
	if !second.CacheHit || second.CostUSD != 0 || second.Text != first.Text {
		t.Fatalf("second run should be a $0 hit with the same text: %+v", second)
	}
	if e.drv.calls != 1 {
		t.Fatalf("driver called %d times, want 1", e.drv.calls)
	}
	es := e.entries("ai.summarize")
	if len(es) != 2 || es[1].AI == nil || !es[1].AI.CacheHit || es[1].AI.CostUSD != 0 || es[0].AI.CacheHit {
		t.Fatalf("audit entries do not show a paid call then a $0 hit: %+v", es)
	}
	if got, _ := e.svc.Ledger.Spend("", time.Time{}, time.Time{}); got != 0.002 {
		t.Fatalf("ledger spend = %v, want 0.002 (a hit adds nothing)", got)
	}
}

func TestChangedHeadSHAIsACacheMiss(t *testing.T) {
	e := newEnv(t)
	for _, head := range []string{"aaa", "bbb"} {
		if _, err := e.svc.Run(context.Background(), summarizeJob(head)); err != nil {
			t.Fatal(err)
		}
	}
	if e.drv.calls != 2 {
		t.Fatalf("a new head SHA must be re-summarised; driver calls = %d", e.drv.calls)
	}
}

func TestCacheSurvivesCacheDBLossThroughReindex(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.Run(ctx, summarizeJob("abc")); err != nil {
		t.Fatal(err)
	}
	// Lose cache.db entirely.
	e.st.Close()
	for _, f := range []string{"cache.db", "cache.db-wal", "cache.db-shm"} {
		os.Remove(filepath.Join(e.dir, "cache", f))
	}
	e.open()
	n, err := e.svc.Cache.Reindex()
	if err != nil || n != 1 {
		t.Fatalf("reindex = %d, %v; want 1 answer found", n, err)
	}
	res, err := e.svc.Run(ctx, summarizeJob("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.CacheHit || e.drv.calls != 1 {
		t.Fatalf("after reindex the answer must come from the artifact: hit=%v driver calls=%d", res.CacheHit, e.drv.calls)
	}
	// And without a reindex the lost cache really does cost a second purchase.
	e.st.Close()
	for _, f := range []string{"cache.db", "cache.db-wal", "cache.db-shm"} {
		os.Remove(filepath.Join(e.dir, "cache", f))
	}
	e.open()
	if _, err := e.svc.Run(ctx, summarizeJob("abc")); err != nil {
		t.Fatal(err)
	}
	if e.drv.calls != 2 {
		t.Fatalf("without reindex the call is bought again; driver calls = %d", e.drv.calls)
	}
}

func TestArtifactCarriesFrontmatterAndLivesInTheItemRecord(t *testing.T) {
	e := newEnv(t)
	res, err := e.svc.Run(context.Background(), summarizeJob("abc"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("items", "sandbox", "grp", "api", "mr-1", "ai")
	if !strings.HasPrefix(filepath.FromSlash(res.Artifact), want) {
		t.Fatalf("artifact %q is not under %s", res.Artifact, want)
	}
	raw, err := os.ReadFile(filepath.Join(e.dir, "data", filepath.FromSlash(res.Artifact)))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"feature: summarize", "profile: p", "model: haiku-test", "prompt_version: summarize-v2", "input_hash: ", "audit_id: " + res.AuditID, "cost_usd: 0.002"} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("artifact frontmatter lacks %q:\n%s", key, raw)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(string(raw)), "Risk: low") {
		t.Errorf("artifact body missing:\n%s", raw)
	}
}

func TestDailyCapRefusesBeforeSpendingAndIsAudited(t *testing.T) {
	e := newEnv(t)
	e.conf.Budget.DailyUSD = 0.01
	// $0.0099 already spent today.
	e.log.Append(audit.Entry{Action: "ai.summarize", Item: "x", AI: &audit.AIInfo{Profile: "p", Model: "m", CostUSD: 0.0099, CostSource: CostReported}})
	sub, cancel := e.bus.Subscribe(8)
	defer cancel()

	_, err := e.svc.Run(context.Background(), summarizeJob("abc"))
	stop, ok := AsStop(err)
	if !ok || stop.Source != "local" || !strings.Contains(stop.Message, "daily cap reached") {
		t.Fatalf("want a local daily-cap stop, got %v", err)
	}
	if e.drv.calls != 0 {
		t.Fatal("the driver must not be called when a cap refuses")
	}
	stops := e.entries(ledger.StopAction)
	if len(stops) != 1 || stops[0].Params["source"] != "local" || stops[0].Params["feature"] != "summarize" {
		t.Fatalf("budget.stop audit entry: %+v", stops)
	}
	select {
	case ev := <-sub:
		if ev.Kind != "budget.stop" || ev.Severity != events.Error {
			t.Fatalf("event %+v", ev)
		}
	default:
		t.Fatal("no budget.stop event published")
	}

	// --override-budget goes through, and says so in the audit log.
	j := summarizeJob("abc")
	j.Override = true
	if _, err := e.svc.Run(context.Background(), j); err != nil {
		t.Fatalf("override should pass: %v", err)
	}
	if n := len(e.entries("budget.override")); n != 1 || e.drv.calls != 1 {
		t.Fatalf("override audit entries = %d, driver calls = %d", n, e.drv.calls)
	}
}

func TestCallCapReachesTheDriverAsMaxCostAndRefusesBigEstimates(t *testing.T) {
	e := newEnv(t)
	e.conf.Budget.CallUSD = 0.5
	if _, err := e.svc.Run(context.Background(), summarizeJob("abc")); err != nil {
		t.Fatal(err)
	}
	if e.drv.last.MaxCostUSD != 0.5 {
		t.Fatalf("MaxCostUSD = %v: the per-call cap must reach the driver (omp --max-cost)", e.drv.last.MaxCostUSD)
	}
	e.conf.Budget.CallUSD = 0.0000001
	_, err := e.svc.Run(context.Background(), summarizeJob("different"))
	if stop, ok := AsStop(err); !ok || !strings.Contains(stop.Message, "budget.call_usd") {
		t.Fatalf("an estimate over the call cap must be refused: %v", err)
	}
}

// A stop on the retry follows a first call that was paid for: the ledger
// counts cost from ai.* entries, so that call is booked next to the stop.
func TestBudgetStopAfterAPaidCallLedgersTheCost(t *testing.T) {
	e := newEnv(t)
	e.drv.resp = Response{CostUSD: 0.25, Usage: Usage{In: 100, Out: 50}}
	e.drv.err = &StopError{Source: "gateway", Message: "Budget has been exceeded!"}
	if _, err := e.svc.Run(context.Background(), summarizeJob("abc")); err == nil {
		t.Fatal("want the stop")
	}
	if n := len(e.entries(ledger.StopAction)); n != 1 {
		t.Errorf("%d budget.stop entries, want 1", n)
	}
	es := e.entries("ai.summarize")
	if len(es) != 1 || es[0].AI == nil || es[0].AI.CostUSD != 0.25 || es[0].AI.TokensOut != 50 {
		t.Fatalf("the paid first call is not booked: %+v", es)
	}
}

func TestProviderBudgetStopIsClassifiedAndAudited(t *testing.T) {
	e := newEnv(t)
	e.drv.resp = Response{} // nothing was spent before the stop
	e.drv.err = &StopError{Source: "gateway", Message: "Budget has been exceeded! Key=k Current cost: 0.01, Max budget: 0.01"}
	_, err := e.svc.Run(context.Background(), summarizeJob("abc"))
	if stop, ok := AsStop(err); !ok || stop.Source != "gateway" {
		t.Fatalf("err = %v", err)
	}
	stops := e.entries(ledger.StopAction)
	if len(stops) != 1 || stops[0].Params["source"] != "gateway" || !strings.Contains(stops[0].Error, "Budget has been exceeded") {
		t.Fatalf("budget.stop entries: %+v", stops)
	}
	if n := len(e.entries("ai.summarize")); n != 0 {
		t.Fatalf("a stopped call must not also leave an ai.summarize entry (got %d)", n)
	}
	if _, err := e.svc.Cache.Reindex(); err != nil {
		t.Fatal(err)
	}
}

func TestOtherDriverErrorsAreAuditedAsFailedCalls(t *testing.T) {
	e := newEnv(t)
	e.drv.err = errors.New("boom")
	if _, err := e.svc.Run(context.Background(), summarizeJob("abc")); err == nil {
		t.Fatal("want the driver error")
	}
	es := e.entries("ai.summarize")
	if len(es) != 1 || es[0].Outcome != "error" || es[0].Error != "boom" {
		t.Fatalf("entries: %+v", es)
	}
	// A failure leaves nothing in the cache: the next run retries.
	e.drv.err = nil
	if res, err := e.svc.Run(context.Background(), summarizeJob("abc")); err != nil || res.CacheHit {
		t.Fatalf("retry: %+v %v", res, err)
	}
}

func TestAskModeNeedsConfirmationAboveThreshold(t *testing.T) {
	e := newEnv(t)
	e.conf.AI.Mode = "ask"
	e.conf.Budget.ConfirmOverUSD = 0.0000001
	// Without a way to ask, the call fails and nothing is sent.
	_, err := e.svc.Run(context.Background(), summarizeJob("abc"))
	var nc *ErrNeedsConfirm
	if !errors.As(err, &nc) || e.drv.calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, e.drv.calls)
	}
	// Declined: cancelled, audited, nothing sent.
	j := summarizeJob("abc")
	j.Confirm = func(Preflight) (bool, error) { return false, nil }
	if _, err := e.svc.Run(context.Background(), j); !errors.Is(err, ErrCancelled) || e.drv.calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, e.drv.calls)
	}
	if es := e.entries("ai.summarize"); len(es) != 1 || es[0].Outcome != "cancelled" {
		t.Fatalf("entries: %+v", es)
	}
	// --yes goes through; auto mode never asks.
	j = summarizeJob("abc")
	j.Assume = true
	if _, err := e.svc.Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	e.conf.AI.Mode = "auto"
	j = summarizeJob("zzz")
	if _, err := e.svc.Run(context.Background(), j); err != nil {
		t.Fatalf("auto mode must not ask: %v", err)
	}
}

func TestModeOffRefusesWithoutSideEffects(t *testing.T) {
	e := newEnv(t)
	e.conf.AI.Mode = "off"
	if _, err := e.svc.Run(context.Background(), summarizeJob("abc")); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
	if e.drv.calls != 0 || len(e.entries("*")) != 0 {
		t.Fatal("off must not call the driver or write audit entries")
	}
}

func TestMissingModelForRoleIsAClearError(t *testing.T) {
	e := newEnv(t)
	p := e.conf.AI.Profiles["p"]
	p.Models = map[string]string{}
	e.conf.AI.Profiles["p"] = p
	_, err := e.svc.Run(context.Background(), summarizeJob("abc"))
	if err == nil || !strings.Contains(err.Error(), `no model configured for role "explain"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestPreviewSendsNothingAndReportsCached(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pf, cached, err := e.svc.Preview(ctx, summarizeJob("abc"))
	if err != nil || cached || !pf.Priced || pf.EstimateUSD <= 0 {
		t.Fatalf("preview = %+v cached=%v err=%v", pf, cached, err)
	}
	if e.drv.calls != 0 || len(e.entries("*")) != 0 {
		t.Fatal("preview must not call or audit")
	}
	if _, err := e.svc.Run(ctx, summarizeJob("abc")); err != nil {
		t.Fatal(err)
	}
	if _, cached, _ := e.svc.Preview(ctx, summarizeJob("abc")); !cached {
		t.Fatal("preview should see the cached answer")
	}
}

func TestUnpricedModelIsFlaggedNotZeroPriced(t *testing.T) {
	e := newEnv(t)
	p := e.conf.AI.Profiles["p"]
	p.Prices = nil
	e.conf.AI.Profiles["p"] = p
	pf, _, err := e.svc.Preview(context.Background(), summarizeJob("abc"))
	if err != nil || pf.Priced {
		t.Fatalf("pf = %+v err = %v: an unpriced model must say so", pf, err)
	}
}

func TestDriverPricesAreUsedAndCached(t *testing.T) {
	e := newEnv(t)
	p := e.conf.AI.Profiles["p"]
	p.Prices = nil
	e.conf.AI.Profiles["p"] = p
	e.drv.prices = map[string]config.Price{"anthropic/haiku-test": {Input: 1, Output: 5}}
	pf, _, err := e.svc.Preview(context.Background(), summarizeJob("abc"))
	if err != nil || !pf.Priced {
		t.Fatalf("a price published by the driver (selector form) must match the bare model: %+v %v", pf, err)
	}
	// A second process within the TTL reads the price from cache.db, not the driver.
	e.drv.prices = nil
	e.open()
	pf, _, err = e.svc.Preview(context.Background(), summarizeJob("abc"))
	if err != nil || !pf.Priced {
		t.Fatalf("cached driver prices: %+v %v", pf, err)
	}
}

func TestSprintCapOnlyAppliesWhenASprintIsKnown(t *testing.T) {
	e := newEnv(t)
	e.conf.Budget.SprintUSD = 0.0001
	e.log.Append(audit.Entry{Action: "ai.summarize", AI: &audit.AIInfo{Profile: "p", CostUSD: 0.5, CostSource: CostReported}})
	if _, err := e.svc.Run(context.Background(), summarizeJob("abc")); err != nil {
		t.Fatalf("no sprint known: the cap must not be guessed, got %v", err)
	}
	e.conf.Budget.DailyUSD, e.conf.Budget.WeeklyUSD = 0, 0
	e.svc.SprintStart = func() (time.Time, bool) { return time.Now().Add(-24 * time.Hour), true }
	_, err := e.svc.Run(context.Background(), summarizeJob("def"))
	if stop, ok := AsStop(err); !ok || !strings.Contains(stop.Message, "sprint cap reached") {
		t.Fatalf("err = %v", err)
	}
}

func TestHeadroomFromGatewaySnapshotRefusesAnOverBudgetCall(t *testing.T) {
	// covered through check(): the provider's own remaining budget refuses.
	pf := Preflight{EstimateUSD: 0.02, Headroom: new(0.01)}
	check(&pf, config.Budget{}, 0, true)
	if !strings.Contains(pf.Refuse, "provider's own budget has $0.01 left") {
		t.Fatalf("refuse = %q", pf.Refuse)
	}
}

func TestSoftWeeklyCapAsksButNeverRefuses(t *testing.T) {
	pf := Preflight{EstimateUSD: 0.5, Spent: Spent{Week: 9.8}}
	check(&pf, config.Budget{}, 10, true)
	if pf.Refuse != "" || len(pf.Confirm) != 1 || !strings.Contains(pf.Confirm[0], "soft cap") {
		t.Fatalf("%+v", pf)
	}
}

// A driver that asks again when the answer is cut short can spend the input
// twice and 1x + 2x the output cap: the estimate is that worst case.
func TestPreflightCoversTheRetryOfDriversThatRetry(t *testing.T) {
	e := newEnv(t)
	e.conf.AI.Profiles["h"] = config.AIProfile{
		Driver: "openai-compat", Models: map[string]string{"explain": "haiku-test"},
		Prices: map[string]config.Price{"haiku-test": {Input: 1, Output: 5}},
	}
	ctx := context.Background()
	omp, _, err := e.svc.Preview(ctx, summarizeJob("abc"))
	if err != nil {
		t.Fatal(err)
	}
	j := summarizeJob("abc")
	j.Profile = "h"
	retrying, _, err := e.svc.Preview(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	if omp.Retry || !retrying.Retry {
		t.Fatalf("Retry: omp %v, retrying %v", omp.Retry, retrying.Retry)
	}
	want := (2*float64(retrying.InTokens)*1 + 3*float64(retrying.OutTokens)*5) / 1e6
	if math.Abs(retrying.EstimateUSD-want) > 1e-12 || retrying.EstimateUSD <= 2*omp.EstimateUSD {
		t.Errorf("estimate %v, want %v (one call: %v)", retrying.EstimateUSD, want, omp.EstimateUSD)
	}
	if !strings.Contains(retrying.Describe(), fmt.Sprintf("≤%d out in all", 3*retrying.OutTokens)) || strings.Contains(omp.Describe(), "asks again") {
		t.Errorf("describe: %q / %q", retrying.Describe(), omp.Describe())
	}
}
