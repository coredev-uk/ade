package ai

import (
	"os"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/ledger"
	"github.com/coredev-uk/ade/internal/store"
	"github.com/coredev-uk/ade/internal/workspace"
)

// Calls in flight are not in the ledger until they finish, so each one would
// pass the pre-flight against the same spend: several MR-page calls at once, a
// detached research worker beside the dashboard, two ade processes, an agent
// run. Before a call is sent its pre-flight ceiling is reserved in cache.db
// (ledger.Reserve: checked and recorded under a file lock, so across
// processes too); the caps count every live reservation as spent. The
// reservation is dropped once the call is booked in the audit log (the booked
// cost replaces it), on failure alike. One whose process died, or that outlived
// its call's longest possible run, is ignored and swept (callTTL).

// reserveMargin is added to a call's longest possible run before its
// reservation may be treated as stale even though its pid is still alive (a
// reused pid).
const reserveMargin = 2 * time.Minute

// callTTL is the longest an AI call from p can run: one omp call is bounded by
// the profile's timeout; an HTTP driver tries up to three times and asks again
// once when cut short (retryTruncated), each request bounded by it.
func callTTL(p config.AIProfile) time.Duration {
	t, attempts := p.Timeout.Duration, 1
	if p.Driver == "omp" {
		if t <= 0 {
			t = defaultOMPTimeout
		}
	} else {
		if t <= 0 {
			t = 5 * time.Minute
		}
		attempts = 2 * maxHTTPAttempts
	}
	return time.Duration(attempts)*t + reserveMargin
}

// newLedger opens the ledger so that a reservation counts as live only while
// the process that made it (same pid and start time, not a reused pid) runs.
func newLedger(st *store.Store, log *audit.Log) *ledger.Ledger {
	l := ledger.New(st, log)
	l.Alive = workspace.ProcessAlive
	return l
}

// Held is what calls in flight have reserved (zero without a ledger).
func (s *Service) Held() (ledger.Held, error) {
	if s.Ledger == nil {
		return ledger.Held{}, nil
	}
	return s.Ledger.Held(s.clock())
}

// clock is Now, or the wall clock for a Service built without one.
func (s *Service) clock() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// Reserve is ledger.Reserve for one call of profile: check runs under the
// reservation lock with what is held and returns the ceiling to reserve, or the
// error that refuses the call. The returned release must be called once the
// call's cost is booked (or it failed). Without a ledger nothing is shared, so
// check runs alone.
func (s *Service) Reserve(profile, feature string, ttl time.Duration, check func(ledger.Held) (float64, error)) (release func(), err error) {
	if s.Ledger == nil {
		if _, err := check(ledger.Held{}); err != nil {
			return nil, err
		}
		return func() {}, nil
	}
	now := s.clock()
	pid := os.Getpid()
	return s.Ledger.Reserve(ledger.Reservation{
		Profile: profile, Feature: feature,
		PID: pid, PIDStart: workspace.ProcessStart(pid), Expires: now.Add(ttl),
	}, now, check)
}
