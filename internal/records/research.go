package records

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// ResearchDir is the folder of an item record that holds the state of its
// research job. The answer itself is an AI artifact (AIDir) like any other,
// which is what keeps it from being bought twice.
const ResearchDir = "research"

// ResearchFile is the job state inside ResearchDir; ResearchLog receives the
// detached worker's stderr.
const (
	ResearchFile = "job.json"
	ResearchLog  = "worker.log"
)

// Research job states.
const (
	ResearchRunning = "running"
	ResearchDone    = "done"
	ResearchFailed  = "failed"
)

// Research is the latest research job of one issue: a single file, replaced
// by the next job. It says where the job stands and, once done, which
// artifact holds the answer.
type Research struct {
	ID      string    `json:"id"`
	Ref     string    `json:"ref"`
	State   string    `json:"state"`
	PID     int       `json:"pid,omitempty"`
	Started time.Time `json:"started"`
	// Finished is zero while the job runs.
	Finished time.Time `json:"finished,omitzero"`
	Profile  string    `json:"profile,omitempty"`
	Model    string    `json:"model,omitempty"`
	CostUSD  float64   `json:"cost_usd,omitempty"`
	// CacheHit is true when the answer was already bought: the job cost nothing.
	CacheHit bool `json:"cache_hit,omitempty"`
	// Artifact is the answer's path, relative to the data dir.
	Artifact string `json:"artifact,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ResearchPath is the item's job state file.
func (s *Store) ResearchPath(forgeName string, ref forge.Ref) string {
	return filepath.Join(s.ItemDir(forgeName, ref), ResearchDir, ResearchFile)
}

// ResearchLogPath is where the detached worker writes what it prints.
func (s *Store) ResearchLogPath(forgeName string, ref forge.Ref) string {
	return filepath.Join(s.ItemDir(forgeName, ref), ResearchDir, ResearchLog)
}

// ErrNoResearch means the item has no research job yet.
var ErrNoResearch = errors.New("no research job for this item yet")

// ReadResearch loads the item's latest research job; ErrNoResearch when there
// is none.
func (s *Store) ReadResearch(forgeName string, ref forge.Ref) (Research, error) {
	path := s.ResearchPath(forgeName, ref)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Research{}, ErrNoResearch
	}
	if err != nil {
		return Research{}, fmt.Errorf("read %s: %w", path, err)
	}
	var r Research
	if err := json.Unmarshal(data, &r); err != nil {
		return Research{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// WriteResearch saves the job state atomically, so a reader (the dashboard,
// `ade item`) never sees half a file while the worker updates it.
func (s *Store) WriteResearch(forgeName string, ref forge.Ref, r Research) error {
	path := s.ResearchPath(forgeName, ref)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}
