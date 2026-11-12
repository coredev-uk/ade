// Package store owns cache.db: ADE's disposable SQLite cache (WAL mode, pure Go).
// Losing it costs a re-sync and read/unread state, never history.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite" // driver "sqlite"
)

type Store struct {
	DB   *sql.DB
	Path string
}

type migration struct{ name, stmt string }

var (
	regMu      sync.Mutex
	migrations []migration
)

func init() {
	Register("store.kv", `CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT NOT NULL)`)
}

// Register adds a named migration. Packages that own tables call it from
// init(). Migrations are tracked by name, so the order packages initialise
// in (import-path order) never matters across releases: each name is applied
// exactly once per database. Within one package, registration order is kept.
func Register(name, stmt string) {
	regMu.Lock()
	defer regMu.Unlock()
	for _, m := range migrations {
		if m.name == name {
			panic("store: duplicate migration " + name) // programming error at init
		}
	}
	migrations = append(migrations, migration{name, stmt})
}

// Open opens (creating if needed) the cache at path and applies migrations.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db, Path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenReadOnly opens an existing cache at path without creating it, applying
// migrations or taking the write lock: for commands that must start in
// milliseconds and may not disturb a running dashboard (the shell prompt). It
// returns an error wrapping fs.ErrNotExist when there is no cache yet; a
// table that does not exist yet fails at query time.
func OpenReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(250)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return &Store{DB: db, Path: path}, nil
}

func (s *Store) migrate() error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS migrations (name TEXT PRIMARY KEY)`); err != nil {
		return err
	}
	if err := s.resetLegacy(); err != nil {
		return err
	}
	regMu.Lock()
	ms := append([]migration(nil), migrations...)
	regMu.Unlock()
	for _, m := range ms {
		if err := s.apply(m); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) apply(m migration) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	var done int
	if err := tx.QueryRow(`SELECT count(*) FROM migrations WHERE name = ?`, m.name).Scan(&done); err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	if _, err := tx.Exec(m.stmt); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	if _, err := tx.Exec(`INSERT INTO migrations (name) VALUES (?)`, m.name); err != nil {
		return err
	}
	return tx.Commit()
}

// resetLegacy drops tables from caches created by the positional
// (user_version) scheme. The cache is disposable by design: a re-sync and the
// loss of read/unread state is the whole cost.
func (s *Store) resetLegacy() error {
	var v int
	if err := s.DB.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v == 0 {
		return nil
	}
	rows, err := s.DB.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT IN ('migrations') AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, n)
	}
	rows.Close()
	for _, t := range tables {
		if _, err := s.DB.Exec(`DROP TABLE "` + t + `"`); err != nil {
			return err
		}
	}
	_, err = s.DB.Exec(`PRAGMA user_version = 0`)
	return err
}

func (s *Store) Close() error { return s.DB.Close() }
