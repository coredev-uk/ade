package agent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/store"
)

func init() {
	store.Register("agent.runs", `CREATE TABLE agent_runs (
		id      TEXT PRIMARY KEY,
		item    TEXT NOT NULL,
		state   TEXT NOT NULL,
		updated TEXT NOT NULL,
		body    TEXT NOT NULL
	)`)
}

// Files of a run's session directory. Everything here stays in the item
// record: none of it is copied to cache.db or the audit log.
const (
	runsDir        = "runs"
	fileRun        = "run.json"
	fileRunMD      = "run.md"
	FileTranscript = "transcript.jsonl"
	FilePlan       = "plan.md"
	FileDiff       = "diff.md"
	FileCost       = "cost.json"
	fileStderr     = "stderr.log"
)

// Store persists runs: run.json in the session directory is the durable
// record, cache.db is the index the TUI lists from (rebuilt from the
// directories when it is lost).
type Store struct {
	db  *sql.DB
	rec *records.Store
	dir string // the records dir rec is rooted at
	now func() time.Time
}

// NewStore wires the index to cache.db and the directories to rec.
func NewStore(st *store.Store, rec *records.Store, recordsDir string) *Store {
	return &Store{db: st.DB, rec: rec, dir: recordsDir, now: time.Now}
}

// Dir is the run's session directory in its item record.
func (s *Store) Dir(r Run) string {
	return filepath.Join(s.rec.ItemDir(r.Forge, r.Tracker), runsDir, r.ID)
}

// Save writes run.json and run.md, then the index row. The files come first:
// the index is disposable.
func (s *Store) Save(r Run) error {
	dir := s.Dir(r)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, fileRun), append(body, '\n')); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, fileRunMD), []byte(renderRunMD(r))); err != nil {
		return err
	}
	return s.index(r, body)
}

func (s *Store) index(r Run, body []byte) error {
	_, err := s.db.Exec(`INSERT INTO agent_runs (id, item, state, updated, body) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET item = excluded.item, state = excluded.state, updated = excluded.updated, body = excluded.body`,
		r.ID, r.Item(), string(r.State), r.Updated.UTC().Format(time.RFC3339Nano), string(body))
	return err
}

func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Get returns the run an id, unique prefix or unique suffix names.
func (s *Store) Get(id string) (Run, error) {
	all, err := s.List()
	if err != nil {
		return Run{}, err
	}
	ids := make([]string, len(all))
	for i, r := range all {
		ids[i] = r.ID
	}
	full, err := matchID(ids, id)
	if err != nil {
		return Run{}, err
	}
	for _, r := range all {
		if r.ID == full {
			return r, nil
		}
	}
	return Run{}, fmt.Errorf("run %s vanished", full)
}

// exact reads one run by its full id.
func (s *Store) exact(id string) (Run, error) {
	var body string
	if err := s.db.QueryRow(`SELECT body FROM agent_runs WHERE id = ?`, id).Scan(&body); err != nil {
		return Run{}, err
	}
	var r Run
	return r, json.Unmarshal([]byte(body), &r)
}

// List returns every run, newest first. An empty index is rebuilt from the
// session directories first.
func (s *Store) List() ([]Run, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_runs`).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		if _, err := s.Rebuild(); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.Query(`SELECT body FROM agent_runs ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var r Run
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			continue // a row from a newer schema: skip, never fail the list
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ForItem lists the runs of one tracker issue, newest first.
func (s *Store) ForItem(ref forge.Ref) ([]Run, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []Run
	for _, r := range all {
		if r.Tracker == ref {
			out = append(out, r)
		}
	}
	return out, nil
}

// Rebuild re-indexes every run.json under the records items tree.
func (s *Store) Rebuild() (int, error) {
	root := filepath.Join(s.dir, "items")
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || d.Name() != fileRun || filepath.Base(filepath.Dir(filepath.Dir(path))) != runsDir {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var r Run
		if json.Unmarshal(body, &r) != nil || r.ID == "" {
			return nil
		}
		n++
		return s.index(r, body)
	})
	return n, err
}

// CostFile is cost.json: what each call cost and the sum against the cap.
type CostFile struct {
	Run      string  `json:"run"`
	CapUSD   float64 `json:"cap_usd"`
	TotalUSD float64 `json:"total_usd"`
	// ByKind sums the calls of each kind (plan, run, address).
	ByKind map[Kind]float64 `json:"by_kind"`
	Calls  []Call           `json:"calls"`
}

func (s *Store) writeCost(r Run) error {
	c := CostFile{Run: r.ID, CapUSD: r.CapUSD, TotalUSD: r.CostUSD, ByKind: map[Kind]float64{}, Calls: r.Calls}
	for _, call := range r.Calls {
		c.ByKind[call.Kind] += call.CostUSD
	}
	body, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(s.Dir(r), FileCost), append(body, '\n'))
}

func renderRunMD(r Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Run %s\n\n", r.ID)
	fmt.Fprintf(&b, "- item: %s\n- kind: %s\n- state: %s\n- driver: %s (%s)\n", r.Item(), r.Kind, r.State, r.Driver, r.Model)
	fmt.Fprintf(&b, "- worktree: %s\n- branch: %s\n", r.Worktree, r.Branch)
	if r.AIBranch != "" {
		fmt.Fprintf(&b, "- -ai worktree: %s\n- -ai branch: %s (accept or discard it)\n", r.AIWorktree, r.AIBranch)
	}
	if r.MR != nil {
		fmt.Fprintf(&b, "- merge request: %s\n", r.MR)
	}
	fmt.Fprintf(&b, "- cost: $%.4f of $%.2f cap\n", r.CostUSD, r.CapUSD)
	if len(r.Commits) > 0 {
		fmt.Fprintf(&b, "- commits: %s\n", strings.Join(r.Commits, ", "))
	}
	if r.Error != "" {
		fmt.Fprintf(&b, "- error: %s\n", r.Error)
	}
	if len(r.Calls) > 0 {
		b.WriteString("\n| call | exit | cost | turns |\n|---|---|---|---|\n")
		for _, c := range r.Calls {
			fmt.Fprintf(&b, "| %s | %d | $%.4f | %d |\n", c.Kind, c.Exit, c.CostUSD, c.Turns)
		}
	}
	return b.String()
}
