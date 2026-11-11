package forgesync

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/store"
)

func init() {
	store.Register("forgesync.work_items", `CREATE TABLE work_items (
		project TEXT NOT NULL,
		ref     TEXT NOT NULL,
		json    TEXT NOT NULL,
		PRIMARY KEY (project, ref)
	)`)
	store.Register("forgesync.sync_state", `CREATE TABLE sync_state (
		project TEXT PRIMARY KEY,
		json    TEXT NOT NULL
	)`)
}

// stamped is a cached value with the time it was stored.
type stamped struct {
	At time.Time       `json:"at"`
	V  json.RawMessage `json:"v"`
}

// cachedValue returns the value stored under key in the kv table when it is
// younger than ttl, else calls fetch and stores the result. An unreadable
// entry counts as a miss; a failed store does not fail the fetch.
func cachedValue[T any](st *store.Store, key string, ttl time.Duration, now time.Time, fetch func() (T, error)) (T, error) {
	var zero T
	var raw string
	if err := st.DB.QueryRow(`SELECT v FROM kv WHERE k = ?`, key).Scan(&raw); err == nil {
		var s stamped
		var v T
		if json.Unmarshal([]byte(raw), &s) == nil && now.Sub(s.At) < ttl && json.Unmarshal(s.V, &v) == nil {
			return v, nil
		}
	}
	v, err := fetch()
	if err != nil {
		return zero, err
	}
	if b, err := json.Marshal(v); err == nil {
		if b, err = json.Marshal(stamped{At: now, V: b}); err == nil {
			st.DB.Exec(`INSERT INTO kv (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, key, string(b)) //nolint:errcheck // cache only
		}
	}
	return v, nil
}

// stateJSON is the sync_state row: the Result without its items.
func stateJSON(r Result) ([]byte, error) {
	r.Items = nil
	return json.Marshal(r)
}

// saveResult atomically replaces a project's cached items and state.
func saveResult(st *store.Store, r Result) error {
	state, err := stateJSON(r)
	if err != nil {
		return err
	}
	tx, err := st.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(`DELETE FROM work_items WHERE project = ?`, r.Project); err != nil {
		return fmt.Errorf("cache items: %w", err)
	}
	for _, wi := range r.Items {
		b, err := json.Marshal(wi)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO work_items (project, ref, json) VALUES (?, ?, ?)`, r.Project, wi.Ref.String(), string(b)); err != nil {
			return fmt.Errorf("cache item %s: %w", wi.Ref, err)
		}
	}
	if err := putState(tx, r.Project, state); err != nil {
		return err
	}
	return tx.Commit()
}

// saveFailure records a failed sync on top of the last good state (when
// there is one), keeping its cached items.
func saveFailure(st *store.Store, old Result, hadPrev bool, failed Result) error {
	r := failed
	if hadPrev {
		r = old
		r.Err = failed.Err
	}
	state, err := stateJSON(r)
	if err != nil {
		return err
	}
	return putState(st.DB, r.Project, state)
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func putState(db execer, project string, state []byte) error {
	_, err := db.Exec(`INSERT INTO sync_state (project, json) VALUES (?, ?) ON CONFLICT(project) DO UPDATE SET json = excluded.json`, project, string(state))
	if err != nil {
		return fmt.Errorf("cache state: %w", err)
	}
	return nil
}

// Load returns the cached Results, items included, without touching the
// network. Projects come back in name order.
func Load(st *store.Store) ([]Result, error) {
	rows, err := st.DB.Query(`SELECT json FROM sync_state ORDER BY project`)
	if err != nil {
		return nil, fmt.Errorf("load sync state: %w", err)
	}
	defer rows.Close()
	var results []Result
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r Result
		if json.Unmarshal([]byte(raw), &r) != nil {
			// Written by an older ADE: the cache is disposable and the next
			// sync rewrites it, so skip rather than break the dashboard.
			continue
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range results {
		if results[i].Items, err = loadItems(st, results[i].Project); err != nil {
			return nil, err
		}
	}
	return results, nil
}

func loadItems(st *store.Store, project string) ([]domain.WorkItem, error) {
	rows, err := st.DB.Query(`SELECT json FROM work_items WHERE project = ? ORDER BY rowid`, project)
	if err != nil {
		return nil, fmt.Errorf("load items: %w", err)
	}
	defer rows.Close()
	var items []domain.WorkItem
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var wi domain.WorkItem
		if json.Unmarshal([]byte(raw), &wi) != nil {
			continue // older format; the next sync rewrites it
		}
		items = append(items, wi)
	}
	return items, rows.Err()
}
