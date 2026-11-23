package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/ledger"
)

// gateDriver holds every call inside Complete until release is closed, and
// says (on started) when one got there.
type gateDriver struct {
	fakeDriver
	mu      sync.Mutex
	started chan struct{}
	release chan struct{}
	entered int
}

func newGate(f fakeDriver) *gateDriver {
	return &gateDriver{fakeDriver: f, started: make(chan struct{}, 16), release: make(chan struct{})}
}

func (g *gateDriver) Complete(_ context.Context, c Call) (Response, error) {
	g.mu.Lock()
	g.entered++
	g.mu.Unlock()
	g.started <- struct{}{}
	<-g.release
	return g.resp, g.err
}

func (g *gateDriver) inside() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.entered
}

// capForTwo sets the daily cap so that two calls fit but not three, and
// returns one call's estimate. Every call but the first is a different prompt,
// so none is a cache hit.
func capForTwo(t *testing.T, e *env) float64 {
	t.Helper()
	pf, _, err := e.svc.Preview(context.Background(), summarizeJob("probe"))
	if err != nil || pf.EstimateUSD <= 0 {
		t.Fatalf("preview: %+v %v", pf, err)
	}
	e.conf.Budget.DailyUSD = 2.5 * pf.EstimateUSD
	e.drv.resp.CostUSD = pf.EstimateUSD / 100 // booked at a small real cost, so a finished call frees its room
	return pf.EstimateUSD
}

func TestCallInFlightCountsAgainstTheCap(t *testing.T) {
	e := newEnv(t)
	est := capForTwo(t, e)
	gate := newGate(*e.drv)
	e.svc.Open = func(context.Context, config.AIProfile) (Driver, error) { return gate, nil }
	ctx := context.Background()

	done := make(chan error, 2)
	for _, head := range []string{"one", "two"} {
		go func() { _, err := e.svc.Run(ctx, summarizeJob(head)); done <- err }()
	}
	<-gate.started
	<-gate.started // two calls are in flight, $0 booked

	_, err := e.svc.Run(ctx, summarizeJob("three"))
	stop, ok := AsStop(err)
	if !ok || stop.Source != "local" || !strings.Contains(stop.Message, "daily cap") || !strings.Contains(stop.Message, "in flight") {
		t.Fatalf("a third call beside two in flight must be refused over the daily cap, naming them: %v", err)
	}
	if n := gate.inside(); n != 2 {
		t.Errorf("%d calls reached the driver, want 2", n)
	}
	if len(e.entries("budget.stop")) != 1 {
		t.Error("the refusal is a budget.stop")
	}
	pf, _, err := e.svc.Preview(ctx, summarizeJob("four"))
	if err != nil || pf.InFlightUSD < 1.9*est || pf.Refuse == "" {
		t.Errorf("the preview must show what is in flight and refuse: %+v %v", pf, err)
	}

	close(gate.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if h, _ := e.svc.Held(); h.Total != 0 {
		t.Errorf("reservations outlived their calls: %+v", h)
	}
	// Booked at their (small) real cost, the two no longer hold the room.
	if _, err := e.svc.Run(ctx, summarizeJob("five")); err != nil {
		t.Errorf("room freed by finished calls: %v", err)
	}
}

// All callers pass the pre-flight together (nothing is reserved yet), so only
// the check at reservation time tells them apart.
func TestCallsThatPassPreflightTogetherAreCheckedAgainAtReservation(t *testing.T) {
	e := newEnv(t)
	capForTwo(t, e)
	gate := newGate(*e.drv)
	e.svc.Open = func(context.Context, config.AIProfile) (Driver, error) { return gate, nil }
	ctx := context.Background()

	const callers = 6
	type out struct {
		err error
	}
	results := make(chan out, callers)
	for i := range callers {
		go func() {
			_, err := e.svc.Run(ctx, summarizeJob("c"+string(rune('a'+i))))
			results <- out{err}
		}()
	}
	// The refused callers return at once; the two that got in wait at the gate.
	refused := 0
	for refused < callers-2 {
		r := <-results
		if _, ok := AsStop(r.err); !ok {
			t.Fatalf("a caller failed without being stopped: %v", r.err)
		}
		refused++
	}
	<-gate.started
	<-gate.started
	if n := gate.inside(); n != 2 {
		t.Errorf("%d calls got through a cap that holds 2", n)
	}
	close(gate.release)
	for range 2 {
		if r := <-results; r.err != nil {
			t.Errorf("a caller that was let in failed: %v", r.err)
		}
	}
}

func TestReservationOfACrashedProcessExpires(t *testing.T) {
	e := newEnv(t)
	est := capForTwo(t, e)
	now := time.Now().UTC()
	dead := 424242
	e.svc.Ledger.Alive = func(pid int, _ uint64) bool { return pid != dead }
	// A process that died mid-call left a reservation that takes the whole day's room.
	if _, err := e.svc.Ledger.Reserve(ledger.Reservation{Profile: "p", Feature: "ai.summarize", PID: dead, Expires: now.Add(time.Hour)}, now, func(ledger.Held) (float64, error) {
		return 5 * est, nil
	}); err != nil {
		t.Fatal(err)
	}
	if h, _ := e.svc.Held(); h.Total != 0 {
		t.Fatalf("a dead process's reservation counts: %+v", h)
	}
	if _, err := e.svc.Run(context.Background(), summarizeJob("after-crash")); err != nil {
		t.Errorf("a crashed process's reservation blocked a call: %v", err)
	}

	// A live pid that never finished (or a reused one) is ignored once the call
	// could not still be running.
	e.svc.Ledger.Alive = nil
	live := ledger.Reservation{Profile: "p", Feature: "ai.summarize", PID: os.Getpid(), Expires: now.Add(callTTL(config.AIProfile{Driver: "omp"}))}
	if _, err := e.svc.Ledger.Reserve(live, now, func(ledger.Held) (float64, error) { return 5 * est, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Run(context.Background(), summarizeJob("while-live")); err == nil {
		t.Fatal("a live reservation must hold its room")
	}
	e.svc.Now = func() time.Time { return now.Add(callTTL(config.AIProfile{Driver: "omp"}) + time.Second) }
	if _, err := e.svc.Run(context.Background(), summarizeJob("after-overdue")); err != nil {
		t.Errorf("an overdue reservation blocked a call: %v", err)
	}
}

func TestReservationIsReleasedWhenTheCallFails(t *testing.T) {
	e := newEnv(t)
	capForTwo(t, e)
	e.drv.err = errors.New("boom")
	if _, err := e.svc.Run(context.Background(), summarizeJob("fails")); err == nil {
		t.Fatal("want the driver's error")
	}
	if h, _ := e.svc.Held(); h.Total != 0 {
		t.Errorf("a failed call keeps its reservation: %+v", h)
	}

	// A budget stop from the provider releases it as well.
	e.drv.err = &StopError{Source: "provider", Message: "out of credit"}
	if _, err := e.svc.Run(context.Background(), summarizeJob("stopped")); err == nil {
		t.Fatal("want the stop")
	}
	if h, _ := e.svc.Held(); h.Total != 0 {
		t.Errorf("a stopped call keeps its reservation: %+v", h)
	}
}

func TestReservationIsHeldWhileTheCallRunsAndOnlyThen(t *testing.T) {
	e := newEnv(t)
	est := capForTwo(t, e)
	gate := newGate(*e.drv)
	e.svc.Open = func(context.Context, config.AIProfile) (Driver, error) { return gate, nil }
	done := make(chan error, 1)
	go func() { _, err := e.svc.Run(context.Background(), summarizeJob("held")); done <- err }()
	<-gate.started
	h, err := e.svc.Held()
	if err != nil || h.Total < est*0.99 || h.Total > est*1.01 || h.Profile("p") != h.Total {
		t.Errorf("in flight = %+v (%v), want the call's estimate %v on profile p", h, err, est)
	}
	close(gate.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if h, _ := e.svc.Held(); h.Total != 0 {
		t.Errorf("held after the call: %+v", h)
	}
}

// The Anthropic API answers an empty prepaid balance with a 400
// invalid_request_error (docs/billing/vendors.md): it is a budget stop, once.
func TestAnthropicEmptyBalanceIsABudgetStop(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."},"request_id":"req_x"}`))
	}))
	defer srv.Close()
	d := newAnthropic(config.AIProfile{BaseURL: srv.URL}, "k", srv.Client())
	_, err := d.Complete(context.Background(), Call{Model: "claude-x", Prompt: "p", MaxTokens: 10})
	stop, ok := AsStop(err)
	if !ok || stop.Source != "provider" || !strings.Contains(stop.Message, "credit balance is too low") {
		t.Fatalf("err = %v, want a provider budget stop", err)
	}
	if calls != 1 {
		t.Errorf("a budget stop is not retried: %d requests", calls)
	}
}

func TestAnthropicEmptyBalanceBecomesABudgetStopEntry(t *testing.T) {
	e := newEnv(t)
	e.drv.err = classifyStop("provider", []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`))
	_, err := e.svc.Run(context.Background(), summarizeJob("empty"))
	if _, ok := AsStop(err); !ok {
		t.Fatalf("err = %v", err)
	}
	if es := e.entries("budget.stop"); len(es) != 1 || !strings.Contains(es[0].Error, "credit balance is too low") {
		t.Errorf("budget.stop entries: %+v", es)
	}
}
