// Package app wires the pieces every ade command needs: paths, config, audit
// log, event bus, and the cache.
package app

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/store"
)

type App struct {
	Paths      paths.Paths
	Config     *config.Config
	Root       string // expanded paths.root: <root>/<project>/<repo>/...
	RecordsDir string // expanded records dir (audit log, item records)
	Audit      *audit.Log
	Bus        *events.Bus

	cacheOnce sync.Once
	cache     *store.Store
	cacheErr  error
}

// Load resolves paths, reads the user config, and opens the audit log.
// actor is the default audit actor ("user" for interactive commands).
func Load(actor string) (*App, error) {
	p, err := paths.Default()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(p.ConfigFile)
	if err != nil {
		return nil, err
	}
	root, err := cfg.RootDir()
	if err != nil {
		return nil, fmt.Errorf("paths.root: %w", err)
	}
	rec, err := cfg.RecordsDir(p.DataDir)
	if err != nil {
		return nil, fmt.Errorf("records.dir: %w", err)
	}
	if err := paths.EnsureDir(rec); err != nil {
		return nil, err
	}
	al, err := audit.Open(rec, actor)
	if err != nil {
		return nil, err
	}
	return &App{Paths: p, Config: cfg, Root: root, RecordsDir: rec, Audit: al, Bus: events.NewBus()}, nil
}

// Cache opens cache.db once per process.
func (a *App) Cache() (*store.Store, error) {
	a.cacheOnce.Do(func() {
		a.cache, a.cacheErr = store.Open(filepath.Join(a.Paths.CacheDir, "cache.db"))
	})
	return a.cache, a.cacheErr
}

// Close releases the cache if it was opened.
func (a *App) Close() error {
	if a.cache != nil {
		return a.cache.Close()
	}
	return nil
}
