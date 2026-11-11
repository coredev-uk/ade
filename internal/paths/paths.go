// Package paths resolves ADE's on-disk locations (XDG base directories).
package paths

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Paths are the directories ADE reads and writes. Root is resolved later from
// config (paths.root); everything else is fixed by XDG conventions.
type Paths struct {
	ConfigFile string // $ADE_CONFIG or $XDG_CONFIG_HOME/ade/config.toml
	DataDir    string // $XDG_DATA_HOME/ade: audit log, item records (durable)
	CacheDir   string // $XDG_CACHE_HOME/ade: cache.db (disposable)
}

// Default resolves Paths from the environment.
func Default() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	xdg := func(env, fallback string) string {
		if v := os.Getenv(env); v != "" && filepath.IsAbs(v) {
			return v
		}
		return filepath.Join(home, fallback)
	}
	p := Paths{
		ConfigFile: filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "ade", "config.toml"),
		DataDir:    filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "ade"),
		CacheDir:   filepath.Join(xdg("XDG_CACHE_HOME", ".cache"), "ade"),
	}
	if v := os.Getenv("ADE_CONFIG"); v != "" {
		p.ConfigFile = v
	}
	return p, nil
}

// Expand resolves a leading "~/" against the user's home directory.
func Expand(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
	}
	if strings.HasPrefix(p, "~") {
		return "", errors.New("~user paths are not supported: " + p)
	}
	return p, nil
}

// EnsureDir creates dir (and parents) with owner-only permissions.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}
