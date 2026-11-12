package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Defaults of the waits a pane of the services tab does (UpWaiting, WaitInstalled).
const (
	// DefaultWaitTimeout bounds one wait: a pane never hangs for ever on something that will not come.
	DefaultWaitTimeout = 10 * time.Minute
	// DefaultWaitReport is how often a wait says it is still waiting.
	DefaultWaitReport = 30 * time.Second
)

// failureGrace lets a wait that starts a moment after the failure of what it
// waits for still see it, while a failure from an earlier run is ignored: the
// panes of a freshly opened services tab start together, and the failure of a
// previous session must not fail them before their requirement runs again.
const failureGrace = 30 * time.Second

// pacer times one wait: when to report that it is still waiting, and when to give up.
type pacer struct {
	start, next    time.Time
	every, timeout time.Duration
	reported       bool
}

func newPacer(every, timeout time.Duration) *pacer {
	if every <= 0 {
		every = DefaultWaitReport
	}
	if timeout <= 0 {
		timeout = DefaultWaitTimeout
	}
	now := time.Now()
	return &pacer{start: now, next: now, every: every, timeout: timeout}
}

// report says whether a progress line is due and how long the wait has lasted;
// the first line is due at once and reports no time.
func (p *pacer) report() (elapsed time.Duration, due bool) {
	now := time.Now()
	if now.Before(p.next) {
		return 0, false
	}
	p.next = now.Add(p.every)
	if !p.reported {
		p.reported = true
		return 0, true
	}
	return now.Sub(p.start), true
}

func (p *pacer) expired() bool { return time.Since(p.start) >= p.timeout }

func (m *Manager) failurePath(id string) string {
	return filepath.Join(m.StateDir, "failed", slugOf(id))
}

// recordFailure notes why instance id did not come up, for the panes waiting
// for it. Best effort: a wait that cannot see it falls back to its timeout.
func (m *Manager) recordFailure(id, reason string) {
	path := m.failurePath(id)
	if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		_ = os.WriteFile(path, []byte(reason+"\n"), 0o600)
	}
}

// clearFailure forgets the failure of id when it is started again.
func (m *Manager) clearFailure(id string) { _ = os.Remove(m.failurePath(id)) }

// failureSince returns the recorded reason id failed, when that happened at or after since.
func (m *Manager) failureSince(id string, since time.Time) (string, bool) {
	path := m.failurePath(id)
	fi, err := os.Stat(path)
	if err != nil || fi.ModTime().Before(since) {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}
