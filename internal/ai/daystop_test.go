package ai

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/ledger"
)

// drainKinds empties a subscription and counts the events of kind.
func drainKinds(sub <-chan events.Event, kind string) int {
	n := 0
	for {
		select {
		case ev := <-sub:
			if ev.Kind == kind {
				n++
			}
		default:
			return n
		}
	}
}

// A provider out of budget refuses every call. Each refusal is audited, but
// only the profile's first of the day is a notification; the next day the
// first refusal raises one again.
func TestBudgetStopNotifiesOncePerProfilePerDay(t *testing.T) {
	e := newEnv(t)
	e.drv.resp = Response{}
	e.drv.err = &StopError{Source: "gateway", Message: "Budget has been exceeded!"}
	sub, cancel := e.bus.Subscribe(64)
	defer cancel()

	day := time.Now()
	e.svc.Now = func() time.Time { return day }
	const n = 4
	for i := range n {
		if _, err := e.svc.Run(context.Background(), summarizeJob(fmt.Sprint("head", i))); err == nil {
			t.Fatal("want the stop")
		}
	}
	if got := len(e.entries(ledger.StopAction)); got != n {
		t.Fatalf("%d budget.stop audit entries, want %d: every refusal is audited", got, n)
	}
	if got := drainKinds(sub, ledger.StopAction); got != 1 {
		t.Fatalf("%d budget.stop events for %d refusals in one day, want 1", got, n)
	}

	// Another process (a fresh service on the same audit log) the same day
	// stays quiet too.
	e.open()
	e.svc.Now = func() time.Time { return day }
	if _, err := e.svc.Run(context.Background(), summarizeJob("other-process")); err == nil {
		t.Fatal("want the stop")
	}
	if got := drainKinds(sub, ledger.StopAction); got != 0 {
		t.Fatalf("a second process raised %d budget.stop events on the same day", got)
	}

	// The next day the first refusal is news again, once.
	e.svc.Now = func() time.Time { return day.Add(24 * time.Hour) }
	for i := range 2 {
		if _, err := e.svc.Run(context.Background(), summarizeJob(fmt.Sprint("next", i))); err == nil {
			t.Fatal("want the stop")
		}
	}
	if got := drainKinds(sub, ledger.StopAction); got != 1 {
		t.Fatalf("%d budget.stop events the next day, want 1", got)
	}
	if got := len(e.entries(ledger.StopAction)); got != n+3 {
		t.Fatalf("%d budget.stop audit entries, want %d", got, n+3)
	}
}

// The dedupe is per profile: a stop on one profile does not silence another.
func TestBudgetStopOnAnotherProfileIsItsOwnAlert(t *testing.T) {
	e := newEnv(t)
	sub, cancel := e.bus.Subscribe(8)
	defer cancel()
	now := time.Now()
	for _, p := range []string{"p", "q", "p"} {
		RecordStop(e.log, e.bus, now, time.UTC, Stopped{Item: "x", Feature: "summarize", Profile: p, Stop: &StopError{Source: "gateway", Message: "out"}})
	}
	if got := drainKinds(sub, ledger.StopAction); got != 2 {
		t.Fatalf("%d events for stops on p, q, p; want 2", got)
	}
}

// An automatic call on a stopped day is not made, not even refused and
// audited; a cached answer is still served, and a call someone asked for
// still goes out.
func TestAutoCallIsNotMadeOnAStoppedDay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cached := summarizeJob("cached")
	if _, err := e.svc.Run(ctx, cached); err != nil {
		t.Fatal(err)
	}
	e.log.Append(audit.Entry{
		Action: ledger.StopAction, Item: "x", Outcome: "error", Error: "Budget has been exceeded",
		Params: map[string]any{"feature": "review", "profile": "p", "source": "gateway"},
	})
	calls := e.drv.calls

	auto := summarizeJob("new")
	auto.Auto = true
	if _, err := e.svc.Run(ctx, auto); !errors.Is(err, ErrAutoPaused) {
		t.Fatalf("err = %v, want ErrAutoPaused", err)
	}
	if e.drv.calls != calls || len(e.entries(ledger.StopAction)) != 1 || len(e.entries("ai.summarize")) != 1 {
		t.Fatalf("a paused automatic call reached the driver or the audit log (calls %d)", e.drv.calls-calls)
	}

	cached.Auto = true
	if res, err := e.svc.Run(ctx, cached); err != nil || !res.CacheHit {
		t.Fatalf("a cached answer is free and must still be served: %+v, %v", res, err)
	}

	if _, err := e.svc.Run(ctx, summarizeJob("asked")); err != nil || e.drv.calls != calls+1 {
		t.Fatalf("a call someone asked for must still go out: %v (calls %d)", err, e.drv.calls-calls)
	}

	// Tomorrow the automatic call goes out again.
	e.svc.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	if _, err := e.svc.Run(ctx, auto); err != nil {
		t.Fatalf("the next day: %v", err)
	}
}
