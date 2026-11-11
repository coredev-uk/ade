package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/coredev-uk/ade/internal/store"
)

func init() {
	store.Register("billing.snapshots", `CREATE TABLE billing_snapshots (
		profile    TEXT PRIMARY KEY,
		snapshot   TEXT NOT NULL,
		fetched_at TEXT NOT NULL
	)`)
}

// Cache keeps the last snapshot per profile in cache.db, so the budget page
// renders at once and sources are polled at their own cadence, not per paint.
type Cache struct {
	db  *sql.DB
	now func() time.Time
}

// NewCache wraps an open cache.
func NewCache(st *store.Store) *Cache { return &Cache{db: st.DB, now: time.Now} }

// Get returns the profile's last snapshot and when it was fetched.
func (c *Cache) Get(profile string) (Snapshot, time.Time, bool, error) {
	var raw, at string
	err := c.db.QueryRow(`SELECT snapshot, fetched_at FROM billing_snapshots WHERE profile = ?`, profile).Scan(&raw, &at)
	if err == sql.ErrNoRows {
		return Snapshot{}, time.Time{}, false, nil
	}
	if err != nil {
		return Snapshot{}, time.Time{}, false, err
	}
	var s Snapshot
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Snapshot{}, time.Time{}, false, nil // a snapshot from an older shape is just a miss
	}
	t, _ := time.Parse(time.RFC3339Nano, at)
	return s, t, true, nil
}

// Put stores a snapshot for profile.
func (c *Cache) Put(profile string, s Snapshot) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(`INSERT INTO billing_snapshots (profile, snapshot, fetched_at) VALUES (?, ?, ?)
		ON CONFLICT(profile) DO UPDATE SET snapshot = excluded.snapshot, fetched_at = excluded.fetched_at`,
		profile, string(raw), c.now().UTC().Format(time.RFC3339Nano))
	return err
}

// Refresh returns the profile's snapshot, fetching it when the cached one is
// older than the source's interval (or force is set). When the fetch fails it
// returns the stale snapshot, if any, together with the error, so the page
// can show old figures labelled as old.
func (c *Cache) Refresh(ctx context.Context, profile string, src Source, force bool) (Snapshot, error) {
	old, at, ok, err := c.Get(profile)
	if err != nil {
		return Snapshot{}, err
	}
	if ok && !force && src.Interval() > 0 && c.now().Sub(at) < src.Interval() {
		return old, nil
	}
	s, ferr := src.Fetch(ctx)
	if ferr != nil {
		return old, ferr
	}
	if err := c.Put(profile, s); err != nil {
		return s, err
	}
	return s, nil
}
