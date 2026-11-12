package forgesync

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/store"
	"github.com/coredev-uk/ade/internal/workspace"
)

func init() {
	store.Register("forgesync.work_items", `CREATE TABLE work_items (
		project TEXT NOT NULL,
		ref     TEXT NOT NULL,
		json    TEXT NOT NULL,
		PRIMARY KEY (project, ref)
	)`)
	// attention is derived by SQLite from the item's own JSON, so every writer
	// of work_items (a sync, a one-item refresh) keeps it right, and CountAttention
	// answers from the index without decoding any item.
	store.Register("forgesync.work_items_attention", fmt.Sprintf(`ALTER TABLE work_items ADD COLUMN attention INTEGER
		GENERATED ALWAYS AS (CASE WHEN json_extract(json, '$.assigned') = 1 AND json_extract(json, '$.court') = %q
			AND json_extract(json, '$.stage') != %d THEN 1 ELSE 0 END) VIRTUAL`, "you", int(domain.StageDone)))
	store.Register("forgesync.work_items_attention_idx", `CREATE INDEX work_items_attention ON work_items (attention, project)`)
	store.Register("forgesync.sync_state", `CREATE TABLE sync_state (
		project TEXT PRIMARY KEY,
		json    TEXT NOT NULL
	)`)
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
		if !failed.StalledAt.IsZero() {
			r.StalledAt = failed.StalledAt
		}
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

// ItemRefs flattens the cached results into the work items workspaces look up
// worktrees against.
func ItemRefs(st *store.Store) ([]workspace.ItemRef, error) {
	results, err := Load(st)
	if err != nil {
		return nil, err
	}
	var refs []workspace.ItemRef
	for _, r := range results {
		for _, it := range r.Items {
			refs = append(refs, workspace.ItemRef{Project: r.Project, Forge: r.Forge, Item: it})
		}
	}
	return refs, nil
}
