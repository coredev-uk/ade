// Package budget builds the figures the budget page and `ade budget` show:
// caps against ledger spend, the spend by day, feature and model, the active
// profiles with their provider-side snapshots, budget stops, and how
// agent-review drafts were triaged. It only reads; the ledger (derived from
// the audit log) and the billing snapshots are the sources.
package budget

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/billing"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/ledger"
)

// Deps is what Build reads.
type Deps struct {
	Cfg     *config.Config
	Ledger  *ledger.Ledger
	Audit   *audit.Log
	Billing *billing.Cache
	// Source builds a profile's billing source (nil: none configured).
	Source      func(ctx context.Context, name string, p config.AIProfile) (billing.Source, error)
	SprintStart func() (time.Time, bool)
	Now         func() time.Time
	Loc         *time.Location
}

// Options tunes a report.
type Options struct {
	// Refresh forces billing sources to be read now instead of when their
	// snapshot is older than the source's interval.
	Refresh bool
	// Days is how many days the by-day series covers (default 7).
	Days int
	// Window is the span for by-feature, by-model and top-items (default 30 days).
	Window time.Duration
	// Offline skips every provider call and uses cached snapshots only.
	Offline bool
}

// Cap is one budget period against its cap. Limit 0 means no cap.
type Cap struct {
	Period    string  `json:"period"` // day | week | sprint
	SpentUSD  float64 `json:"spent_usd"`
	LimitUSD  float64 `json:"limit_usd"`
	Remaining float64 `json:"remaining_usd"` // meaningful only with a limit
	Known     bool    `json:"known"`         // false: the sprint window is not known
}

// ProfileView is one configured profile with its provider-side figures.
type ProfileView struct {
	Name   string            `json:"name"`
	Active bool              `json:"active"`
	Driver string            `json:"driver"`
	Models map[string]string `json:"models"` // role -> model id, review resolved through its fallback
	Source string            `json:"billing_source,omitempty"`
	// Provider is the merged provider figure: nil when the profile has no
	// billing source or none could be read yet.
	Provider *billing.Merged `json:"provider,omitempty"`
	// ProviderErr is why the provider figure is missing or stale.
	ProviderErr string `json:"provider_error,omitempty"`
	Stale       bool   `json:"stale,omitempty"` // Provider is an older snapshot than asked for
	// WeekUSD is the profile's own ledger spend this week.
	WeekUSD float64 `json:"week_usd"`
}

// Report is everything the budget page shows.
type Report struct {
	Now      time.Time       `json:"now"`
	Mode     string          `json:"mode"`
	Profile  string          `json:"profile"`
	Caps     []Cap           `json:"caps"`
	Profiles []ProfileView   `json:"profiles"`
	ByDay    []ledger.Bucket `json:"by_day"` // oldest first, zero days included
	// ByFeature, ByModel and TopItems cover Options.Window.
	ByFeature []ledger.Bucket `json:"by_feature"`
	ByModel   []ledger.Bucket `json:"by_model"`
	TopItems  []ledger.Bucket `json:"top_items"`
	Stops     []ledger.Entry  `json:"stops"` // newest first
	Rates     []ledger.Rates  `json:"review_rates"`
	// CacheHits is how many calls in the window were answered from the
	// cache; SavedUSD is not tracked per hit, so only the count is shown.
	CacheHits int `json:"cache_hits"`
	Calls     int `json:"calls"`
}

// Build assembles the report.
func Build(ctx context.Context, d Deps, o Options) (Report, error) {
	if o.Days <= 0 {
		o.Days = 7
	}
	if o.Window <= 0 {
		o.Window = 30 * 24 * time.Hour
	}
	now := d.Now()
	r := Report{Now: now, Mode: d.Cfg.AI.Mode, Profile: d.Cfg.AI.Profile}

	per, err := d.Ledger.Periods(now, d.Loc, d.SprintStart)
	if err != nil {
		return r, err
	}
	b := d.Cfg.Budget
	r.Caps = []Cap{
		capOf("day", per.Day, b.DailyUSD, true),
		capOf("week", per.Week, b.WeeklyUSD, true),
		capOf("sprint", per.Sprint, b.SprintUSD, per.HaveSprint),
	}

	since := now.Add(-o.Window)
	es, err := d.Ledger.Entries(ledger.Query{From: since, Stops: true})
	if err != nil {
		return r, err
	}
	var calls []ledger.Entry
	for _, e := range es {
		if e.Stop() {
			r.Stops = append(r.Stops, e)
			continue
		}
		calls = append(calls, e)
		if e.CacheHit {
			r.CacheHits++
		}
	}
	r.Calls = len(calls)
	slices.Reverse(r.Stops)
	if len(r.Stops) > 10 {
		r.Stops = r.Stops[:10]
	}
	r.ByFeature = ledger.Group(calls, ledger.ByFeature)
	r.ByModel = ledger.Group(calls, ledger.ByModel)
	r.TopItems = ledger.TopItems(calls, 5)
	r.ByDay = byDay(calls, now, d.Loc, o.Days)

	if r.Rates, err = ledger.ReviewRates(d.Audit, since); err != nil {
		return r, err
	}
	r.Profiles = d.profiles(ctx, now, o)
	return r, nil
}

func capOf(period string, spent, limit float64, known bool) Cap {
	c := Cap{Period: period, SpentUSD: spent, LimitUSD: limit, Known: known}
	if limit > 0 {
		c.Remaining = limit - spent
	}
	return c
}

// byDay returns one bucket per local day for the last days days, today last,
// with empty days present so the series has no gaps.
func byDay(calls []ledger.Entry, now time.Time, loc *time.Location, days int) []ledger.Bucket {
	have := map[string]ledger.Bucket{}
	for _, b := range ledger.Group(calls, ledger.ByDay(loc)) {
		have[b.Key] = b
	}
	today := ledger.DayStart(now, loc)
	out := make([]ledger.Bucket, 0, days)
	for i := days - 1; i >= 0; i-- {
		key := today.AddDate(0, 0, -i).Format("2006-01-02")
		b, ok := have[key]
		if !ok {
			b = ledger.Bucket{Key: key}
		}
		out = append(out, b)
	}
	return out
}

func (d Deps) profiles(ctx context.Context, now time.Time, o Options) []ProfileView {
	var out []ProfileView
	for _, name := range slices.Sorted(maps.Keys(d.Cfg.AI.Profiles)) {
		p := d.Cfg.AI.Profiles[name]
		v := ProfileView{Name: name, Active: name == d.Cfg.AI.Profile, Driver: p.Driver, Models: map[string]string{}, Source: p.Billing.Source}
		for _, role := range config.AIRoles {
			if m, err := p.Model(role); err == nil {
				v.Models[role] = m
			}
		}
		if w, err := d.Ledger.Spend(name, ledger.WeekStart(now, d.Loc), time.Time{}); err == nil {
			v.WeekUSD = w
		}
		d.provider(ctx, name, p, &v, o)
		out = append(out, v)
	}
	return out
}

// provider fills v.Provider from the profile's billing source, falling back
// to the last snapshot (marked stale) when the source cannot be read.
func (d Deps) provider(ctx context.Context, name string, p config.AIProfile, v *ProfileView, o Options) {
	if d.Source == nil || d.Billing == nil {
		return
	}
	src, err := d.Source(ctx, name, p)
	if err != nil {
		v.ProviderErr = err.Error()
		return
	}
	if src == nil {
		return
	}
	v.Source = src.Name()
	var snap billing.Snapshot
	var ferr error
	// Offline never calls a provider; a local source (omp: the ledger itself)
	// has none to call and is always fresh.
	if o.Offline && src.Interval() > 0 {
		var ok bool
		if snap, _, ok, ferr = d.Billing.Get(name); !ok && ferr == nil {
			v.ProviderErr = "not read yet"
			return
		}
	} else {
		snap, ferr = d.Billing.Refresh(ctx, name, src, o.Refresh)
	}
	if ferr != nil {
		v.ProviderErr = ferr.Error()
		if snap.Source == "" {
			return
		}
		v.Stale = true
	}
	local, err := d.Ledger.Spend(name, snap.AsOf, time.Time{})
	if err != nil {
		v.ProviderErr = fmt.Sprint(err)
		return
	}
	m := billing.Merge(snap, local)
	v.Provider = &m
}
