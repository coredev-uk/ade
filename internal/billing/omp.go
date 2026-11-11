package billing

import (
	"context"
	"time"
)

// OMP is the subscription source: omp has no quota endpoint, so the figure is
// ADE's own ledger of omp's list-price cost this week against weekly_soft_usd
// (docs/billing: "usage vs a soft cap"). The cap is soft; it asks for
// confirmation, it never blocks.
type OMP struct {
	// SoftWeeklyUSD is billing.weekly_soft_usd; 0 means no soft cap.
	SoftWeeklyUSD float64
	// WeekSpend returns the profile's ledger spend in [from, to).
	WeekSpend func(from, to time.Time) (float64, error)
	// WeekStart returns the start of the week containing t.
	WeekStart func(t time.Time) time.Time
	Now       func() time.Time
}

func (o OMP) Name() string            { return SourceOMP }
func (o OMP) Interval() time.Duration { return 0 } // local: always fresh

func (o OMP) Fetch(_ context.Context) (Snapshot, error) {
	now := o.Now()
	from := o.WeekStart(now)
	spent, err := o.WeekSpend(from, now.Add(time.Nanosecond))
	if err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{
		Source: SourceOMP, AsOf: now.UTC(), Scope: "local", Window: "week",
		Spend: &spent,
		Note:  "list-price usage from omp; the subscription quota is not observable",
	}
	s.ResetAt = new(from.AddDate(0, 0, 7))
	if o.SoftWeeklyUSD > 0 {
		s.SoftLimit = new(o.SoftWeeklyUSD)
	}
	return s, nil
}
