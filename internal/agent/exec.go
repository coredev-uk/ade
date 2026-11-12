package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coredev-uk/ade/internal/config"
)

// StopCostCap is Call.StoppedBy when ADE killed the process at the cap.
const StopCostCap = "cost-cap"

// saveEvery throttles how often a running call's cost reaches the store.
const saveEvery = 500 * time.Millisecond

// callResult is what one process left behind.
type callResult struct {
	Progress Progress
	Exit     int
	// StoppedBy is StopCostCap when ADE (or omp's own --max-cost) stopped it.
	StoppedBy string
	Stderr    string
	Err       error // start failure, or the context's error
}

// procHooks are what the process runner reports back while a call runs.
type procHooks struct {
	// transcript receives every stdout line, verbatim.
	transcript *os.File
	// capUSD hard-stops the process once total (spent before + this call's
	// running cost) exceeds it; 0 = no cap.
	capUSD, spentBefore float64
	// onProgress is called at most every saveEvery, and once at the end.
	onProgress func(Progress)
	// onStart gets the process id once started.
	onStart func(pid int)
}

// runProcess starts c and reads its stdout line by line: each line goes to the
// transcript and the parser, and the running cost is checked after every
// line. Over the cap the whole process group is killed at once: the cap is a
// hard stop, not a request.
func runProcess(ctx context.Context, c Cmd, p Parser, h procHooks, omp3 bool) callResult {
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = append(config.WithoutEnv(os.Environ(), c.Unset), c.Env...)
	cmd.Stdin = strings.NewReader(c.Stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr tailBuffer
	stderr.max = 8 << 10
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return callResult{Err: err}
	}
	if err := cmd.Start(); err != nil {
		return callResult{Err: fmt.Errorf("start %s: %w", c.Path, err)}
	}
	if h.onStart != nil {
		h.onStart(cmd.Process.Pid)
	}
	kill := func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

	var once sync.Once
	var capped bool
	var mu sync.Mutex
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			once.Do(kill)
		case <-stop:
		}
	}()

	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 32<<20)
	last := time.Now()
	for sc.Scan() {
		line := sc.Bytes()
		if h.transcript != nil {
			_, _ = h.transcript.Write(append(append([]byte(nil), line...), '\n'))
		}
		p.Feed(line)
		pr := p.Progress()
		if h.capUSD > 0 && h.spentBefore+pr.CostUSD > h.capUSD {
			mu.Lock()
			capped = true
			mu.Unlock()
			once.Do(kill)
			break
		}
		if h.onProgress != nil && time.Since(last) >= saveEvery {
			h.onProgress(pr)
			last = time.Now()
		}
	}
	// Drain so a killed child never blocks on a full pipe, then reap it.
	_, _ = bufio.NewReader(out).WriteTo(discard{})
	waitErr := cmd.Wait()

	res := callResult{Progress: p.Progress(), Stderr: strings.TrimSpace(stderr.String())}
	var ee *exec.ExitError
	switch {
	case errors.As(waitErr, &ee):
		res.Exit = ee.ExitCode()
		if res.Exit < 0 { // killed by a signal
			res.Exit = 128 + int(ee.Sys().(syscall.WaitStatus).Signal())
		}
	case waitErr != nil:
		res.Err = waitErr
	}
	mu.Lock()
	if capped || (omp3 && res.Exit == OMPExitMaxCost) {
		res.StoppedBy = StopCostCap
	}
	mu.Unlock()
	if ctx.Err() != nil && res.StoppedBy == "" {
		res.Err = ctx.Err()
	}
	if h.onProgress != nil {
		h.onProgress(res.Progress)
	}
	return res
}

type discard struct{}

func (discard) Write(b []byte) (int, error) { return len(b), nil }

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	max int
	buf bytes.Buffer
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.buf.Write(b)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string { return t.buf.String() }

// openTranscript opens the run's transcript for appending, with a header line
// that marks where this call starts.
func openTranscript(dir string, header string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, FileTranscript), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	_, err = f.WriteString(header + "\n")
	return f, err
}

// Alive reports whether a process with this id exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// killGroup kills the process group led by pid.
func killGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
