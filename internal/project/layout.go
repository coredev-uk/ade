// Package project manages ADE projects on disk: fetching manifests, cloning
// repos as bare clones plus worktrees, and keeping them up to date (PLAN 2.2, 2.3).
package project

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/coredev-uk/ade/internal/config"
)

const snapshotFile = ".project.toml"

// snapshot is <root>/<project>/.project.toml: the last good manifest plus where
// it came from, so a failing source never leaves ADE without a manifest.
type snapshot struct {
	Source     string    `toml:"source"`
	FetchedAt  time.Time `toml:"fetched_at"`
	LastUpdate time.Time `toml:"last_update"`
	Manifest   string    `toml:"manifest"`
}

func (sn *snapshot) parse() (*config.Manifest, error) {
	return config.ParseManifest([]byte(sn.Manifest), sn.Source)
}

func readSnapshot(dir string) (*snapshot, error) {
	data, err := os.ReadFile(filepath.Join(dir, snapshotFile))
	if err != nil {
		return nil, err
	}
	var sn snapshot
	if _, err := toml.Decode(string(data), &sn); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, snapshotFile), err)
	}
	return &sn, nil
}

// LoadManifest reads the last good manifest snapshot of the project cloned
// into projectDir (<root>/<project>).
func LoadManifest(projectDir string) (*config.Manifest, error) {
	sn, err := readSnapshot(projectDir)
	if err != nil {
		return nil, err
	}
	return sn.parse()
}

// writeSnapshot replaces the snapshot atomically so readers never see a torn file.
func writeSnapshot(dir string, sn snapshot) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, snapshotFile+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after a successful rename
	if err := toml.NewEncoder(tmp).Encode(sn); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, snapshotFile))
}

// snapshots returns every project snapshot under root keyed by project directory name.
func snapshots(root string) (map[string]*snapshot, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]*snapshot{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sn, err := readSnapshot(filepath.Join(root, e.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[e.Name()] = sn
	}
	return out, nil
}

// Cloned returns the names of the projects cloned under root, sorted.
func Cloned(root string) ([]string, error) {
	all, err := snapshots(root)
	if err != nil {
		return nil, err
	}
	return slices.Sorted(maps.Keys(all)), nil
}

// findSnapshot locates the project that was cloned from source.
func findSnapshot(root, source string) (name string, sn *snapshot, err error) {
	all, err := snapshots(root)
	if err != nil {
		return "", nil, err
	}
	for n, s := range all {
		if s.Source == source {
			return n, s, nil
		}
	}
	return "", nil, nil
}

// validSegment guards names that become directory names under the root.
func validSegment(s string) bool {
	return s != "" && !strings.HasPrefix(s, ".") && !strings.ContainsAny(s, `/\`)
}

// validate runs the full manifest validation plus the one guard it does not
// cover: repo names become directory names under the project root.
func validate(m *config.Manifest, cfg *config.Config) error {
	var msgs []string
	for _, is := range m.Validate(cfg) {
		if is.Severity == config.SeverityError {
			msgs = append(msgs, is.Path+": "+is.Msg)
		}
	}
	for _, name := range m.RepoNames() {
		if !validSegment(name) {
			msgs = append(msgs, fmt.Sprintf("repos.%s: must be a plain directory name", name))
		}
	}
	if len(msgs) > 0 {
		return fmt.Errorf("invalid manifest:\n  %s", strings.Join(msgs, "\n  "))
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
