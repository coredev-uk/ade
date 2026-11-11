package ledger

import (
	"sort"
	"strings"
	"time"
)

// Query selects ledger rows. Zero fields match everything.
type Query struct {
	From, To time.Time // From inclusive, To exclusive
	Profile  string
	Stops    bool // include budget.stop rows (they are never counted as spend)
	Item     string
	Limit    int // newest N; 0 = all
}

// Entries returns matching rows, oldest first. It indexes new audit entries
// first, so callers never see a stale ledger.
func (l *Ledger) Entries(q Query) ([]Entry, error) {
	if _, err := l.Sync(); err != nil {
		return nil, err
	}
	var where []string
	var args []any
	if !q.From.IsZero() {
		where, args = append(where, "ts >= ?"), append(args, q.From.UTC().Format(tsFormat))
	}
	if !q.To.IsZero() {
		where, args = append(where, "ts < ?"), append(args, q.To.UTC().Format(tsFormat))
	}
	if q.Profile != "" {
		where, args = append(where, "profile = ?"), append(args, q.Profile)
	}
	if q.Item != "" {
		where, args = append(where, "item = ?"), append(args, q.Item)
	}
	if !q.Stops {
		where = append(where, "action != '"+StopAction+"'")
	}
	query := `SELECT id, ts, action, feature, profile, driver, model, tokens_in, tokens_out, tokens_cache,
		cost_usd, cost_source, cache_hit, item, target, artifact, outcome, error FROM ledger_entries`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY ts, id"
	rows, err := l.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var ts string
		var hit int
		if err := rows.Scan(&e.ID, &ts, &e.Action, &e.Feature, &e.Profile, &e.Driver, &e.Model,
			&e.TokensIn, &e.TokensOut, &e.TokensCache, &e.CostUSD, &e.CostSource, &hit,
			&e.Item, &e.Target, &e.Artifact, &e.Outcome, &e.Error); err != nil {
			return nil, err
		}
		e.TS, _ = time.Parse(tsFormat, ts)
		e.CacheHit = hit != 0
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[len(out)-q.Limit:]
	}
	return out, nil
}

// Spend sums the cost of calls in [from, to) for profile ("" = every profile).
// Cache hits cost 0 and budget stops are not calls.
func (l *Ledger) Spend(profile string, from, to time.Time) (float64, error) {
	es, err := l.Entries(Query{From: from, To: to, Profile: profile})
	if err != nil {
		return 0, err
	}
	return Total(es), nil
}

// Total is the summed cost of es.
func Total(es []Entry) float64 {
	var sum float64
	for _, e := range es {
		sum += e.CostUSD
	}
	return sum
}

// Bucket is one group of entries: a day, a feature, a model.
type Bucket struct {
	Key         string  `json:"key"`
	Calls       int     `json:"calls"` // paid calls plus cache hits
	CacheHits   int     `json:"cache_hits"`
	TokensIn    int     `json:"tokens_in"`
	TokensOut   int     `json:"tokens_out"`
	TokensCache int     `json:"tokens_cache"`
	CostUSD     float64 `json:"cost_usd"`
}

// Group aggregates es by key. Buckets come back in key order.
func Group(es []Entry, key func(Entry) string) []Bucket {
	idx := map[string]*Bucket{}
	for _, e := range es {
		k := key(e)
		b := idx[k]
		if b == nil {
			b = &Bucket{Key: k}
			idx[k] = b
		}
		b.Calls++
		if e.CacheHit {
			b.CacheHits++
		}
		b.TokensIn += e.TokensIn
		b.TokensOut += e.TokensOut
		b.TokensCache += e.TokensCache
		b.CostUSD += e.CostUSD
	}
	out := make([]Bucket, 0, len(idx))
	for _, b := range idx {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ByDay keys entries by their local calendar day, "2006-01-02".
func ByDay(loc *time.Location) func(Entry) string {
	return func(e Entry) string { return e.TS.In(loc).Format("2006-01-02") }
}

func ByFeature(e Entry) string { return e.Feature }
func ByModel(e Entry) string   { return e.Model }
func ByProfile(e Entry) string { return e.Profile }

// TopItems returns the n most expensive items among es, costliest first.
// Entries without an item are skipped.
func TopItems(es []Entry, n int) []Bucket {
	bs := Group(es, func(e Entry) string { return e.Item })
	out := bs[:0]
	for _, b := range bs {
		if b.Key != "" {
			out = append(out, b)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CostUSD > out[j].CostUSD })
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// DayStart is local midnight of now's day.
func DayStart(now time.Time, loc *time.Location) time.Time {
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// WeekStart is local midnight of the Monday on or before now.
func WeekStart(now time.Time, loc *time.Location) time.Time {
	d := DayStart(now, loc)
	back := (int(d.Weekday()) + 6) % 7
	return d.AddDate(0, 0, -back)
}

// Periods is the spend of each budget period, across every profile.
type Periods struct {
	Day    float64 `json:"day_usd"`
	Week   float64 `json:"week_usd"`
	Sprint float64 `json:"sprint_usd"`
	// HaveSprint is false when no sprint window is known; Sprint is then 0
	// and its cap is not enforced rather than guessed.
	HaveSprint bool `json:"have_sprint"`
}

// Periods sums spend for today, this week and, when sprintStart says when
// the current sprint began, this sprint. Days and weeks are in loc.
func (l *Ledger) Periods(now time.Time, loc *time.Location, sprintStart func() (time.Time, bool)) (Periods, error) {
	var p Periods
	var err error
	if p.Day, err = l.Spend("", DayStart(now, loc), time.Time{}); err != nil {
		return p, err
	}
	if p.Week, err = l.Spend("", WeekStart(now, loc), time.Time{}); err != nil {
		return p, err
	}
	if sprintStart != nil {
		if from, ok := sprintStart(); ok {
			p.HaveSprint = true
			if p.Sprint, err = l.Spend("", from, time.Time{}); err != nil {
				return p, err
			}
		}
	}
	return p, nil
}
