package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Local overrides (PLAN 2). Beside every config file or manifest `name.toml`
// ADE reads `name.local.toml` when it exists: per-machine paths and private
// extras that never leave the machine (gitignore them). The overlay is
// merged over the base before anything is decoded or validated:
//
//   - tables merge key by key, recursively: `[repos.api]` in the overlay
//     changes only the keys it names and keeps the rest of that repo;
//   - every other value replaces the base value outright: strings, numbers,
//     booleans, arrays (so `toast = ["ci.*"]` replaces the list rather than
//     extending it) and arrays of tables (`[[projects]]` as a whole);
//   - keys the overlay adds are added, and nothing in the overlay can remove
//     a base key (set it to its off value instead).
//
// Unknown keys in an overlay are reported like unknown keys anywhere else.

// LocalPath is where the overlay of the file at path lives: `x.toml` →
// `x.local.toml`, `x.ade.toml` → `x.ade.local.toml`, anything else gets
// `.local.toml` appended.
func LocalPath(path string) string {
	if base, ok := strings.CutSuffix(path, ".toml"); ok {
		return base + ".local.toml"
	}
	return path + ".local.toml"
}

// readOverlay returns the overlay of the file at path, nil when none exists.
func readOverlay(path string) ([]byte, error) {
	data, err := os.ReadFile(LocalPath(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// MergeTOML merges overlay over base with the semantics above and returns the
// merged document. Each document is parsed on its own first, so a syntax
// error names the file it is in (baseSource / overlaySource).
func MergeTOML(base []byte, baseSource string, overlay []byte, overlaySource string) ([]byte, error) {
	var b, o map[string]any
	if _, err := toml.Decode(string(base), &b); err != nil {
		return nil, fmt.Errorf("%s: %w", baseSource, err)
	}
	if _, err := toml.Decode(string(overlay), &o); err != nil {
		return nil, fmt.Errorf("%s: %w", overlaySource, err)
	}
	if b == nil {
		b = map[string]any{}
	}
	mergeTables(b, o)
	var out bytes.Buffer
	if err := toml.NewEncoder(&out).Encode(b); err != nil {
		return nil, fmt.Errorf("%s + %s: %w", baseSource, overlaySource, err)
	}
	return out.Bytes(), nil
}

func mergeTables(dst, src map[string]any) {
	for k, sv := range src {
		st, sok := sv.(map[string]any)
		dt, dok := dst[k].(map[string]any)
		if sok && dok {
			mergeTables(dt, st)
			continue
		}
		dst[k] = sv
	}
}

// ReadManifestFile reads the manifest at path with its local overlay applied.
func ReadManifestFile(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ov, err := readOverlay(path)
	if err != nil {
		return nil, err
	}
	if ov != nil {
		if data, err = MergeTOML(data, path, ov, LocalPath(path)); err != nil {
			return nil, err
		}
	}
	m, err := ParseManifest(data, path)
	if err != nil {
		return nil, err
	}
	if ov != nil {
		m.Overlay = LocalPath(filepath.Clean(path))
	}
	return m, nil
}
