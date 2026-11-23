package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/ledger"
)

// An agent call is a call in flight like any other: the daily cap counts what
// other calls have reserved, and the call reserves what it may spend.
func TestAgentCallCountsAndLeavesReservations(t *testing.T) {
	e := newEnv(t)
	e.m.Cfg.Budget = config.Budget{DailyUSD: 0.2, AgentRunUSD: 1.5}
	r, q := e.newRun(KindPlan, e.ctx()), e.newRun(KindPlan, e.ctx())
	e.m.AI = &ai.Service{Ledger: ledger.New(e.cache, e.m.Audit), Loc: time.UTC}
	ctx := context.Background()
	hold := func(usd float64) func() {
		t.Helper()
		release, err := e.m.AI.Reserve("t", "ai.summarize", time.Hour, func(ledger.Held) (float64, error) { return usd, nil })
		if err != nil {
			t.Fatal(err)
		}
		return release
	}

	// Another call has the whole day's room in flight: the run does not start.
	release := hold(0.2)
	t.Setenv("FAKE_OMP", "")
	got, err := e.m.Plan(ctx, q.ID)
	if _, ok := ai.AsStop(err); !ok || !strings.Contains(err.Error(), "in flight") || got.State != StateQueued || len(got.Calls) != 0 {
		t.Fatalf("a run starting while other calls hold the daily cap: %v %+v", err, got)
	}
	release()

	// With $0.15 of it in flight the call may spend only the other $0.05.
	release = hold(0.15)
	t.Setenv("FAKE_OMP", "overspend")
	r, err = e.m.Plan(ctx, r.ID)
	if _, ok := ai.AsStop(err); !ok || r.Calls[0].StoppedBy != StopCostCap || r.CostUSD > 0.7 {
		t.Fatalf("the call was not bounded by what the others left: %v %+v", err, r.Calls)
	}
	release()
	if h, _ := e.m.AI.Held(); h.Total != 0 {
		t.Errorf("the call kept its reservation after it ended: %+v", h)
	}
}
