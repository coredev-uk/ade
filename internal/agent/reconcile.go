package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/workspace"
)

// staleStart is how long a run that is planning or running but has no owner
// recorded (made before owners were) and no agent pid yet is given to start.
const staleStart = 2 * time.Minute

// dead reports that a run which says it is planning or running has no process
// behind it: the ADE process that drove it (the owner) is gone, or, for a run
// recorded before owners, so is its agent. A run's agent can outlive its owner
// (SIGKILL, a crash): Reconcile kills it, nobody would read its output.
func (r Run) dead(now time.Time) bool {
	if !r.State.Active() {
		return false
	}
	switch {
	case r.Owner > 0:
		return !workspace.ProcessAlive(r.Owner, r.OwnerStart)
	case r.PID > 0:
		return !workspace.ProcessAlive(r.PID, r.PIDStart)
	}
	return now.Sub(r.Updated) > staleStart
}

// Reconcile marks every run whose process is gone failed, kills an agent its
// owner left behind, and audits the spend the dead call had reached (its cost
// only ever reaches the audit log when the process returns). It returns the
// runs it changed.
func (m *Manager) Reconcile() ([]Run, error) {
	all, err := m.Store.List()
	if err != nil {
		return nil, err
	}
	var out []Run
	for _, r := range all {
		if !r.dead(m.Now()) {
			continue
		}
		if cur, err := m.Store.exact(r.ID); err == nil && !cur.State.Active() {
			continue // another process settled it meanwhile
		}
		if r, ok := m.reap(r); ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// reap fails one dead run.
func (m *Manager) reap(r Run) (Run, bool) {
	kerr := killVerified(r.PID, r.PIDStart)
	now := m.Now()
	reason := "the ADE process that ran it ended before it finished (closed terminal, crash or kill): the run is failed, its work so far is in the worktree"
	if errors.Is(kerr, ErrUnverifiedPID) {
		reason += "; " + kerr.Error()
	}
	var open *Call
	if n := len(r.Calls); n > 0 && r.Calls[n-1].Ended.IsZero() {
		open = &r.Calls[n-1]
		open.Ended, open.Exit = now, -1
	}
	r.PID, r.PIDStart = 0, 0
	r.Error = reason
	if err := r.move(StateFailed, now); err != nil {
		return r, false
	}
	if err := m.Store.Save(r); err != nil {
		return r, false
	}
	_ = m.Store.writeCost(r)
	if open != nil {
		// What the call had cost when its process ended reaches the ledger.
		m.recordCall(r, *open, r.Profile, callResult{
			Progress: Progress{CostUSD: open.CostUSD, Turns: open.Turns, In: open.TokensIn, Out: open.TokensOut},
			Exit:     -1, Err: errors.New(reason),
			Dest: DestHost(open.Driver, ""), Bytes: m.promptBytes(r, open.Kind),
		})
	}
	m.emit(r, "failed", events.Error, false, fmt.Sprintf("agent run %s failed: %s", r.ID, "its process ended"), reason)
	return r, true
}

// promptBytes is the size of the prompt a call sent, from the copy kept in the
// run directory.
func (m *Manager) promptBytes(r Run, k Kind) int {
	if st, err := os.Stat(filepath.Join(m.Store.Dir(r), "prompt-"+string(k)+".md")); err == nil {
		return int(st.Size())
	}
	return 0
}

// List is every run, newest first, after dead ones are reconciled.
func (m *Manager) List() ([]Run, error) {
	if _, err := m.Reconcile(); err != nil {
		return nil, err
	}
	return m.Store.List()
}

// ForItem is the runs of one tracker issue, newest first, after dead ones are
// reconciled.
func (m *Manager) ForItem(ref forge.Ref) ([]Run, error) {
	if _, err := m.Reconcile(); err != nil {
		return nil, err
	}
	return m.Store.ForItem(ref)
}

// Get is the run an id (or unique prefix or suffix) names, after dead ones are
// reconciled.
func (m *Manager) Get(id string) (Run, error) {
	if _, err := m.Reconcile(); err != nil {
		return Run{}, err
	}
	return m.Store.Get(id)
}
