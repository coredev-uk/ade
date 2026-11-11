// Package notify records every event in a durable inbox (cache.db) and fans it
// out to the toast, terminal and desktop channels according to config rules.
package notify

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/store"
)

func init() {
	store.Register(`CREATE TABLE inbox (
		id       TEXT PRIMARY KEY,
		time     INTEGER NOT NULL,
		kind     TEXT NOT NULL,
		severity TEXT NOT NULL,
		project  TEXT NOT NULL DEFAULT '',
		subject  TEXT NOT NULL DEFAULT '',
		title    TEXT NOT NULL,
		detail   TEXT NOT NULL DEFAULT '',
		link     TEXT NOT NULL DEFAULT '',
		read     INTEGER NOT NULL DEFAULT 0
	)`)
	store.Register(`CREATE INDEX inbox_unread_time ON inbox (read, time DESC)`)
}

// Item is an inbox row.
type Item struct {
	events.Event
	Read bool
}

// ListOpts filters List. Zero values match everything.
type ListOpts struct {
	UnreadOnly bool
	Limit      int // newest N; 0 = all
	Project    string
}

// Inbox is the durable event list shared by every ade process on this cache.
type Inbox struct{ db *sql.DB }

// NewInbox wraps an open store's database.
func NewInbox(s *store.Store) *Inbox { return &Inbox{db: s.DB} }

const cols = `id, time, kind, severity, project, subject, title, detail, link, read`

// Record stores e unread; recording the same ID twice is a no-op.
func (in *Inbox) Record(e events.Event) error {
	_, err := in.db.Exec(`INSERT OR IGNORE INTO inbox (`+cols+`) VALUES (?,?,?,?,?,?,?,?,?,0)`,
		e.ID, e.Time.UnixNano(), e.Kind, e.Severity.String(), e.Project, e.Subject, e.Title, e.Detail, e.Link)
	if err != nil {
		return fmt.Errorf("record inbox event %s: %w", e.ID, err)
	}
	return nil
}

// List returns items newest first.
func (in *Inbox) List(o ListOpts) ([]Item, error) {
	var where []string
	var args []any
	if o.UnreadOnly {
		where = append(where, "read = 0")
	}
	if o.Project != "" {
		where = append(where, "project = ?")
		args = append(args, o.Project)
	}
	q := `SELECT ` + cols + ` FROM inbox` + whereClause(where) + ` ORDER BY time DESC, id DESC`
	if o.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", o.Limit)
	}
	return in.query(q, args...)
}

// After returns items with an ID greater than cursor, oldest first. An empty
// cursor returns everything. IDs are ULIDs, so this is "newer than".
func (in *Inbox) After(cursor string) ([]Item, error) {
	return in.query(`SELECT `+cols+` FROM inbox WHERE id > ? ORDER BY id ASC`, cursor)
}

// Get returns one item, or sql.ErrNoRows-wrapped error when absent. A unique
// ID prefix is accepted so the CLI can show short IDs.
func (in *Inbox) Get(id string) (Item, error) {
	items, err := in.query(`SELECT `+cols+` FROM inbox WHERE id = ? OR id LIKE ? ESCAPE '\' LIMIT 2`, id, escapeLike(id)+"%")
	if err != nil {
		return Item{}, err
	}
	for _, it := range items {
		if it.ID == id {
			return it, nil
		}
	}
	switch len(items) {
	case 0:
		return Item{}, fmt.Errorf("inbox item %q: %w", id, sql.ErrNoRows)
	case 1:
		return items[0], nil
	}
	return Item{}, fmt.Errorf("inbox id prefix %q is ambiguous", id)
}

// MarkRead marks the given IDs read and reports how many rows changed.
func (in *Inbox) MarkRead(ids ...string) (int, error) {
	n := 0
	for _, id := range ids {
		res, err := in.db.Exec(`UPDATE inbox SET read = 1 WHERE id = ? AND read = 0`, id)
		if err != nil {
			return n, fmt.Errorf("mark %s read: %w", id, err)
		}
		c, _ := res.RowsAffected()
		n += int(c)
	}
	return n, nil
}

// MarkAllRead marks every unread item read.
func (in *Inbox) MarkAllRead() (int, error) {
	res, err := in.db.Exec(`UPDATE inbox SET read = 1 WHERE read = 0`)
	if err != nil {
		return 0, fmt.Errorf("mark all read: %w", err)
	}
	c, _ := res.RowsAffected()
	return int(c), nil
}

// UnreadCount counts unread items.
func (in *Inbox) UnreadCount() (int, error) {
	var n int
	err := in.db.QueryRow(`SELECT COUNT(*) FROM inbox WHERE read = 0`).Scan(&n)
	return n, err
}

func (in *Inbox) query(q string, args ...any) ([]Item, error) {
	rows, err := in.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query inbox: %w", err)
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var ns int64
		var sev string
		var read int
		if err := rows.Scan(&it.ID, &ns, &it.Kind, &sev, &it.Project, &it.Subject, &it.Title, &it.Detail, &it.Link, &read); err != nil {
			return nil, err
		}
		it.Time, it.Severity, it.Read = time.Unix(0, ns), events.ParseSeverity(sev), read != 0
		out = append(out, it)
	}
	return out, rows.Err()
}

func whereClause(w []string) string {
	if len(w) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w, " AND ")
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
