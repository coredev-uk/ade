// Package ports hands out TCP ports to run-graph instances. An allocation is
// keyed by instance (node and tree), so a worktree keeps its port across
// restarts, and two instances never share one (PLAN 2.1).
package ports

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/store"
)

func init() {
	store.Register("ports.allocations", `CREATE TABLE port_allocations (
		key  TEXT PRIMARY KEY,
		port INTEGER NOT NULL UNIQUE
	)`)
}

// maxAttempts bounds the search for a free port that no other key owns.
const maxAttempts = 64

// Free asks the OS for a currently free port on the loopback interface.
func Free() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// Available reports whether nothing listens on port (loopback and all interfaces).
func Available(port int) bool {
	for _, host := range []string{"127.0.0.1", ""} {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			return false
		}
		ln.Close()
	}
	return true
}

// Registry persists allocations in cache.db.
type Registry struct{ db *sql.DB }

// NewRegistry returns a registry over the cache.
func NewRegistry(s *store.Store) *Registry { return &Registry{db: s.DB} }

// Assign returns the port for key. The stored port is kept while it is usable:
// when held is true the caller's own live instance is bound to it, otherwise
// the port must be free right now. An unusable or missing allocation is
// replaced by a fresh free port that no other key owns.
func (r *Registry) Assign(key string, held bool) (int, error) {
	var port int
	err := r.db.QueryRow(`SELECT port FROM port_allocations WHERE key = ?`, key).Scan(&port)
	switch {
	case err == nil:
		if held || Available(port) {
			return port, nil
		}
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}
	for range maxAttempts {
		p, err := Free()
		if err != nil {
			return 0, err
		}
		if err := r.store(key, p); err != nil {
			if isUnique(err) { // another key owns p: it is merely idle, pick again
				continue
			}
			return 0, err
		}
		return p, nil
	}
	return 0, fmt.Errorf("no free port found for %s after %d attempts", key, maxAttempts)
}

func (r *Registry) store(key string, port int) error {
	_, err := r.db.Exec(`INSERT INTO port_allocations (key, port) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET port = excluded.port`, key, port)
	return err
}

// Lookup returns the stored port for key without allocating.
func (r *Registry) Lookup(key string) (port int, ok bool, err error) {
	err = r.db.QueryRow(`SELECT port FROM port_allocations WHERE key = ?`, key).Scan(&port)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return port, err == nil, err
}

// isUnique recognises SQLite's UNIQUE constraint failure without importing the driver.
func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
