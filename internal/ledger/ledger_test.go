package ledger

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/store"
)

func setup(t *testing.T) (*Ledger, *audit.Log, string) {
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
	return New(st, log), log, dir
}

func ai(action, profile, model string, cost float64, at time.Time, item string) audit.Entry {
	return audit.Entry{
		TS: at, Action: action, Item: item,
		AI: &audit.AIInfo{Profile: profile, Model: model, CostUSD: cost, CostSource: "reported", TokensIn: 100, TokensOut: 10},
	}
}

func TestLedgerIsADerivedViewOfAITrafficOnly(t *testing.T) {
	l, log, _ := setup(t)
	now := time.Now().UTC()
	log.Append(ai("ai.summarize", "p", "m1", 0.01, now.Add(-time.Hour), "g/a!1"))
	log.Append(ai("ai.review", "p", "m2", 0.05, now.Add(-time.Minute), "g/a!1"))
	log.Append(audit.Entry{TS: now, Action: "forge.thread.reply", Item: "g/a!1"})
	log.Append(audit.Entry{TS: now, Action: "cli.sync"})
	hit := ai("ai.summarize", "p", "m1", 0, now, "g/a!1")
	hit.AI.CacheHit = true
	log.Append(hit)
	log.Append(audit.Entry{
		TS: now, Action: StopAction, Item: "g/a!2", Outcome: "error", Error: "Budget has been exceeded",
		Params: map[string]any{"feature": "review", "profile": "p", "source": "gateway"},
	})

	es, err := l.Entries(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 3 {
		t.Fatalf("calls = %d, want 3 (two paid, one hit); non-ai audit lines must not appear: %+v", len(es), es)
	}
	if got := Total(es); got < 0.0599 || got > 0.0601 {
		t.Fatalf("total = %v", got)
	}
	withStops, _ := l.Entries(Query{Stops: true})
	if len(withStops) != 4 || !withStops[3].Stop() || withStops[3].Feature != "review" || withStops[3].Profile != "p" {
		t.Fatalf("stops: %+v", withStops)
	}
	spend, _ := l.Spend("p", now.Add(-2*time.Hour), time.Time{})
	if spend < 0.0599 || spend > 0.0601 {
		t.Fatalf("Spend = %v", spend)
	}
	if other, _ := l.Spend("other", time.Time{}, time.Time{}); other != 0 {
		t.Fatalf("another profile's spend = %v", other)
	}
}

func TestSyncIsIncrementalAndIdempotent(t *testing.T) {
	l, log, _ := setup(t)
	now := time.Now().UTC()
	log.Append(ai("ai.summarize", "p", "m", 0.01, now, ""))
	if n, err := l.Sync(); err != nil || n != 1 {
		t.Fatalf("first sync = %d, %v", n, err)
	}
	if n, _ := l.Sync(); n != 0 {
		t.Fatalf("re-sync indexed %d again", n)
	}
	log.Append(ai("ai.summarize", "p", "m", 0.02, now.Add(time.Second), ""))
	if n, _ := l.Sync(); n != 1 {
		t.Fatalf("incremental sync = %d, want just the new entry", n)
	}
	if got, _ := l.Spend("", time.Time{}, time.Time{}); got < 0.0299 || got > 0.0301 {
		t.Fatalf("spend = %v: double counting after overlapping re-scan?", got)
	}
}

func TestRebuildAfterCacheLossRestoresTheSameSpend(t *testing.T) {
	l, log, dir := setup(t)
	now := time.Now().UTC()
	log.Append(ai("ai.summarize", "p", "m", 0.01, now.Add(-48*time.Hour), ""))
	log.Append(ai("ai.review", "p", "m", 0.02, now, ""))
	before, _ := l.Spend("", time.Time{}, time.Time{})

	// A new cache.db over the same audit log: empty index until rebuilt.
	st, err := store.Open(filepath.Join(dir, "fresh", "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	l2 := New(st, log)
	n, err := l2.Rebuild()
	if err != nil || n != 2 {
		t.Fatalf("rebuild = %d, %v", n, err)
	}
	if after, _ := l2.Spend("", time.Time{}, time.Time{}); after != before {
		t.Fatalf("spend after rebuild %v != before %v", after, before)
	}
	// Rebuild on a populated index replaces rather than duplicates.
	if n, _ := l2.Rebuild(); n != 2 {
		t.Fatalf("second rebuild = %d", n)
	}
	if after, _ := l2.Spend("", time.Time{}, time.Time{}); after != before {
		t.Fatalf("rebuild duplicated rows: %v", after)
	}
}

func TestPeriodsUseLocalDaysAndMondayWeeks(t *testing.T) {
	l, log, _ := setup(t)
	loc := time.FixedZone("X", 2*3600)
	// Wednesday 2026-11-11 01:00 local = Tuesday 23:00 UTC.
	now := time.Date(2026, 11, 11, 1, 0, 0, 0, loc)
	log.Append(ai("ai.summarize", "p", "m", 1, time.Date(2026, 11, 10, 21, 30, 0, 0, time.UTC), "")) // 23:30 local Tue: last week? no, this week, not today
	log.Append(ai("ai.summarize", "p", "m", 2, time.Date(2026, 11, 10, 22, 30, 0, 0, time.UTC), "")) // 00:30 local Wed: today
	log.Append(ai("ai.summarize", "p", "m", 4, time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC), ""))   // Sunday: previous week
	got, err := l.Periods(now, loc, func() (time.Time, bool) { return time.Date(2026, 11, 2, 0, 0, 0, 0, loc), true })
	if err != nil {
		t.Fatal(err)
	}
	if got.Day != 2 || got.Week != 3 || got.Sprint != 7 || !got.HaveSprint {
		t.Fatalf("periods = %+v, want day 2, week 3, sprint 7", got)
	}
	if ws := WeekStart(now, loc); ws.Weekday() != time.Monday || ws.Day() != 9 {
		t.Fatalf("week start = %v", ws)
	}
	if got, _ := l.Periods(now, loc, nil); got.HaveSprint || got.Sprint != 0 {
		t.Fatalf("no sprint known: %+v", got)
	}
}

func TestGroupingAndTopItems(t *testing.T) {
	es := []Entry{
		{TS: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC), Feature: "summarize", Model: "a", CostUSD: 0.1, Item: "x!1", TokensIn: 10},
		{TS: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), Feature: "summarize", Model: "a", CacheHit: true, Item: "x!1"},
		{TS: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC), Feature: "review", Model: "b", CostUSD: 0.5, Item: "x!2"},
		{TS: time.Date(2026, 11, 11, 9, 5, 0, 0, time.UTC), Feature: "classify", Model: "b", CostUSD: 0.01},
	}
	days := Group(es, ByDay(time.UTC))
	if len(days) != 2 || days[0].Key != "2026-11-10" || days[0].Calls != 2 || days[0].CacheHits != 1 || days[1].CostUSD != 0.51 {
		t.Fatalf("by day: %+v", days)
	}
	top := TopItems(es, 5)
	if len(top) != 2 || top[0].Key != "x!2" || top[1].Key != "x!1" {
		t.Fatalf("top items (no-item entries dropped, costliest first): %+v", top)
	}
	if got := TopItems(es, 1); len(got) != 1 {
		t.Fatalf("limit: %+v", got)
	}
}

func TestReviewRatesAttributeDecisionsToTheProfileThatWroteTheDraft(t *testing.T) {
	_, log, _ := setup(t)
	now := time.Now()
	draft := func(id, profile string) {
		log.Append(audit.Entry{TS: now, Action: DraftAction, Item: "g/a!1", Target: id, Params: map[string]any{"profile": profile}})
	}
	decide := func(action, id string) {
		log.Append(audit.Entry{TS: now.Add(time.Second), Action: action, Item: "g/a!1", Target: id})
	}
	draft("d1", "noisy")
	draft("d2", "noisy")
	draft("d3", "noisy")
	draft("d4", "good")
	draft("d5", "good")
	decide(DecisionAccept, "d1")
	decide(DecisionDeny, "d2")
	decide(DecisionDeny, "d3")
	decide(DecisionDelete, "d3") // later decision wins; delete counts as denied
	decide(DecisionEdit, "d4")
	decide(DecisionAccept, "unrelated-own-draft") // a draft no agent wrote: ignored
	rates, err := ReviewRates(log, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Rates{
		"good":  {Profile: "good", Drafts: 2, Edited: 1, Pending: 1},
		"noisy": {Profile: "noisy", Drafts: 3, Accepted: 1, Denied: 2},
	}
	if len(rates) != 2 {
		t.Fatalf("rates = %+v", rates)
	}
	for _, r := range rates {
		if r != want[r.Profile] {
			t.Errorf("%s: %+v, want %+v", r.Profile, r, want[r.Profile])
		}
	}
	if rates[0].Decided() != 1 {
		t.Errorf("Decided = %d", rates[0].Decided())
	}
}
