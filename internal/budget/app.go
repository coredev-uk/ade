package budget

import (
	"context"
	"time"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/billing"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/ledger"
)

// ForApp wires Deps from the app: the ledger and billing snapshots live in
// cache.db, and the sprint window comes from the last sync.
func ForApp(a *app.App) (Deps, error) {
	st, err := a.Cache()
	if err != nil {
		return Deps{}, err
	}
	l := ledger.New(st, a.Audit)
	loc := ai.Location(a.Config)
	d := Deps{
		Cfg: a.Config, Ledger: l, Audit: a.Audit, Billing: billing.NewCache(st),
		Now: time.Now, Loc: loc,
		SprintStart: SprintStart(a),
	}
	d.Source = func(ctx context.Context, name string, p config.AIProfile) (billing.Source, error) {
		return billing.New(ctx, p, billing.Local{
			Spend:     func(from, to time.Time) (float64, error) { return l.Spend(name, from, to) },
			WeekStart: func(t time.Time) time.Time { return ledger.WeekStart(t, loc) },
			Now:       time.Now,
		})
	}
	return d, nil
}

// SprintStart reports when the current sprint began, from the cached sync
// results (no network). It is false when no sprint is known.
func SprintStart(a *app.App) func() (time.Time, bool) {
	return func() (time.Time, bool) {
		st, err := a.Cache()
		if err != nil {
			return time.Time{}, false
		}
		results, err := forgesync.Load(st)
		if err != nil {
			return time.Time{}, false
		}
		loc := ai.Location(a.Config)
		for _, r := range results {
			if cur := r.Window.Current; cur != nil && !cur.Start.IsZero() {
				y, m, d := cur.Start.Date()
				return time.Date(y, m, d, 0, 0, 0, 0, loc), true
			}
		}
		return time.Time{}, false
	}
}
