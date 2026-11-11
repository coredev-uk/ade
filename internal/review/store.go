package review

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/store"
)

func init() {
	store.Register("review.drafts", `CREATE TABLE review_drafts (
		mr         TEXT NOT NULL,
		id         TEXT NOT NULL,
		path       TEXT NOT NULL,
		old_path   TEXT NOT NULL,
		line       INTEGER NOT NULL,
		side       TEXT NOT NULL,
		body       TEXT NOT NULL,
		severity   TEXT NOT NULL,
		source     TEXT NOT NULL,
		state      TEXT NOT NULL,
		remote_id  INTEGER NOT NULL,
		published  INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (mr, id)
	)`)
}

// Store keeps unsubmitted drafts in cache.db, keyed by MR, so quitting the
// TUI (or running `ade review add` in another process) never loses a
// half-written review. Rows keep insertion order.
type Store struct{ db *sql.DB }

// NewStore wraps an open cache.
func NewStore(st *store.Store) *Store { return &Store{db: st.DB} }

const draftColumns = `id, path, old_path, line, side, body, severity, source, state, remote_id, published, created_at`

// List returns the MR's drafts oldest first.
func (s *Store) List(mr forge.Ref) ([]Draft, error) {
	rows, err := s.db.Query(`SELECT `+draftColumns+` FROM review_drafts WHERE mr = ? ORDER BY rowid`, mr.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Draft
	for rows.Next() {
		var (
			d       Draft
			created string
			pub     int
		)
		if err := rows.Scan(&d.ID, &d.Path, &d.OldPath, &d.Line, &d.Side, &d.Body, &d.Severity, &d.Source, &d.State,
			&d.RemoteID, &pub, &created); err != nil {
			return nil, err
		}
		d.Published = pub != 0
		if d.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("review draft %s: %w", d.ID, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Put inserts or replaces one draft (matched by id), keeping its place in the
// list. The draft should already be normalised.
func (s *Store) Put(mr forge.Ref, d Draft) error {
	return put(s.db, mr, d)
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func put(db execer, mr forge.Ref, d Draft) error {
	pub := 0
	if d.Published {
		pub = 1
	}
	_, err := db.Exec(`INSERT INTO review_drafts (mr, `+draftColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (mr, id) DO UPDATE SET path = excluded.path, old_path = excluded.old_path, line = excluded.line,
			side = excluded.side, body = excluded.body, severity = excluded.severity, source = excluded.source,
			state = excluded.state, remote_id = excluded.remote_id, published = excluded.published`,
		mr.String(), d.ID, d.Path, d.OldPath, d.Line, d.Side, d.Body, d.Severity, d.Source, d.State, d.RemoteID, pub,
		d.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

// Save makes the stored drafts of mr exactly ds, atomically.
func (s *Store) Save(mr forge.Ref, ds []Draft) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(`DELETE FROM review_drafts WHERE mr = ?`, mr.String()); err != nil {
		return err
	}
	for _, d := range ds {
		if err := put(tx, mr, d); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Delete removes one draft; a missing id is not an error.
func (s *Store) Delete(mr forge.Ref, id string) error {
	_, err := s.db.Exec(`DELETE FROM review_drafts WHERE mr = ? AND id = ?`, mr.String(), id)
	return err
}

// MRs lists the merge requests that have saved drafts, for "review in progress"
// markers.
func (s *Store) MRs() ([]forge.Ref, error) {
	rows, err := s.db.Query(`SELECT mr FROM review_drafts GROUP BY mr ORDER BY min(rowid)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []forge.Ref
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		ref, err := forge.ParseRef(s)
		if err != nil {
			continue // a hand-edited row; skip rather than hide the rest
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}
