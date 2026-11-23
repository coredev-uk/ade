package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/store"
)

func res(profile string, pid int, now time.Time, ttl time.Duration) Reservation {
	return Reservation{Profile: profile, Feature: "ai.summarize", PID: pid, Expires: now.Add(ttl)}
}

func take(usd float64) func(Held) (float64, error) {
	return func(Held) (float64, error) { return usd, nil }
}

func TestReservationCountsUntilReleased(t *testing.T) {
	l, _, _ := setup(t)
	now := time.Now().UTC()
	release, err := l.Reserve(res("a", os.Getpid(), now, time.Minute), now, take(0.5))
	if err != nil {
		t.Fatal(err)
	}
	var seen Held
	other, err := l.Reserve(res("b", os.Getpid(), now, time.Minute), now, func(h Held) (float64, error) { seen = h; return 0.25, nil })
	if err != nil {
		t.Fatal(err)
	}
	if seen.Total != 0.5 || seen.Profile("a") != 0.5 || seen.Profile("b") != 0 {
		t.Errorf("the second reserver saw %+v, want the first's $0.50 on profile a", seen)
	}
	if h, _ := l.Held(now); h.Total != 0.75 {
		t.Errorf("held = %v, want 0.75", h.Total)
	}
	release()
	release() // twice is fine
	if h, _ := l.Held(now); h.Total != 0.25 || h.Profile("a") != 0 {
		t.Errorf("after release: %+v", h)
	}
	other()
	if h, _ := l.Held(now); h.Total != 0 {
		t.Errorf("after both released: %+v", h)
	}
}

func TestRefusedCheckReservesNothing(t *testing.T) {
	l, _, _ := setup(t)
	now := time.Now().UTC()
	refuse := errors.New("over the cap")
	if _, err := l.Reserve(res("a", os.Getpid(), now, time.Minute), now, func(Held) (float64, error) { return 0.5, refuse }); !errors.Is(err, refuse) {
		t.Fatalf("err = %v", err)
	}
	if _, err := l.Reserve(res("a", os.Getpid(), now, time.Minute), now, take(0)); err != nil {
		t.Fatal(err)
	}
	if h, _ := l.Held(now); h.Total != 0 {
		t.Errorf("held = %v after a refusal and a zero reservation", h.Total)
	}
}

func TestCrashedAndOverdueReservationsExpire(t *testing.T) {
	l, _, _ := setup(t)
	now := time.Now().UTC()
	dead := 4242
	l.Alive = func(pid int, _ uint64) bool { return pid != dead }

	// A process that died holding a reservation: it stops counting at once.
	if _, err := l.Reserve(res("a", dead, now, time.Hour), now, take(1)); err != nil {
		t.Fatal(err)
	}
	if h, _ := l.Held(now); h.Total != 0 {
		t.Errorf("a dead process's reservation counts: %+v", h)
	}
	// A live pid (possibly reused) holds one only until it is overdue.
	if _, err := l.Reserve(res("a", os.Getpid(), now, time.Minute), now, take(2)); err != nil {
		t.Fatal(err)
	}
	if h, _ := l.Held(now.Add(30 * time.Second)); h.Total != 2 {
		t.Errorf("a live reservation inside its window: %+v", h)
	}
	if h, _ := l.Held(now.Add(2 * time.Minute)); h.Total != 0 {
		t.Errorf("an overdue reservation counts: %+v", h)
	}
	// The next reserver sweeps what is stale.
	var seen Held
	if _, err := l.Reserve(res("a", os.Getpid(), now, time.Minute), now.Add(2*time.Minute), func(h Held) (float64, error) { seen = h; return 0, nil }); err != nil {
		t.Fatal(err)
	}
	if seen.Total != 0 {
		t.Errorf("reserver saw stale reservations: %+v", seen)
	}
	var n int
	if err := l.db.QueryRow(`SELECT count(*) FROM ledger_reservations`).Scan(&n); err != nil || n != 0 {
		t.Errorf("stale rows left: %d %v", n, err)
	}
}

// Many reservers checking and reserving at once, through two ledgers on one
// cache.db as two ade processes would have: the check runs under the lock, so
// exactly as many get in as the cap holds.
func TestConcurrentReserversCannotAllPassTheSameCheck(t *testing.T) {
	l, log, dir := setup(t)
	st2, err := store.Open(filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })
	ledgers := []*Ledger{l, New(st2, log)}
	now := time.Now().UTC()
	const limit, each, callers = 1.0, 0.4, 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ledgers[i%2].Reserve(res("a", os.Getpid(), now, time.Minute), now, func(h Held) (float64, error) {
				if h.Total+each > limit {
					return 0, errors.New("over")
				}
				return each, nil
			})
			if err == nil {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != 2 {
		t.Errorf("%d of %d reservers passed a cap that holds 2", granted, callers)
	}
}
