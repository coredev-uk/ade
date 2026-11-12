package forgesync

import (
	"database/sql"
	"fmt"
	"time"
)

// Attention counts what waits on you in the cache: assigned items whose court
// is you and that are not done (Home's "waiting on you"), and open MRs that
// ask for your review.
type Attention struct {
	Items   int `json:"items"`
	Reviews int `json:"reviews"`
	Total   int `json:"total"`
	// SyncedAt is the newest last-good sync among the counted projects; zero
	// when none has run.
	SyncedAt time.Time `json:"synced_at,omitzero"`
}

// CountAttention reads Attention straight from the cache with SQL (the
// work_items.attention index), so a prompt decodes no item. project limits it
// to one ADE project ("" counts all). A cache without the tables or the column
// (never synced, or not yet migrated by a newer ade) is an error the caller can
// treat as "no data".
func CountAttention(db *sql.DB, project string) (Attention, error) {
	var a Attention
	itemQ := `SELECT count(*) FROM work_items WHERE attention = 1`
	var args []any
	stateQ := `SELECT json_extract(json, '$.synced_at'), coalesce(json_array_length(json_extract(json, '$.reviews')), 0) FROM sync_state`
	var stateArgs []any
	if project != "" {
		itemQ += ` AND project = ?`
		args = append(args, project)
		stateQ += ` WHERE project = ?`
		stateArgs = append(stateArgs, project)
	}
	if err := db.QueryRow(itemQ, args...).Scan(&a.Items); err != nil {
		return Attention{}, fmt.Errorf("count items: %w", err)
	}
	rows, err := db.Query(stateQ, stateArgs...)
	if err != nil {
		return Attention{}, fmt.Errorf("count reviews: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var at sql.NullString
		var n int
		if err := rows.Scan(&at, &n); err != nil {
			return Attention{}, err
		}
		a.Reviews += n
		if t, err := time.Parse(time.RFC3339Nano, at.String); err == nil && t.After(a.SyncedAt) {
			a.SyncedAt = t
		}
	}
	if err := rows.Err(); err != nil {
		return Attention{}, err
	}
	a.Total = a.Items + a.Reviews
	return a, nil
}
