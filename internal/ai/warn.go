package ai

import (
	"fmt"
	"math"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/ledger"
)

// WarnAction is the audit action (and event kind) raised once per budget
// period when spend reaches WarnFraction of the period's cap.
const WarnAction = "budget.warn"

// WarnFraction is how much of a cap triggers the warning (PLAN 6.8: 80 %).
const WarnFraction = 0.8

// epsilon absorbs float rounding in summed costs, so exactly 80 % warns.
const epsilon = 1e-9

// warnBudget runs after a paid call. For each capped period (daily, weekly,
// sprint) whose spend has reached 80 % of the cap it records a budget.warn
// audit entry and publishes the event, unless the log already holds one for
// that period: the audit log is what makes it "once" across processes.
// Warning failures never fail the call that was already paid for.
func (s *Service) warnBudget(item string) {
	b := s.Cfg.Budget
	if b.DailyUSD <= 0 && b.WeeklyUSD <= 0 && b.SprintUSD <= 0 {
		return
	}
	now := s.Now()
	var sprintFrom time.Time
	haveSprint := false
	if s.SprintStart != nil {
		sprintFrom, haveSprint = s.SprintStart()
	}
	spent, err := s.Ledger.Periods(now, s.Loc, s.SprintStart)
	if err != nil {
		return
	}
	periods := []struct {
		name, label string
		from        time.Time
		spent, cap  float64
		on          bool
	}{
		{"daily", "today", ledger.DayStart(now, s.Loc), spent.Day, b.DailyUSD, true},
		{"weekly", "this week", ledger.WeekStart(now, s.Loc), spent.Week, b.WeeklyUSD, true},
		{"sprint", "this sprint", sprintFrom, spent.Sprint, b.SprintUSD, haveSprint},
	}
	for _, p := range periods {
		if !p.on || p.cap <= 0 || p.spent+epsilon < WarnFraction*p.cap {
			continue
		}
		key := p.name + ":" + p.from.In(s.Loc).Format(time.DateOnly)
		if s.warned(key, p.from) {
			continue
		}
		pct := int(math.Round(p.spent / p.cap * 100))
		s.record(audit.Entry{
			Action: WarnAction, Target: item, Item: item,
			Params: map[string]any{"period": p.name, "key": key, "spent_usd": p.spent, "cap_usd": p.cap, "percent": pct},
		})
		if s.Bus != nil {
			e := events.New(WarnAction, events.Warn, fmt.Sprintf("AI spend at %d%% of the %s cap: %s of %s %s", pct, p.name, usd(p.spent), usd(p.cap), p.label))
			e.Subject = item
			s.Bus.Publish(e)
		}
	}
}

// warned reports whether a budget.warn for key was already recorded in the
// period that began at from.
func (s *Service) warned(key string, from time.Time) bool {
	es, err := s.Audit.Query(audit.Filter{Action: WarnAction, Since: from})
	if err != nil {
		// Unable to tell: stay quiet rather than warn on every call.
		return true
	}
	for _, e := range es {
		if k, _ := e.Params["key"].(string); k == key {
			return true
		}
	}
	return false
}
