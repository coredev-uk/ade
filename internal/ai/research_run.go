package ai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/records"
)

// Detached research (feedback F13). `ade ai research --detach` and the Home
// `R` key run the pre-flight in the foreground, where a refusal or a question
// can be answered, then hand the paid call to a worker process that outlives
// the caller. The worker is `ade ai research --worker <id>`: it runs the same
// job through Service.Run, so the result cache, the caps, the ledger and the
// audit trail apply exactly as for any other call. The state of the job lives
// in the item's record (records.Research); the answer is an AI artifact there,
// found again by its input hash, so asking twice about an unchanged issue buys
// nothing.

// staleStart is how long a job may sit at "running" without a worker pid
// before it counts as dead: the spawn happened but the worker never came up.
const staleStart = 2 * time.Minute

// ErrResearchRunning is returned by StartResearch when the issue's research job
// is still running.
var ErrResearchRunning = errors.New("research is already running for this issue")

// ResearchOpts are how a research job may be started.
type ResearchOpts struct {
	Profile  string
	Refresh  bool // buy a new answer past the cache
	Override bool // go over the local caps (audited as budget.override)
	Assume   bool // skip the confirmation
	Notify   bool // raise an event (inbox, toast) when the job finishes: set for detached workers
	Confirm  func(Preflight) (bool, error)
	// InputHash is the hash of the input the pre-flight was confirmed on
	// (JobHash): the worker refuses to send anything else.
	InputHash string
}

// ResearchSpawnSpec is what the worker process is told.
type ResearchSpawnSpec struct {
	Forge    string
	Ref      forge.Ref
	JobID    string
	Profile  string
	Refresh  bool
	Override bool
	// NoNotes is true when the confirmed prompt left the user's notes out
	// (--no-notes, or there were none): the worker must build it the same way.
	NoNotes bool
	// InputHash is JobHash of the confirmed job. The worker rebuilds the job
	// and refuses to send it when its hash differs (the issue, the notes or the
	// flags changed since the yes).
	InputHash string
	Log       string // file the worker's output goes to
}

// ResearchSpawn starts the worker and returns its pid. Production runs this
// binary detached; tests replace it.
var ResearchSpawn = spawnResearchWorker

func spawnResearchWorker(sp ResearchSpawnSpec) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("find the ade binary: %w", err)
	}
	args := []string{"ai", "research", sp.Ref.String(), "--forge", sp.Forge, "--worker", sp.JobID, "--yes"}
	if sp.Profile != "" {
		args = append(args, "--profile", sp.Profile)
	}
	if sp.Refresh {
		args = append(args, "--refresh")
	}
	if sp.Override {
		args = append(args, "--override-budget")
	}
	if sp.NoNotes {
		args = append(args, "--no-notes")
	}
	if sp.InputHash != "" {
		args = append(args, "--input-hash", sp.InputHash)
	}
	if err := os.MkdirAll(filepath.Dir(sp.Log), 0o700); err != nil {
		return 0, err
	}
	logf, err := os.OpenFile(sp.Log, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // survives the terminal and the dashboard
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start the research worker: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

// gate applies a pre-flight's verdict the way Run does, without sending
// anything: a refusal is audited as a budget stop (unless Override lets it
// through, which the worker audits when it runs), and a confirmation is asked
// for. It returns nil when the call may go ahead.
func (s *Service) gate(j Job, pf Preflight) error {
	item := j.Ref.String()
	if pf.Refuse != "" && !j.Override {
		stop := &StopError{Source: "local", Message: pf.Refuse}
		s.recordStop(j, pf.Profile, pf.Model, item, stop)
		return stop
	}
	if len(pf.Confirm) == 0 || j.Assume {
		return nil
	}
	if j.Confirm == nil {
		return &ErrNeedsConfirm{Preflight: pf}
	}
	ok, err := j.Confirm(pf)
	if err != nil {
		return err
	}
	if !ok {
		s.record(audit.Entry{
			Action: "ai." + j.Feature, Item: item, Target: item, Outcome: "cancelled",
			AI: &audit.AIInfo{Profile: pf.Profile, Driver: pf.Driver, Model: pf.Model, CostSource: CostReported},
		})
		return ErrCancelled
	}
	return nil
}

// ResearchStart is the outcome of StartResearch.
type ResearchStart struct {
	Research  records.Research
	Preflight Preflight
	// Result is set when the answer was already bought: it is stored and shown
	// at once, nothing is spawned and nothing is spent.
	Result *Result
}

// StartResearch runs the pre-flight for j in the foreground and, when the
// call may go ahead and is not cached, starts the detached worker. A cached
// answer is filed in the item's record straight away. ErrResearchRunning when
// the item's previous job is still alive.
func (s *Service) StartResearch(ctx context.Context, forgeName string, j Job, o ResearchOpts) (ResearchStart, error) {
	j.Profile, j.Refresh, j.Override, j.Assume, j.Confirm = o.Profile, o.Refresh, o.Override, o.Assume, o.Confirm
	if cur, err := s.ReadResearch(forgeName, j.Ref); err == nil && cur.State == records.ResearchRunning {
		return ResearchStart{Research: cur.Research}, ErrResearchRunning
	}
	pf, cached, err := s.Preview(ctx, j)
	if err != nil {
		return ResearchStart{}, err
	}
	st := records.Research{ID: ulid.Make().String(), Ref: j.Ref.String(), State: records.ResearchRunning, Started: s.Now().UTC()}
	if cached {
		res, err := s.Run(ctx, j)
		if err != nil {
			return ResearchStart{Preflight: pf}, err
		}
		st.State, st.Finished, st.Profile, st.Model = records.ResearchDone, s.Now().UTC(), res.Profile, res.Model
		st.CacheHit, st.Artifact = true, res.Artifact
		if err := s.Records.WriteResearch(forgeName, j.Ref, st); err != nil {
			return ResearchStart{Preflight: pf, Result: &res}, err
		}
		return ResearchStart{Research: st, Preflight: pf, Result: &res}, nil
	}
	if err := s.gate(j, pf); err != nil {
		return ResearchStart{Preflight: pf}, err
	}
	if err := s.Records.WriteResearch(forgeName, j.Ref, st); err != nil {
		return ResearchStart{Preflight: pf}, err
	}
	hash, err := s.JobHash(j)
	if err != nil {
		return ResearchStart{Preflight: pf}, err
	}
	_, err = ResearchSpawn(ResearchSpawnSpec{
		Forge: forgeName, Ref: j.Ref, JobID: st.ID, Profile: o.Profile, Refresh: o.Refresh, Override: o.Override,
		NoNotes: !j.Notes, InputHash: hash,
		Log: s.Records.ResearchLogPath(forgeName, j.Ref),
	})
	if err != nil {
		st.State, st.Finished, st.Error = records.ResearchFailed, s.Now().UTC(), err.Error()
		_ = s.Records.WriteResearch(forgeName, j.Ref, st)
		return ResearchStart{Research: st, Preflight: pf}, err
	}
	return ResearchStart{Research: st, Preflight: pf}, nil
}

// RunResearch is the worker's side (and the foreground run's): it marks the job
// as its own, runs j (the worker is started with the confirmation already
// given, so o.Assume is set), and files the outcome. A failure is stored
// in the job and returned; either way the user is notified through the bus.
func (s *Service) RunResearch(ctx context.Context, forgeName, jobID string, j Job, o ResearchOpts) (Result, error) {
	j.Profile, j.Refresh, j.Override, j.Assume, j.Confirm = o.Profile, o.Refresh, o.Override, o.Assume, o.Confirm
	st, err := s.Records.ReadResearch(forgeName, j.Ref)
	if err != nil || st.ID != jobID {
		st = records.Research{ID: jobID, Ref: j.Ref.String(), Started: s.Now().UTC()}
	}
	st.State, st.PID = records.ResearchRunning, os.Getpid()
	if err := s.Records.WriteResearch(forgeName, j.Ref, st); err != nil {
		return Result{}, err
	}
	var res Result
	err = s.checkInput(j, o.InputHash)
	if err == nil {
		res, err = s.Run(ctx, j)
	}
	st.Finished = s.Now().UTC()
	if err != nil {
		st.State, st.Error = records.ResearchFailed, err.Error()
		if werr := s.Records.WriteResearch(forgeName, j.Ref, st); werr != nil {
			err = errors.Join(err, werr)
		}
		if o.Notify {
			s.publish("ai.research.failed", events.Error, "Research failed on "+j.Ref.String(), j.Ref, err.Error())
		}
		return res, err
	}
	st.State, st.Error = records.ResearchDone, ""
	st.Profile, st.Model, st.CostUSD, st.CacheHit, st.Artifact = res.Profile, res.Model, res.CostUSD, res.CacheHit, res.Artifact
	if err := s.Records.WriteResearch(forgeName, j.Ref, st); err != nil {
		return res, err
	}
	title := "Research ready on " + j.Ref.String()
	if res.CutShort() != "" {
		title += " (cut short)"
	}
	if o.Notify {
		s.publish("ai.research.done", events.Success, title, j.Ref, "")
	}
	return res, nil
}

func (s *Service) publish(kind string, sev events.Severity, title string, ref forge.Ref, detail string) {
	if s.Bus == nil {
		return
	}
	e := events.New(kind, sev, title)
	e.Subject, e.Detail = ref.String(), detail
	s.Bus.Publish(e)
}

// ResearchView is an item's research job as it can be shown: the stored state,
// with a dead worker reported as failed, and the answer when there is one.
type ResearchView struct {
	records.Research
	Text string `json:"text,omitempty"`
}

// ReadResearch reads the item's research job and, for a finished one, its
// answer from the artifact. records.ErrNoResearch when there is no job.
func (s *Service) ReadResearch(forgeName string, ref forge.Ref) (ResearchView, error) {
	r, err := s.Records.ReadResearch(forgeName, ref)
	if err != nil {
		return ResearchView{}, err
	}
	if r.State == records.ResearchRunning && !s.workerAlive(r) {
		r.State, r.Finished = records.ResearchFailed, s.Now().UTC()
		r.Error = "the background worker ended without writing a result (see " + s.Records.ResearchLogPath(forgeName, ref) + ")"
	}
	v := ResearchView{Research: r}
	if r.State == records.ResearchDone && r.Artifact != "" {
		if a, err := s.Records.ReadArtifact(r.Artifact); err == nil {
			v.Text = a.Body
		}
	}
	return v, nil
}

// workerAlive reports whether a running job's worker can still be there. A
// job with no pid yet is given staleStart to come up.
func (s *Service) workerAlive(r records.Research) bool {
	if r.PID <= 0 {
		return s.Now().Sub(r.Started) < staleStart
	}
	err := syscall.Kill(r.PID, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// ErrInputChanged is returned by a research worker whose rebuilt prompt is not
// the one the pre-flight was confirmed on: it sends nothing.
var ErrInputChanged = errors.New("the research input changed since you confirmed the pre-flight (the issue, your notes or the options): nothing was sent; start it again")

// JobHash is the hash that keys j's cached answer: prompt version, role,
// model, system prompt and prompt. Two jobs with the same hash send the same
// request to the same model.
func (s *Service) JobHash(j Job) (string, error) {
	_, _, model, err := s.profileFor(j)
	if err != nil {
		return "", err
	}
	return InputHash(j.PromptVersion, string(j.Role), model, j.System, j.Prompt), nil
}

// checkInput refuses a job whose hash is not want. An empty want checks nothing
// (a foreground run has no earlier confirmation to match).
func (s *Service) checkInput(j Job, want string) error {
	if want == "" {
		return nil
	}
	h, err := s.JobHash(j)
	if err != nil {
		return err
	}
	if h != want {
		return ErrInputChanged
	}
	return nil
}

// Cancelled is the outcome of CancelResearch.
type Cancelled struct {
	// PID is the worker's pid (0 when it never started).
	PID int
	// Signalled is true when the worker was sent SIGTERM. False with a PID means
	// the job is filed as cancelled but the process was left alone: it could not
	// be verified as this job's worker (no /proc to read its command line), and
	// signalling a pid on trust could hit a stranger.
	Signalled bool
}

// CancelResearch stops the detached worker of the item's research job and files
// the job as failed ("cancelled by you"). It signals the worker's process group
// (the worker is its own session leader, so omp, its child, goes with it). A pid
// is signalled only when it is alive and its command line (/proc) says it is
// this job's worker: a stale pid something else has taken is refused with an
// error, and one that cannot be verified (no /proc, as on macOS) is not signalled
// but the job is still filed as cancelled, and the caller is told the pid.
// ErrNotRunning when there is nothing to cancel.
func (s *Service) CancelResearch(forgeName string, ref forge.Ref) (Cancelled, error) {
	r, err := s.Records.ReadResearch(forgeName, ref)
	if err != nil || r.State != records.ResearchRunning {
		return Cancelled{}, ErrNotRunning
	}
	out := Cancelled{PID: r.PID}
	if r.PID > 0 && s.workerAlive(r) {
		switch isWorkerOf(r.PID, r.ID) {
		case workerNo:
			return out, fmt.Errorf("pid %d is not this research job's worker any more: not signalling it", r.PID)
		case workerYes:
			if err := syscall.Kill(-r.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
				if err := syscall.Kill(r.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
					return out, fmt.Errorf("stop the worker (pid %d): %w", r.PID, err)
				}
			}
			out.Signalled = true
		}
	}
	r.State, r.Finished, r.Error = records.ResearchFailed, s.Now().UTC(), "cancelled by you"
	if err := s.Records.WriteResearch(forgeName, ref, r); err != nil {
		return out, err
	}
	s.record(audit.Entry{Action: "research.cancel", Item: ref.String(), Target: ref.String(), Params: map[string]any{"job": r.ID, "pid": r.PID, "signalled": out.Signalled}})
	return out, nil
}

// ErrNotRunning is returned by CancelResearch for an item with no running job.
var ErrNotRunning = errors.New("no research is running for this issue")

// procCmdline reads a process's command line; tests replace it.
var procCmdline = func(pid int) ([]byte, error) { return os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)) }

type workerCheck int

const (
	workerUnknown workerCheck = iota // the command line cannot be read
	workerYes
	workerNo
)

// isWorkerOf reports whether pid is the worker of job id, from its command
// line. Without /proc it cannot say: workerUnknown, never a guess.
func isWorkerOf(pid int, id string) workerCheck {
	cmdline, err := procCmdline(pid)
	switch {
	case err != nil:
		return workerUnknown
	case strings.Contains(string(cmdline), "--worker\x00"+id):
		return workerYes
	}
	return workerNo
}
