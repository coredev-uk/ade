package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coredev-uk/ade/internal/readiness"
)

// Outcome of one node in Up.
type Outcome string

const (
	OutcomeReady   Outcome = "ready"
	OutcomeFailed  Outcome = "failed"
	OutcomeBlocked Outcome = "blocked" // a requirement did not come up
)

// Result is what Up reports per planned instance.
type Result struct {
	ID      string  `json:"id"`
	Node    string  `json:"node"`
	Kind    string  `json:"kind"` // service | oneshot
	Tree    string  `json:"tree"`
	Port    int     `json:"port,omitempty"`
	PID     int     `json:"pid,omitempty"`
	Outcome Outcome `json:"outcome"`
	// Shared is true when an instance another workspace had already started was reused.
	Shared bool   `json:"shared,omitempty"`
	Ready  string `json:"ready,omitempty"` // the probe, e.g. "http http://localhost:54012/healthz"
	Detail string `json:"detail,omitempty"`
	Log    string `json:"log"`
}

// UpOptions says who keeps the instances alive.
type UpOptions struct {
	// Holder names the workspace (or task) keeping instances up; the same
	// holder releases them. Required.
	Holder string
	// HolderPID ends the hold automatically when that process dies. 0 keeps
	// the hold until Release (detached runs).
	HolderPID int
	// Progress, when set, is called as each node finishes. Calls are serialised.
	Progress func(Result)

	// restore are the holds a restarted instance had before it was stopped
	// (dropping its row dropped them); they replace the new hold of Holder.
	restore map[string][]holdRec
	// preset are instances a run leaves untouched; their result stands in for
	// theirs (Restart's untouched part of the plan).
	preset map[string]Result
}

// Up starts the plan in dependency order: independent nodes in parallel, a
// node only after every requirement is ready. A failed or timed-out
// requirement blocks its dependents, but unrelated nodes still start. The
// returned results are in plan order.
func (m *Manager) Up(ctx context.Context, p *Plan, o UpOptions) ([]Result, error) {
	if o.Holder == "" {
		return nil, errors.New("workspace: Up needs a holder")
	}
	if _, err := m.Reap(ctx); err != nil { // instances orphaned by crashed holders must not be reused
		return nil, err
	}
	return m.start(ctx, p, o)
}

// start brings the planned instances up in dependency order.
func (m *Manager) start(ctx context.Context, p *Plan, o UpOptions) ([]Result, error) {
	results := make([]Result, len(p.Instances))
	index := map[string]int{}
	done := make([]chan struct{}, len(p.Instances))
	for i, in := range p.Instances {
		index[in.ID] = i
		done[i] = make(chan struct{})
	}
	var progressMu sync.Mutex
	var wg sync.WaitGroup
	for i, in := range p.Instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done[i])
			for _, req := range in.Requires {
				select {
				case <-done[index[req]]:
				case <-ctx.Done():
				}
			}
			if r, ok := o.preset[in.ID]; ok {
				results[i] = r
				return
			}
			results[i] = m.bring(ctx, p, in, results, index, o)
			if o.Progress != nil {
				progressMu.Lock()
				o.Progress(results[i])
				progressMu.Unlock()
			}
		}()
	}
	wg.Wait()
	return results, ctx.Err()
}

// bring starts (or reuses) one instance, or reports why it cannot.
func (m *Manager) bring(ctx context.Context, p *Plan, in *Instance, results []Result, index map[string]int, o UpOptions) Result {
	res := Result{ID: in.ID, Node: in.Node.ID, Kind: kindOf(in.Node), Tree: in.Dir, Port: in.Port, Log: in.Log}
	if in.Ready != nil {
		res.Ready = in.Ready.String()
	}
	for _, req := range in.Requires {
		if r := results[index[req]]; r.Outcome != OutcomeReady {
			res.Outcome = OutcomeBlocked
			res.Detail = fmt.Sprintf("blocked: %s not ready (%s)", r.Node, r.Detail)
			return res
		}
	}
	if err := ctx.Err(); err != nil {
		res.Outcome, res.Detail = OutcomeFailed, err.Error()
		return res
	}
	unlock, err := m.lock(ctx, in.ID)
	if err != nil {
		res.Outcome, res.Detail = OutcomeFailed, err.Error()
		return res
	}
	defer unlock()
	pid, shared, err := m.ensure(ctx, p, in, o)
	res.PID, res.Shared = pid, shared
	if err != nil {
		res.Outcome, res.Detail = OutcomeFailed, err.Error()
		return res
	}
	res.Outcome = OutcomeReady
	return res
}

// ensure runs under the instance lock. It reuses a healthy instance or starts
// a new one, and records the hold before the lock is released so a concurrent
// Release cannot stop it in between.
func (m *Manager) ensure(ctx context.Context, p *Plan, in *Instance, o UpOptions) (pid int, shared bool, err error) {
	row, err := m.get(in.ID)
	if err != nil {
		return 0, false, err
	}
	if row != nil {
		if m.reusable(ctx, in, row) {
			if err := m.holdFor(in.ID, o); err != nil {
				return 0, false, err
			}
			return row.PID, true, nil
		}
		// Stale: a crashed or half-started instance. Clear it before starting afresh.
		stopGroup(row.PID, row.pstart, m.Grace)
		if err := m.drop(in.ID); err != nil {
			return 0, false, err
		}
	}
	if in.Node.Oneshot() {
		err = m.runOneshot(ctx, p, in)
	} else {
		pid, err = m.startService(ctx, p, in)
	}
	if err != nil {
		return pid, false, err
	}
	return pid, false, m.holdFor(in.ID, o)
}

// holdFor records who keeps id up: its previous holds when it was restarted,
// else the holder of the run.
func (m *Manager) holdFor(id string, o UpOptions) error {
	if hs, ok := o.restore[id]; ok {
		for _, h := range hs {
			if _, err := m.db.Exec(`INSERT INTO ws_holds (instance, holder, pid, pstart) VALUES (?,?,?,?)
				ON CONFLICT(instance, holder) DO UPDATE SET pid = excluded.pid, pstart = excluded.pstart`,
				id, h.holder, h.pid, h.pstart); err != nil {
				return err
			}
		}
		return nil
	}
	return m.hold(id, o.Holder, o.HolderPID)
}

// reusable decides whether an existing row can serve in. Services must still
// be running and pass their probe; oneshots are reused only while another
// holder is keeping them (otherwise they run again, e.g. a fresh migration).
func (m *Manager) reusable(ctx context.Context, in *Instance, row *Row) bool {
	if in.Node.Oneshot() {
		h, _ := m.holders(in.ID)
		return row.State == StateDone && len(h) > 0
	}
	if !row.Alive || row.State != StateReady {
		return false
	}
	if in.Ready == nil || in.Ready.Kind == readiness.Log {
		return true // a log match cannot be re-checked: it is in the old output
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ok, _ := readiness.Check(pctx, *in.Ready, in.Log)
	return ok
}

func (m *Manager) row(p *Plan, in *Instance, state string) Row {
	return Row{
		ID: in.ID, Node: in.Node.ID, Tree: in.Dir, Project: p.Loc.Project, Kind: kindOf(in.Node),
		Port: in.Port, State: state, Keep: in.Node.Task.Keep, Log: in.Log, StartedAt: time.Now(), Requires: in.Requires,
	}
}

func kindOf(n *Node) string {
	if n.Oneshot() {
		return "oneshot"
	}
	return "service"
}

// spawn starts the instance's command detached in its own session, with
// output going to its log file. The returned channel closes when it exits.
func (m *Manager) spawn(in *Instance) (*exec.Cmd, <-chan struct{}, error) {
	for _, d := range []string{in.State, filepath.Dir(in.Log)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, nil, err
		}
	}
	logf, err := os.OpenFile(in.Log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, nil, err
	}
	defer logf.Close()
	cmd := exec.Command("sh", "-c", in.Cmd)
	cmd.Dir = in.Dir
	cmd.Env = os.Environ()
	for _, k := range slices.Sorted(maps.Keys(in.Env)) {
		cmd.Env = append(cmd.Env, k+"="+in.Env[k])
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start %q: %w", in.Cmd, err)
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait() //nolint:errcheck // reaps the child; exit state is read from the log and the probe
		close(exited)
	}()
	return cmd, exited, nil
}

func (m *Manager) startService(ctx context.Context, p *Plan, in *Instance) (int, error) {
	cmd, exited, err := m.spawn(in)
	if err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	row := m.row(p, in, StateStarting)
	row.PID, row.pstart = pid, processStart(pid)
	if err := m.put(row); err != nil {
		stopGroup(pid, row.pstart, m.Grace)
		return pid, err
	}
	fail := func(err error) (int, error) {
		stopGroup(pid, row.pstart, m.Grace)
		m.drop(in.ID) //nolint:errcheck // best effort: the error being returned matters more
		return pid, err
	}
	if in.Ready != nil {
		dead := func() bool {
			select {
			case <-exited:
				return true
			default:
				return false
			}
		}
		if err := readiness.Wait(ctx, *in.Ready, in.Log, dead); err != nil {
			return fail(err)
		}
	}
	if err := m.setState(in.ID, StateReady); err != nil {
		return fail(err)
	}
	return pid, nil
}

func (m *Manager) runOneshot(ctx context.Context, p *Plan, in *Instance) error {
	cmd, exited, err := m.spawn(in)
	if err != nil {
		return err
	}
	timeout := time.Duration(0)
	if in.Node.Task.Ready.Timeout.Duration > 0 {
		timeout = in.Node.Task.Ready.Timeout.Duration
	}
	var expire <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expire = t.C
	}
	pid := cmd.Process.Pid
	start := processStart(pid)
	select {
	case <-exited:
	case <-expire:
		stopGroup(pid, start, m.Grace)
		return fmt.Errorf("did not finish within %s", timeout)
	case <-ctx.Done():
		stopGroup(pid, start, m.Grace)
		return ctx.Err()
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		detail := "exited with an error"
		if cmd.ProcessState != nil {
			detail = "exit status " + fmt.Sprint(cmd.ProcessState.ExitCode())
		}
		if line := readiness.LastLine(in.Log); line != "" {
			detail += ", last log line: " + line
		}
		return errors.New(detail)
	}
	return m.put(m.row(p, in, StateDone))
}

// Stopped lists instances a Release stopped.
type Stopped struct {
	ID   string `json:"id"`
	Node string `json:"node"`
	Port int    `json:"port,omitempty"`
}

// Release drops every hold of holder (and of `holder#…` sub-holders), then
// stops each instance left without a holder unless its node sets keep, which
// also cleans up after holders that died without releasing. Dependents stop
// before their requirements: instances stop newest first, and a requirement
// always starts before its dependents. Instances still held by another
// workspace are left running.
func (m *Manager) Release(ctx context.Context, holder string) ([]Stopped, error) {
	if _, err := m.db.Exec(`DELETE FROM ws_holds WHERE holder = ? OR substr(holder, 1, ?) = ?`,
		holder, len(holder)+1, holder+"#"); err != nil {
		return nil, err
	}
	return m.Reap(ctx)
}

// Reap stops every instance that has no holder left (after pruning holds of
// dead processes), except keep instances. It repairs the aftermath of a
// crashed `ade ws up`.
func (m *Manager) Reap(ctx context.Context) ([]Stopped, error) {
	if err := m.pruneHolds(); err != nil {
		return nil, err
	}
	rows, err := m.db.Query(`SELECT id FROM ws_instances`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	return m.reap(ctx, ids)
}

func (m *Manager) reap(ctx context.Context, ids []string) ([]Stopped, error) {
	var rows []Row
	for _, id := range ids {
		r, err := m.get(id)
		if err != nil {
			return nil, err
		}
		if r != nil {
			rows = append(rows, *r)
		}
	}
	slices.SortFunc(rows, func(a, b Row) int { return b.StartedAt.Compare(a.StartedAt) })
	var stopped []Stopped
	for _, r := range rows {
		s, err := m.reapOne(ctx, r.ID)
		if err != nil {
			return stopped, err
		}
		if s != nil {
			stopped = append(stopped, *s)
		}
	}
	return stopped, nil
}

// reapOne stops id under its lock if it is still unheld.
func (m *Manager) reapOne(ctx context.Context, id string) (*Stopped, error) {
	unlock, err := m.lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	r, err := m.get(id)
	if err != nil || r == nil {
		return nil, err
	}
	h, err := m.holders(id)
	if err != nil {
		return nil, err
	}
	if len(h) > 0 {
		return nil, nil
	}
	if r.Keep && r.Alive {
		return nil, nil
	}
	stopGroup(r.PID, r.pstart, m.Grace)
	if err := m.drop(id); err != nil {
		return nil, err
	}
	return &Stopped{ID: r.ID, Node: r.Node, Port: r.Port}, nil
}

// Follow copies the instance's log to w from the start until ctx ends or the
// instance's process exits (then it flushes the rest and returns nil).
func Follow(ctx context.Context, logPath string, w io.Writer, exited func() bool) error {
	var off int64
	for {
		gone := exited()
		f, err := os.Open(logPath)
		if err == nil {
			if _, err = f.Seek(off, 0); err == nil {
				buf := make([]byte, 32<<10)
				for {
					n, rerr := f.Read(buf)
					if n > 0 {
						if _, werr := w.Write(buf[:n]); werr != nil {
							f.Close()
							return werr
						}
						off += int64(n)
					}
					if rerr != nil || n == 0 {
						break
					}
				}
			}
			f.Close()
		}
		if gone {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Describe renders a result line: "api.dev ● :54031 ready · http …" with
// words, for plain terminals and VS Code output.
func (r Result) Describe() string {
	var b strings.Builder
	b.WriteString(r.Node)
	if r.Port > 0 {
		fmt.Fprintf(&b, " :%d", r.Port)
	}
	b.WriteString("  " + string(r.Outcome))
	if r.Shared {
		b.WriteString(" (shared)")
	}
	if r.Detail != "" {
		b.WriteString(": " + r.Detail)
	}
	return b.String()
}
