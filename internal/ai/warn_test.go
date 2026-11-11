package ai

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/events"
)

// callN runs n distinct summaries (each costs the fake driver's $0.002).
func (e *env) callN(n int, tag string) {
	e.t.Helper()
	for i := range n {
		if _, err := e.svc.Run(context.Background(), summarizeJob(tag+string(rune('a'+i)))); err != nil {
			e.t.Fatal(err)
		}
	}
}

func drain(sub <-chan events.Event) []events.Event {
	var out []events.Event
	for {
		select {
		case ev := <-sub:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestBudgetWarnsOnceAtEightyPercentOfACap(t *testing.T) {
	e := newEnv(t)
	e.conf.Budget.DailyUSD = 0.03
	// $0.016 spent today, just over half the cap. Each call adds $0.002.
	e.log.Append(audit.Entry{Action: "ai.summarize", Item: "x", AI: &audit.AIInfo{Profile: "p", Model: "m", CostUSD: 0.016, CostSource: CostReported}})
	sub, cancel := e.bus.Subscribe(16)
	defer cancel()

	e.callN(2, "below") // $0.020: 67 %
	if evs := drain(sub); len(evs) != 0 || len(e.entries(WarnAction)) != 0 {
		t.Fatalf("warned below 80%%: %+v", evs)
	}

	e.callN(2, "cross") // $0.024: exactly 80 %
	evs := drain(sub)
	if len(evs) != 1 || evs[0].Kind != WarnAction || evs[0].Severity != events.Warn ||
		!strings.Contains(evs[0].Title, "80%") || !strings.Contains(evs[0].Title, "daily") {
		t.Fatalf("events at 80%%: %+v", evs)
	}
	ws := e.entries(WarnAction)
	if len(ws) != 1 || ws[0].Params["period"] != "daily" || ws[0].Params["cap_usd"] != 0.03 {
		t.Fatalf("audit entries: %+v", ws)
	}

	e.callN(1, "past") // $0.026: 87 %, already warned this day
	if evs := drain(sub); len(evs) != 0 {
		t.Fatalf("warned twice in one period: %+v", evs)
	}

	// A new process (the CLI) must not warn again either: the audit log is the memory.
	e.open()
	sub2, cancel2 := e.bus.Subscribe(16)
	defer cancel2()
	e.callN(1, "fresh")
	if evs := drain(sub2); len(evs) != 0 || len(e.entries(WarnAction)) != 1 {
		t.Fatalf("a second process warned again: %+v, %d audit entries", evs, len(e.entries(WarnAction)))
	}
}

func TestBudgetWarnsPerPeriod(t *testing.T) {
	e := newEnv(t)
	e.conf.Budget.WeeklyUSD = 0.02
	e.conf.Budget.SprintUSD = 0.04
	e.svc.SprintStart = func() (time.Time, bool) { return time.Now().Add(-24 * time.Hour), true }
	e.log.Append(audit.Entry{Action: "ai.summarize", Item: "x", AI: &audit.AIInfo{Profile: "p", Model: "m", CostUSD: 0.014, CostSource: CostReported}})
	sub, cancel := e.bus.Subscribe(16)
	defer cancel()

	e.callN(1, "w") // $0.016: weekly 80 %, sprint 40 %
	evs := drain(sub)
	if len(evs) != 1 || !strings.Contains(evs[0].Title, "weekly") {
		t.Fatalf("want only the weekly warning, got %+v", evs)
	}

	e.log.Append(audit.Entry{Action: "ai.summarize", Item: "x", AI: &audit.AIInfo{Profile: "p", Model: "m", CostUSD: 0.016, CostSource: CostReported}})
	e.conf.Budget.WeeklyUSD = 0 // the weekly cap is off now; only the sprint is watched
	e.callN(1, "s")             // $0.034: sprint 85 %
	evs = drain(sub)
	if len(evs) != 1 || !strings.Contains(evs[0].Title, "sprint") {
		t.Fatalf("want the sprint warning, got %+v", evs)
	}
	if n := len(e.entries(WarnAction)); n != 2 {
		t.Errorf("budget.warn audit entries = %d, want 2", n)
	}
}

func TestNoCapsNoWarning(t *testing.T) {
	e := newEnv(t)
	sub, cancel := e.bus.Subscribe(16)
	defer cancel()
	e.callN(2, "n")
	if evs := drain(sub); len(evs) != 0 {
		t.Fatalf("events without caps: %+v", evs)
	}
}
