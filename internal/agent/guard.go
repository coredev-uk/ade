package agent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/ledger"
	"github.com/coredev-uk/ade/internal/workspace"
)

// ErrUncapped is returned when a codex run that edits files (implement,
// address) is asked for while a cost cap applies, without the user's explicit
// yes. Codex reports cost only when a turn completes, and one turn covers
// everything up to the final answer, so ADE cannot stop the process at the
// cap: it can only notice afterwards.
var ErrUncapped = errors.New("codex cannot be stopped at the cost cap")

// uncappedError says what the user may do about ErrUncapped.
func uncappedError(capUSD float64) error {
	what := "a budget cap applies"
	if capUSD > 0 {
		what = fmt.Sprintf("the run cap is $%.2f", capUSD)
	}
	return fmt.Errorf("%w: codex reports cost per turn and one turn covers the whole edit, so ADE would only see the spend after the work is done (%s). Pass --allow-uncapped (TUI: confirm) to run it anyway, or use the omp or claude agent, which are stopped at the cap", ErrUncapped, what)
}

// aiUnavailable is the refusal when config.AI.Available says no. Its text is
// that reason, the one the dialogs show; it unwraps to the sentinel the AI
// layer words for a person (ai.Human).
type aiUnavailable struct {
	reason string
	cause  error
}

func (e *aiUnavailable) Error() string { return e.reason }
func (e *aiUnavailable) Unwrap() error { return e.cause }

// aiOff reports that AI cannot be used (ai.mode "off", no profile selected,
// the profile undefined; config.AI.Available): no agent process may start,
// whatever state an approved plan or queued run was left in.
func (m *Manager) aiOff() error {
	if m.Cfg == nil {
		return nil
	}
	ok, reason := m.Cfg.AI.Available()
	if ok {
		return nil
	}
	cause := config.ErrNoProfile
	switch m.Cfg.AI.State() {
	case config.AIOff:
		cause = ai.ErrDisabled
	case config.AIUnknown:
		cause = config.ErrProfileUndefined
	}
	return &aiUnavailable{reason: reason, cause: cause}
}

// capApplies reports whether any cost cap is in force: the run's own, or a
// daily, weekly or sprint cap.
func (m *Manager) capApplies(runCap float64) bool {
	b := m.Cfg.Budget
	return runCap > 0 || b.DailyUSD > 0 || b.WeeklyUSD > 0 || b.SprintUSD > 0
}

// checkUncapped refuses a codex write run under a cap unless the user said
// yes to running it uncapped.
func (m *Manager) checkUncapped(driver string, kind Kind, capUSD float64, allowed bool) error {
	if driver != "codex" || kind == KindPlan || allowed || !m.capApplies(capUSD) {
		return nil
	}
	return uncappedError(capUSD)
}

// AllowUncapped records the user's yes to running run id's codex agent
// without a stop at the cap (--allow-uncapped, or the TUI's confirm), audited.
func (m *Manager) AllowUncapped(id string) (Run, error) {
	r, err := m.Store.Get(id)
	if err != nil {
		return r, err
	}
	if r.State.Terminal() {
		return r, fmt.Errorf("run %s is already %s", r.ID, r.State)
	}
	if r.Uncapped {
		return r, nil
	}
	r.Uncapped, r.Updated = true, m.Now()
	if err := m.Store.Save(r); err != nil {
		return r, err
	}
	m.audit(auditEntry("agent.allow-uncapped", "user", r, map[string]any{"run": r.ID, "driver": r.Driver, "cap_usd": r.CapUSD}))
	return r, nil
}

func auditEntry(action, actor string, r Run, params map[string]any) audit.Entry {
	return audit.Entry{Action: action, Actor: actor, Target: r.Item(), Item: r.Item(), Project: r.Project, Params: params}
}

// room is how much the period caps (budget.daily_usd, weekly_usd, sprint_usd)
// still allow: the smallest headroom of the caps that are set.
type room struct {
	USD     float64
	Limited bool   // a period cap is set
	Which   string // the cap that binds, e.g. "daily cap budget.daily_usd $2.00"
}

// periodRoom reads the ledger for what the period caps leave, with the calls
// in flight (held: reserved by this or another process) counted as spent.
// Without an AI service (no ledger) nothing is known and nothing limits the call.
func (m *Manager) periodRoom(held ledger.Held) (room, error) {
	var out room
	if m.AI == nil || m.AI.Ledger == nil {
		return out, nil
	}
	b := m.Cfg.Budget
	if b.DailyUSD <= 0 && b.WeeklyUSD <= 0 && b.SprintUSD <= 0 {
		return out, nil
	}
	loc := m.AI.Loc
	if loc == nil {
		loc = time.Local
	}
	sp, err := m.AI.Ledger.Periods(m.Now(), loc, m.AI.SprintStart)
	if err != nil {
		return out, err
	}
	consider := func(name, key string, capUSD, spent float64) {
		if capUSD <= 0 {
			return
		}
		left := max(capUSD-spent, 0)
		if !out.Limited || left < out.USD {
			out = room{USD: left, Limited: true, Which: fmt.Sprintf("%s cap budget.%s $%.2f (%s)", name, key, capUSD, spentNote(spent, held.Total))}
		}
	}
	consider("daily", "daily_usd", b.DailyUSD, sp.Day+held.Total)
	consider("weekly", "weekly_usd", b.WeeklyUSD, sp.Week+held.Total)
	if sp.HaveSprint {
		consider("sprint", "sprint_usd", b.SprintUSD, sp.Sprint+held.Total)
	}
	return out, nil
}

// spentNote words what a period cap has used, in-flight reservations apart.
func spentNote(spentWithHeld, held float64) string {
	if held <= 0 {
		return fmt.Sprintf("$%.2f spent", spentWithHeld)
	}
	return fmt.Sprintf("$%.2f spent, $%.2f in flight in other calls", spentWithHeld-held, held)
}

// agentCallTTL bounds an agent call's reservation when its pid is still alive
// (a reused pid): an agent call has no timeout, so this is far longer than any
// run, and the process check is what normally ends a dead call's reservation.
const agentCallTTL = 12 * time.Hour

// reserve reserves one agent call's cost ceiling in the ledger shared with the
// AI service and every other ade process (see ai.Service.Reserve); check runs
// under its lock. Without an AI service nothing is shared and check runs alone.
func (m *Manager) reserve(profile, feature string, check func(ledger.Held) (float64, error)) (release func(), err error) {
	if m.AI == nil {
		if _, err := check(ledger.Held{}); err != nil {
			return nil, err
		}
		return func() {}, nil
	}
	return m.AI.Reserve(profile, feature, agentCallTTL, check)
}

// --- one agent per worktree ---------------------------------------------------

// lockDir holds one file per worktree an agent is working in.
const lockDir = "agent-locks"

// lockWorktree claims dir for run id: two agents editing one tree would mix
// their changes into one diff, and pushing the first run would commit the
// second's. A claim whose process is gone is taken over.
func (m *Manager) lockWorktree(dir, id string) (release func(), err error) {
	base := filepath.Join(m.Store.dir, lockDir)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(base, sha([]byte(dir))+".lock")
	me := os.Getpid()
	body := fmt.Sprintf("%s %d %d\n", id, me, workspace.ProcessStart(me))
	for range 2 {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := f.WriteString(body)
			cerr := f.Close()
			if err := errors.Join(werr, cerr); err != nil {
				_ = os.Remove(path)
				return nil, err
			}
			return func() {
				if b, err := os.ReadFile(path); err == nil && string(b) == body {
					_ = os.Remove(path)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		holder, pid, start := readLock(path)
		if workspace.ProcessAlive(pid, start) {
			return nil, fmt.Errorf("run %s (pid %d) is already working in %s: wait for it, or cancel it (ade agent cancel %s)", holder, pid, dir, holder)
		}
		_ = os.Remove(path) // its process died without releasing it
	}
	return nil, fmt.Errorf("could not claim %s: another run is starting there", dir)
}

func readLock(path string) (run string, pid int, start uint64) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, 0
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return "", 0, 0
	}
	pid, _ = strconv.Atoi(f[1])
	start, _ = strconv.ParseUint(f[2], 10, 64)
	return f[0], pid, start
}

// ErrUnverifiedPID is returned when a process should be stopped but its start
// time was never recorded (a run made before ADE recorded it): a pid alone may
// have been reused by an unrelated process, so ADE does not kill it.
var ErrUnverifiedPID = errors.New("no start time was recorded for the process")

// killVerified kills the process group led by pid, but only when pid still is
// the process recorded with start time pstart: a recycled pid is somebody
// else's process. Without a recorded start time it kills nothing and returns
// ErrUnverifiedPID (where /proc cannot give start times at all, the pid is all
// there is to go on).
func killVerified(pid int, pstart uint64) error {
	if pid <= 0 {
		return nil
	}
	if pstart == 0 && workspace.ProcessStart(os.Getpid()) != 0 {
		if workspace.ProcessAlive(pid, 0) {
			return fmt.Errorf("pid %d: %w: it is not killed; if it is still the agent, stop it yourself (kill -- -%d)", pid, ErrUnverifiedPID, pid)
		}
		return nil
	}
	if !workspace.ProcessAlive(pid, pstart) {
		return nil
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// roomLeft refuses a run that would start with nothing left under the daily,
// weekly or sprint cap. It runs before the run changes state, so a plan
// approved yesterday waits instead of failing.
func (m *Manager) roomLeft() error {
	var held ledger.Held
	if m.AI != nil {
		var err error
		if held, err = m.AI.Held(); err != nil {
			return err
		}
	}
	pr, err := m.periodRoom(held)
	if err != nil {
		return err
	}
	if pr.Limited && pr.USD <= 0 {
		return &ai.StopError{Source: "local", Message: pr.Which + ": nothing left for this call"}
	}
	return nil
}

// redactor masks the forge tokens ADE knows (the values of every forge's
// token_env and read_token_env in its own environment) in transcript lines: an
// agent that prints its environment must not leave a token in transcript.jsonl.
// nil when there is nothing to mask.
func (m *Manager) redactor() func([]byte) []byte {
	var secrets [][]byte
	for _, f := range m.Cfg.Forges {
		for _, name := range []string{f.TokenEnv, f.ReadTokenEnv} {
			if v := os.Getenv(name); name != "" && len(v) >= 8 {
				secrets = append(secrets, []byte(v))
			}
		}
	}
	if len(secrets) == 0 {
		return nil
	}
	return func(line []byte) []byte {
		for _, s := range secrets {
			line = bytes.ReplaceAll(line, s, []byte("***"))
		}
		return line
	}
}

// PlanIntact reports that run r's approval still covers plan.md: it was
// approved and the file has not been edited since.
func (m *Manager) PlanIntact(r Run) bool {
	if !r.Approved {
		return false
	}
	b, err := os.ReadFile(filepath.Join(m.Store.Dir(r), FilePlan))
	return err == nil && sha(b) == r.PlanSHA
}
