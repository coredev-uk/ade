// Package readiness probes whether a started node (service) is ready: an HTTP
// endpoint answering 2xx, a TCP port accepting connections, or a regular
// expression showing up in the node's output (PLAN 2.1).
package readiness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Kind selects how readiness is detected.
type Kind string

const (
	HTTP Kind = "http" // 2xx from a GET
	TCP  Kind = "tcp"  // the address accepts a connection
	Log  Kind = "log"  // the regexp matches the output log
)

// DefaultTimeout applies when a spec sets none.
const DefaultTimeout = 60 * time.Second

// pollInterval is how often a probe is retried while waiting.
const pollInterval = 100 * time.Millisecond

// Spec is a resolved probe: templates are already expanded.
type Spec struct {
	Kind    Kind
	Target  string // URL, host:port, or regexp
	Timeout time.Duration
}

// String is the probe as a user reads it, e.g. "http http://localhost:5000/healthz".
func (s Spec) String() string { return string(s.Kind) + " " + s.Target }

// TimeoutError reports a probe that did not pass in time. Detail is the last
// failure (connection error, status, or the last log line).
type TimeoutError struct {
	Spec    Spec
	Timeout time.Duration
	Detail  string
}

func (e *TimeoutError) Error() string {
	msg := fmt.Sprintf("not ready (%s, timeout %s)", e.Spec, e.Timeout.Round(time.Second))
	if e.Detail != "" {
		msg += ", last: " + e.Detail
	}
	return msg
}

// Check runs the probe once. detail explains a failure and is empty on success.
// logPath is only read by Log probes.
func Check(ctx context.Context, s Spec, logPath string) (ok bool, detail string) {
	switch s.Kind {
	case HTTP:
		return checkHTTP(ctx, s.Target)
	case TCP:
		return checkTCP(ctx, s.Target)
	case Log:
		return checkLog(s.Target, logPath)
	}
	return false, "unknown probe kind " + string(s.Kind)
}

// ExitedError reports that the process exited before its probe passed.
type ExitedError struct {
	Spec Spec
	Last string // last log line, when there is one
}

func (e *ExitedError) Error() string {
	msg := "process exited before ready (" + e.Spec.String() + ")"
	if e.Last != "" {
		msg += ", last log line: " + e.Last
	}
	return msg
}

// Wait polls the probe until it passes, the timeout elapses, ctx ends, or
// dead (when non-nil) reports that the process exited. A dead process fails
// at once with its last log line instead of burning the timeout.
func Wait(ctx context.Context, s Spec, logPath string, dead func() bool) error {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if s.Kind == Log {
		if _, err := regexp.Compile(s.Target); err != nil {
			return fmt.Errorf("ready.log: %w", err)
		}
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		ok, detail := Check(ctx, s, logPath)
		if ok {
			return nil
		}
		if dead != nil && dead() {
			// The log may have gained its match just before exit.
			if ok, _ := Check(ctx, s, logPath); ok {
				return nil
			}
			return &ExitedError{Spec: s, Last: LastLine(logPath)}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if line := LastLine(logPath); line != "" {
				detail = "last log line: " + line
			}
			return &TimeoutError{Spec: s, Timeout: timeout, Detail: detail}
		case <-tick.C:
		}
	}
}

var probeClient = &http.Client{
	Timeout: 2 * time.Second,
	// A redirect answer is the endpoint's answer; following it could probe an unrelated host.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func checkHTTP(ctx context.Context, target string) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, err.Error()
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return false, trimErr(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16)) //nolint:errcheck // body is irrelevant
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, "HTTP " + resp.Status
	}
	return true, ""
}

func checkTCP(ctx context.Context, addr string) (bool, string) {
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false, trimErr(err)
	}
	c.Close()
	return true, ""
}

func checkLog(pattern, path string) (bool, string) {
	re, err := regexp.Compile("(?m)" + pattern)
	if err != nil {
		return false, err.Error()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, "no output yet"
	}
	if re.Match(data) {
		return true, ""
	}
	return false, "no match yet"
}

// LastLine returns the last non-empty line of the file, trimmed and capped,
// or "" when it cannot be read.
func LastLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\r\n \t"), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if len(line) > 200 {
		line = line[:200] + "…"
	}
	return line
}

// trimErr drops the noisy `Get "url":` prefix net/http adds.
func trimErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}
