package store

import (
	"path/filepath"
	"testing"
)

// Reopening applies nothing twice, and a migration registered later (a new
// package, or a different init order) is applied to an existing cache.
func TestMigrationsAreNamedAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	Register("test.late", `CREATE TABLE late (x INTEGER)`)
	t.Cleanup(func() { migrations = migrations[:len(migrations)-1] })
	for range 2 {
		s, err = Open(path)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if _, err := s.DB.Exec(`INSERT INTO late (x) VALUES (1)`); err != nil {
			t.Fatalf("late migration not applied: %v", err)
		}
		s.Close()
	}
}

// Caches from the old positional scheme are reset rather than misapplied.
func TestLegacyCacheIsReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE migrations`, `CREATE TABLE inbox_old (x)`, `PRAGMA user_version = 3`} {
		if _, err := s.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	defer s.Close()
	var n int
	s.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'inbox_old'`).Scan(&n) //nolint:errcheck
	if n != 0 {
		t.Error("legacy table kept")
	}
	if _, err := s.DB.Exec(`INSERT INTO kv (k, v) VALUES ('a', 'b')`); err != nil {
		t.Errorf("kv not recreated: %v", err)
	}
}
