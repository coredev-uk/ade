package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/devshell"
	"github.com/coredev-uk/ade/internal/ports"
	"github.com/coredev-uk/ade/internal/store"
)

func init() {
	store.Register("workspace.instances", `CREATE TABLE ws_instances (
		id         TEXT PRIMARY KEY,
		node       TEXT NOT NULL,
		tree       TEXT NOT NULL,
		project    TEXT NOT NULL,
		kind       TEXT NOT NULL,
		port       INTEGER NOT NULL DEFAULT 0,
		pid        INTEGER NOT NULL DEFAULT 0,
		pstart     INTEGER NOT NULL DEFAULT 0,
		state      TEXT NOT NULL,
		keep       INTEGER NOT NULL DEFAULT 0,
		log        TEXT NOT NULL,
		started_at INTEGER NOT NULL
	)`)
	// The instance ids a row requires, as a JSON array: Status marks a row
	// blocked once one of them is gone.
	store.Register("workspace.instances.requires", `ALTER TABLE ws_instances ADD COLUMN requires TEXT NOT NULL DEFAULT ''`)
	store.Register("workspace.holds", `CREATE TABLE ws_holds (
		instance TEXT NOT NULL REFERENCES ws_instances(id) ON DELETE CASCADE,
		holder   TEXT NOT NULL,
		pid      INTEGER NOT NULL DEFAULT 0,
		pstart   INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (instance, holder)
	)`)
}

// Instance states.
const (
	StateStarting = "starting"
	StateReady    = "ready"
	StateDone     = "done" // a oneshot that exited 0
	// StateBlocked is only ever reported by Status, never stored: the process
	// runs, but something it requires no longer does.
	StateBlocked = "blocked"
)

// Manager owns running instances: it plans, starts, shares and stops them.
// State lives in cache.db, so separate `ade` processes (a TUI, `ade ws up`,
// a VS Code task) see and reuse each other's instances.
type Manager struct {
	StateDir string // {state}: durable per-instance data
	LogDir   string // output logs and instance locks
	// Grace is how long a stopping instance gets after SIGTERM before SIGKILL.
	Grace time.Duration
	// Devshell finds each tree's dev shell (direnv, nix develop); the zero
	// value looks at PATH and the file system.
	Devshell devshell.Finder

	db    *sql.DB
	ports *ports.Registry
}

// NewManager opens the manager of an app: state under the data dir, logs under the cache dir.
func NewManager(a *app.App) (*Manager, error) {
	st, err := a.Cache()
	if err != nil {
		return nil, err
	}
	return NewManagerAt(st, filepath.Join(a.Paths.DataDir, "state"), filepath.Join(a.Paths.CacheDir, "ws")), nil
}

// NewManagerAt builds a manager over an open cache with explicit directories.
func NewManagerAt(st *store.Store, stateDir, logDir string) *Manager {
	return &Manager{StateDir: stateDir, LogDir: logDir, Grace: 5 * time.Second, db: st.DB, ports: ports.NewRegistry(st)}
}

// Row is a persisted instance.
type Row struct {
	ID        string    `json:"id"`
	Node      string    `json:"node"`
	Tree      string    `json:"tree"`
	Project   string    `json:"project"`
	Kind      string    `json:"kind"`
	Port      int       `json:"port,omitempty"`
	PID       int       `json:"pid,omitempty"`
	State     string    `json:"state"`
	Keep      bool      `json:"keep,omitempty"`
	Log       string    `json:"log"`
	StartedAt time.Time `json:"started_at"`
	Holders   []string  `json:"holders,omitempty"`
	Alive     bool      `json:"alive"`
	// Requires are the instance ids this one was started against.
	Requires []string `json:"requires,omitempty"`
	// BlockedBy names the node whose death left this instance blocked (State is "blocked").
	BlockedBy string `json:"blocked_by,omitempty"`

	pstart uint64
}

const rowCols = `id, node, tree, project, kind, port, pid, pstart, state, keep, log, started_at, requires`

func scanRow(sc interface{ Scan(...any) error }) (Row, error) {
	var (
		r       Row
		keep    int
		started int64
		pstart  int64
		reqs    string
	)
	if err := sc.Scan(&r.ID, &r.Node, &r.Tree, &r.Project, &r.Kind, &r.Port, &r.PID, &pstart, &r.State, &keep, &r.Log, &started, &reqs); err != nil {
		return Row{}, err
	}
	if reqs != "" {
		if err := json.Unmarshal([]byte(reqs), &r.Requires); err != nil {
			return Row{}, fmt.Errorf("instance %s: requires: %w", r.ID, err)
		}
	}
	r.pstart, r.Keep, r.StartedAt = uint64(pstart), keep != 0, time.Unix(0, started)
	r.Alive = r.liveProcess()
	return r, nil
}

// liveProcess: a service is alive while its process is; a finished oneshot has none.
func (r Row) liveProcess() bool {
	if r.Kind == "oneshot" {
		return r.State == StateDone
	}
	return processAlive(r.PID, r.pstart)
}

func (m *Manager) get(id string) (*Row, error) {
	r, err := scanRow(m.db.QueryRow(`SELECT `+rowCols+` FROM ws_instances WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// live reports whether instance id has a running process.
func (m *Manager) live(id string) bool {
	r, err := m.get(id)
	return err == nil && r != nil && r.Alive && r.Kind != "oneshot"
}

func (m *Manager) put(r Row) error {
	reqs, err := json.Marshal(r.Requires)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(`INSERT INTO ws_instances (`+rowCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET port=excluded.port, pid=excluded.pid, pstart=excluded.pstart,
		  state=excluded.state, keep=excluded.keep, log=excluded.log, started_at=excluded.started_at,
		  requires=excluded.requires`,
		r.ID, r.Node, r.Tree, r.Project, r.Kind, r.Port, r.PID, int64(r.pstart), r.State, boolInt(r.Keep), r.Log, r.StartedAt.UnixNano(), string(reqs))
	return err
}

func (m *Manager) setState(id, state string) error {
	_, err := m.db.Exec(`UPDATE ws_instances SET state = ? WHERE id = ?`, state, id)
	return err
}

func (m *Manager) drop(id string) error {
	_, err := m.db.Exec(`DELETE FROM ws_instances WHERE id = ?`, id)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// hold records that holder keeps instance id running. holderPID is the
// process whose death ends the hold (0: until an explicit Release).
func (m *Manager) hold(id, holder string, holderPID int) error {
	var pstart uint64
	if holderPID > 0 {
		pstart = processStart(holderPID)
	}
	_, err := m.db.Exec(`INSERT INTO ws_holds (instance, holder, pid, pstart) VALUES (?,?,?,?)
		ON CONFLICT(instance, holder) DO UPDATE SET pid = excluded.pid, pstart = excluded.pstart`,
		id, holder, holderPID, int64(pstart))
	return err
}

func (m *Manager) holders(id string) ([]string, error) {
	rows, err := m.db.Query(`SELECT holder FROM ws_holds WHERE instance = ? ORDER BY holder`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// pruneHolds forgets holds whose owning process has died without releasing.
func (m *Manager) pruneHolds() error {
	rows, err := m.db.Query(`SELECT instance, holder, pid, pstart FROM ws_holds WHERE pid > 0`)
	if err != nil {
		return err
	}
	type key struct{ inst, holder string }
	var dead []key
	for rows.Next() {
		var (
			k      key
			pid    int
			pstart int64
		)
		if err := rows.Scan(&k.inst, &k.holder, &pid, &pstart); err != nil {
			rows.Close()
			return err
		}
		if !processAlive(pid, uint64(pstart)) {
			dead = append(dead, k)
		}
	}
	rows.Close()
	for _, k := range dead {
		if _, err := m.db.Exec(`DELETE FROM ws_holds WHERE instance = ? AND holder = ?`, k.inst, k.holder); err != nil {
			return err
		}
	}
	return nil
}

// Status lists every known instance with its holders, after dropping holds of
// processes that died without releasing. Alive says whether the instance's
// own process still runs.
func (m *Manager) Status(ctx context.Context) ([]Row, error) {
	if err := m.pruneHolds(); err != nil {
		return nil, err
	}
	rows, err := m.db.QueryContext(ctx, `SELECT `+rowCols+` FROM ws_instances ORDER BY started_at, id`)
	if err != nil {
		return nil, err
	}
	var out []Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	for i := range out {
		if out[i].Holders, err = m.holders(out[i].ID); err != nil {
			return nil, err
		}
	}
	markBlocked(out)
	return out, nil
}

// markBlocked reports a running service whose requirement is gone (killed, or
// crashed) as blocked by that requirement. A requirement that is itself
// blocked passes the blame on to the node that actually died.
func markBlocked(rows []Row) {
	byID := make(map[string]*Row, len(rows))
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}
	var cause func(r *Row) string // the node that blocks r, "" when none does
	cause = func(r *Row) string {
		for _, id := range r.Requires {
			req := byID[id]
			if req == nil { // dropped from the table: nothing serves it any more
				node, _, _ := strings.Cut(id, "@")
				return node
			}
			if !req.Alive {
				return req.Node
			}
			if req.Kind != "oneshot" && req.State == StateReady {
				if c := cause(req); c != "" {
					return c
				}
			}
		}
		return ""
	}
	for i := range rows {
		r := &rows[i]
		if r.Kind == "oneshot" || !r.Alive || r.State != StateReady {
			continue
		}
		if c := cause(r); c != "" {
			r.State, r.BlockedBy = StateBlocked, c
		}
	}
}

// lockPath is the flock file serialising start and stop of one instance.
func (m *Manager) lockPath(id string) string {
	return filepath.Join(m.LogDir, "locks", slugOf(id)+".lock")
}

// lock takes the cross-process lock of instance id.
func (m *Manager) lock(ctx context.Context, id string) (unlock func(), err error) {
	path := m.lockPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
				f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", id, err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// Running reports whether instance id has a live process.
func (m *Manager) Running(id string) bool { return m.live(id) }

// WatchBlocked polls Status every interval until ctx ends and calls onBlocked
// once each time one of the instances ids turns blocked (again, after it has
// recovered). It is how a foreground workspace notices that something it
// depends on died.
func (m *Manager) WatchBlocked(ctx context.Context, ids []string, every time.Duration, onBlocked func(Row)) {
	reported := map[string]bool{}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rows, err := m.Status(ctx)
		if err != nil {
			continue
		}
		for _, r := range rows {
			if !slices.Contains(ids, r.ID) {
				continue
			}
			switch blocked := r.State == StateBlocked; {
			case blocked && !reported[r.ID]:
				reported[r.ID] = true
				onBlocked(r)
			case !blocked:
				delete(reported, r.ID)
			}
		}
	}
}
