package ledger

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/coredev-uk/ade/internal/store"
)

func init() {
	store.Register("ledger.reservations", `CREATE TABLE ledger_reservations (
		id        TEXT PRIMARY KEY,
		profile   TEXT NOT NULL,
		feature   TEXT NOT NULL,
		usd       REAL NOT NULL,
		pid       INTEGER NOT NULL,
		pid_start INTEGER NOT NULL,
		created   TEXT NOT NULL,  -- RFC 3339 UTC
		expires   TEXT NOT NULL
	)`)
}

// Reservation is the most a call in flight may still cost: its pre-flight
// ceiling, recorded before the call is sent and dropped when the call is
// booked (or fails). Every ade process sharing cache.db sees it, so calls that
// each pass the caps alone cannot pass them together.
type Reservation struct {
	ID      string
	Profile string
	Feature string
	USD     float64
	// PID and PIDStart identify the process that holds it: once it is gone the
	// reservation is stale. Expires bounds a reservation whose pid was reused.
	PID      int
	PIDStart uint64
	Created  time.Time
	Expires  time.Time
}

// Held is what the live reservations add up to.
type Held struct {
	Total     float64
	ByProfile map[string]float64
}

// Profile is what is reserved on one profile.
func (h Held) Profile(name string) float64 { return h.ByProfile[name] }

// reserveMu orders reservers inside one process; the lock file orders them
// across processes.
var reserveMu sync.Mutex

// lock serialises check-and-reserve across goroutines and ade processes. The
// kernel drops the file lock when a process dies, so it never outlives one.
func (l *Ledger) lock() (unlock func(), err error) {
	reserveMu.Lock()
	if l.path == "" {
		return reserveMu.Unlock, nil
	}
	f, err := os.OpenFile(l.path+".reserve.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		reserveMu.Unlock()
		return nil, fmt.Errorf("reservation lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		reserveMu.Unlock()
		return nil, fmt.Errorf("reservation lock: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck // closing drops it anyway
		f.Close()
		reserveMu.Unlock()
	}, nil
}

// Held sums the live reservations at now: not expired, and held by a process
// that still runs. It changes nothing.
func (l *Ledger) Held(now time.Time) (Held, error) {
	h, _, err := l.scan(now)
	return h, err
}

// scan returns the live reservations' sum and the ids of the stale ones.
func (l *Ledger) scan(now time.Time) (Held, []string, error) {
	h := Held{ByProfile: map[string]float64{}}
	rows, err := l.db.Query(`SELECT id, profile, usd, pid, pid_start, expires FROM ledger_reservations`)
	if err != nil {
		return h, nil, err
	}
	defer rows.Close()
	var stale []string
	for rows.Next() {
		var (
			id, profile, expires string
			usd                  float64
			pid                  int
			start                uint64
		)
		if err := rows.Scan(&id, &profile, &usd, &pid, &start, &expires); err != nil {
			return h, nil, err
		}
		exp, perr := time.Parse(tsFormat, expires)
		if perr != nil || !now.Before(exp) || !l.alive(pid, start) {
			stale = append(stale, id)
			continue
		}
		h.Total += usd
		h.ByProfile[profile] += usd
	}
	return h, stale, rows.Err()
}

func (l *Ledger) alive(pid int, start uint64) bool {
	if l.Alive != nil {
		return l.Alive(pid, start)
	}
	return pidAlive(pid)
}

// pidAlive is the default liveness test: signal 0 reaches the process (EPERM
// still means it exists).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// Reserve records a reservation for r if check allows it. check runs under the
// reservation lock, so what it reads (the live reservations it is given, and
// the spend it reads itself) cannot change through another reserver between
// the check and the insert; it returns the amount to reserve, or an error to
// refuse. A reservation of 0 is not recorded. Stale reservations (their
// process gone, or past Expires) are dropped first. The returned release
// removes the reservation and is safe to call twice; call it once the call's
// real cost is booked, so the cost is never missing from both.
func (l *Ledger) Reserve(r Reservation, now time.Time, check func(Held) (float64, error)) (release func(), err error) {
	unlock, err := l.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	h, stale, err := l.scan(now)
	if err != nil {
		return nil, err
	}
	for _, id := range stale {
		if _, err := l.db.Exec(`DELETE FROM ledger_reservations WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	usd, err := check(h)
	if err != nil {
		return nil, err
	}
	if usd <= 0 {
		return func() {}, nil
	}
	r.ID, r.USD, r.Created = ulid.Make().String(), usd, now
	if _, err := l.db.Exec(`INSERT INTO ledger_reservations (id, profile, feature, usd, pid, pid_start, created, expires)
		VALUES (?,?,?,?,?,?,?,?)`, r.ID, r.Profile, r.Feature, r.USD, r.PID, r.PIDStart,
		r.Created.UTC().Format(tsFormat), r.Expires.UTC().Format(tsFormat)); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() { _, _ = l.db.Exec(`DELETE FROM ledger_reservations WHERE id = ?`, r.ID) })
	}, nil
}
