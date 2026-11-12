// Package agent runs coding agents (omp, claude, codex) on work items
// (docs/PLAN.md §6.6): a plan from the issue and repo context, an approved
// plan executed in the item's start-work worktree, and unresolved MR threads
// addressed with drafted replies. A Run is one lifecycle with a persisted
// state machine; its session directory lives in the item record, and the
// transcript lands only there (never in cache.db or the audit log).
package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// Kind is what a run is for.
type Kind string

const (
	// KindPlan starts with a read-only plan; once approved, the same run
	// implements it in the worktree.
	KindPlan Kind = "plan"
	// KindAddress edits code for the MR's unresolved threads and drafts
	// replies into the review drafts. It needs no plan approval.
	KindAddress Kind = "address"
	// KindRun is only ever the kind of a Call: the implementation pass of a
	// plan run.
	KindRun Kind = "run"
)

// State is a run's place in its lifecycle.
type State string

const (
	StateQueued    State = "queued"
	StatePlanning  State = "planning"
	StateAwaiting  State = "awaiting-approval"
	StateRunning   State = "running"
	StateReview    State = "review"
	StatePushed    State = "pushed"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

// Terminal reports whether no transition leaves s.
func (s State) Terminal() bool {
	return s == StatePushed || s == StateFailed || s == StateCancelled
}

// Active reports whether an agent process may be alive.
func (s State) Active() bool { return s == StatePlanning || s == StateRunning }

var transitions = map[State][]State{
	StateQueued:   {StatePlanning, StateRunning, StateFailed, StateCancelled},
	StatePlanning: {StateAwaiting, StateFailed, StateCancelled},
	// Awaiting may go back to planning: a re-plan.
	StateAwaiting: {StateRunning, StatePlanning, StateFailed, StateCancelled},
	StateRunning:  {StateReview, StateFailed, StateCancelled},
	// Review may go back to running: another pass on the same worktree.
	StateReview: {StatePushed, StateRunning, StateFailed, StateCancelled},
}

// CanMove reports whether the state machine allows from → to.
func CanMove(from, to State) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// Call is one agent process of a run: the plan, an implementation pass, or an
// address pass. Its cost counts toward the run's cap.
type Call struct {
	Kind      Kind      `json:"kind"` // plan | run | address
	Driver    string    `json:"driver"`
	Model     string    `json:"model"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended,omitempty"`
	Exit      int       `json:"exit"`
	CostUSD   float64   `json:"cost_usd"`
	Source    string    `json:"cost_source"` // reported | computed | unknown
	TokensIn  int       `json:"tokens_in"`
	TokensOut int       `json:"tokens_out"`
	Turns     int       `json:"turns"`
	// StoppedBy is "cost-cap" when ADE killed the process at the cap.
	StoppedBy string `json:"stopped_by,omitempty"`
}

// Run is the persisted record of one agent lifecycle. The tracker issue
// (Tracker) owns the record directory; the code lives in Worktree.
type Run struct {
	ID      string `json:"id"`
	Kind    Kind   `json:"kind"`
	State   State  `json:"state"`
	Forge   string `json:"forge"`
	Project string `json:"project"`
	// Tracker is the work item the record belongs to.
	Tracker forge.Ref `json:"tracker"`
	// Child is the code-repo issue start work created; commit_prefix may name it.
	Child *forge.Ref `json:"child,omitempty"`
	Repo  string     `json:"repo"`
	// Worktree is the start-work tree the run edits; Branch its branch.
	Worktree string     `json:"worktree"`
	Branch   string     `json:"branch"`
	MR       *forge.Ref `json:"mr,omitempty"`
	// AddDirs are required repos' trees, readable but never written.
	AddDirs []string `json:"add_dirs,omitempty"`

	Profile string `json:"profile"`
	Driver  string `json:"driver"`
	Model   string `json:"model"`
	// CapUSD is the run's cost cap (budget.agent_run_usd or a lower flag).
	CapUSD  float64 `json:"cap_usd"`
	CostUSD float64 `json:"cost_usd"`

	Approved   bool      `json:"approved,omitempty"`
	ApprovedAt time.Time `json:"approved_at,omitempty"`
	// PlanSHA is the hash of plan.md when it was approved: an edit after
	// approval invalidates it.
	PlanSHA string `json:"plan_sha,omitempty"`
	// PID is the agent process of the running call (0 when none).
	PID int `json:"pid,omitempty"`
	// BaseSHA is the worktree HEAD before the first implementation pass.
	BaseSHA string `json:"base_sha,omitempty"`
	// Commits are the SHAs ADE committed on push.
	Commits []string `json:"commits,omitempty"`

	Calls   []Call    `json:"calls,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Error   string    `json:"error,omitempty"`
	// Drafts is how many review drafts an address run left (pending triage).
	Drafts int `json:"drafts,omitempty"`
}

// Item is the run's work item as shown to people: group/project#N.
func (r Run) Item() string { return r.Tracker.String() }

// Elapsed is the time spent on agent calls, plus the running call's so far.
func (r Run) Elapsed(now time.Time) time.Duration {
	var d time.Duration
	for _, c := range r.Calls {
		end := c.Ended
		if end.IsZero() {
			end = now
		}
		d += end.Sub(c.Started)
	}
	return d
}

// Remaining is what the cap still allows; 0 means uncapped only when CapUSD
// is 0 (callers check that first).
func (r Run) Remaining() float64 {
	if r.CapUSD <= 0 {
		return 0
	}
	return max(r.CapUSD-r.CostUSD, 0)
}

// move applies the transition or reports it illegal.
func (r *Run) move(to State, now time.Time) error {
	if !CanMove(r.State, to) {
		return fmt.Errorf("run %s is %s: it cannot become %s", r.ID, r.State, to)
	}
	r.State, r.Updated = to, now
	return nil
}

// newID is sortable by time and short enough to type: 20261111-142530-a3f.
func newID(now time.Time, rnd uint32) string {
	return fmt.Sprintf("%s-%03x", now.UTC().Format("20060102-150405"), rnd&0xfff)
}

// matchID picks the run an id, unique prefix, or unique suffix names.
func matchID(ids []string, want string) (string, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		return "", fmt.Errorf("no run id")
	}
	var hits []string
	for _, id := range ids {
		if id == want {
			return id, nil
		}
		if strings.HasPrefix(id, want) || strings.HasSuffix(id, want) {
			hits = append(hits, id)
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no run %q: `ade agent list` shows the runs", want)
	case 1:
		return hits[0], nil
	}
	return "", fmt.Errorf("%q matches %d runs (%s): use more of the id", want, len(hits), strings.Join(hits, ", "))
}
