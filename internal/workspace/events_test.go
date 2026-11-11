package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
)

func TestResultEvents(t *testing.T) {
	tests := []struct {
		name    string
		r       Result
		kind    string
		sev     events.Severity
		title   string
		subject string
		none    bool
	}{
		{"service ready names the tree", Result{Node: "api.dev", Kind: "service", Port: 5431, Tree: "/p/demo/api/1-limit", Outcome: OutcomeReady}, KindTaskReady, events.Success, "api.dev ready on :5431", "api/1-limit", false},
		{"oneshot done", Result{Node: "api.check", Kind: "oneshot", Tree: "/p/demo/api/main", Outcome: OutcomeReady}, KindTaskDone, events.Success, "api.check finished", "api/main", false},
		{"port clash says which port and what to do", Result{Node: "api.dev", Port: 5431, Outcome: OutcomeFailed, Detail: "process exited before ready (http x), last log line: listen tcp: bind: address already in use"}, KindTaskFailed, events.Error, "api.dev failed to start", "port 5431 is already in use\nfree the port, then run ade ws up", false},
		{"timeout", Result{Node: "api.dev", Outcome: OutcomeFailed, Detail: "not ready (http x, timeout 60s), last: HTTP 503"}, KindTaskFailed, events.Error, "api.dev failed to start", "not ready within 60s\nread the log, fix it, then run ade ws up", false},
		{"oneshot status", Result{Node: "api.check", Outcome: OutcomeFailed, Detail: "exit status 2, last log line: boom"}, KindTaskFailed, events.Error, "api.check failed to start", "exited with status 2\nread the log, fix it, then run ade ws up", false},
		{"blocked names the requirement", Result{Node: "web.dev", Outcome: OutcomeBlocked, Detail: "blocked: api.dev not ready (timeout 60s)"}, KindTaskBlocked, events.Warn, "web.dev waits for api.dev", "api.dev did not come up\nfix it, then run ade ws up", false},
		{"reuse is not news", Result{Node: "api.dev", Outcome: OutcomeReady, Shared: true}, "", 0, "", "", true},
	}
	for _, tc := range tests {
		e, ok := ResultEvent("demo", tc.r)
		if ok == tc.none {
			t.Fatalf("%s: ok=%v", tc.name, ok)
		}
		if tc.none {
			continue
		}
		if e.Kind != tc.kind || e.Severity != tc.sev || e.Title != tc.title || e.Subject != tc.subject || e.Project != "demo" || e.Transient {
			t.Errorf("%s: %+v", tc.name, e)
		}
		if strings.Contains(e.Title, "last log line") {
			t.Errorf("%s: the raw error belongs in the detail, not the title", tc.name)
		}
	}
	e, _ := ResultEvent("demo", Result{Node: "api.dev", Outcome: OutcomeFailed, Detail: "boom", Log: "/l/api.log"})
	if !strings.Contains(e.Detail, "boom") || !strings.Contains(e.Detail, "log: /l/api.log") {
		t.Fatalf("detail: %q", e.Detail)
	}
}

func TestExitEventNamesTheLastLogLineAndTheWayOut(t *testing.T) {
	log := filepath.Join(t.TempDir(), "x.log")
	os.WriteFile(log, []byte("started\npanic: out of memory\n"), 0o600) //nolint:errcheck
	e := ExitEvent("demo", "api.dev", log)
	if e.Kind != KindTaskExited || e.Severity != events.Warn || e.Title != "api.dev exited" ||
		!strings.Contains(e.Subject, "panic: out of memory") || !strings.Contains(e.Subject, "restart with ade ws up") {
		t.Fatalf("%+v", e)
	}
}

func TestRunRecordIsMetadataOnly(t *testing.T) {
	dir := t.TempDir()
	tracker := forge.Ref{Project: "grp/tracker", IID: 7, Kind: forge.KindIssue}
	loc := Location{Project: "demo", Repo: "web", Tree: "/p/demo/web/12-x"}
	r := NewRecorder(dir, "gl", tracker, loc, "", "12-x")
	if want := filepath.Join(dir, "items", "gl", "grp", "tracker", "7", "runs"); !strings.HasPrefix(r.Path(), want) || filepath.Base(r.Path()) != "run.md" {
		t.Fatalf("path %s not under %s", r.Path(), want)
	}
	if _, err := os.Stat(r.Path()); err == nil {
		t.Fatal("nothing is written before Update")
	}
	secret := "SECRET-TOKEN-VALUE"
	res := []Result{
		{Node: "api.dev", Tree: "/p/demo/api/main", Port: 5431, PID: 99, Outcome: OutcomeReady, Shared: true, Detail: secret},
		{Node: "web.dev", Tree: "/p/demo/web/12-x", Port: 5444, PID: 100, Outcome: OutcomeReady},
	}
	if err := r.Update(RunRunning, res); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(r.Path())
	for _, want := range []string{"kind: workspace", "state: running", "item: grp/tracker#7", "branch: 12-x", "workspace: default", "| api.dev | ready (shared) | 5431 | 99 | /p/demo/api/main |"} {
		if !strings.Contains(string(first), want) {
			t.Errorf("run.md lacks %q:\n%s", want, first)
		}
	}
	if strings.Contains(string(first), secret) || strings.Contains(string(first), "ended:") {
		t.Fatalf("run.md must carry metadata only, and no end time while running:\n%s", first)
	}
	time.Sleep(10 * time.Millisecond)
	if err := r.Update(RunStopped, res); err != nil {
		t.Fatal(err)
	}
	last, _ := os.ReadFile(r.Path())
	if !strings.Contains(string(last), "state: stopped") || !strings.Contains(string(last), "ended: ") {
		t.Fatalf("stopped record:\n%s", last)
	}
	var nilRec *Recorder
	if err := nilRec.Update(RunStopped, res); err != nil || nilRec.Path() != "" {
		t.Fatal("a nil recorder must be a no-op")
	}
}
