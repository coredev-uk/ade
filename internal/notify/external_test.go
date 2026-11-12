package notify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
)

// extApp is an app whose external command appends stdin and a few variables
// to <dir>/out, one block per run.
func extApp(t *testing.T, mutate func(*config.NotifyExternal)) (*app.App, string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	x := config.Defaults().Notify.External
	x.Command = `cat >> "` + out + `"; echo "env:$ADE_EVENT_KIND:$ADE_EVENT_SEVERITY:$ADE_EVENT_PROJECT" >> "` + out + `"`
	if mutate != nil {
		mutate(&x)
	}
	a := newApp(t, config.Notify{External: x})
	al, err := audit.Open(filepath.Join(dir, "records"), "test")
	if err != nil {
		t.Fatal(err)
	}
	a.Audit = al
	return a, out
}

func publish(t *testing.T, a *app.App, es ...events.Event) {
	t.Helper()
	stop, err := Attach(context.Background(), a, Channels{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		a.Bus.Publish(e)
	}
	stop()
}

func event(kind string, sev events.Severity, project string) events.Event {
	e := events.New(kind, sev, "title of "+kind)
	e.Project, e.Subject, e.Detail = project, "subj", "line1\nline2"
	return e
}

func runs(t *testing.T, out string) []string {
	t.Helper()
	b, err := os.ReadFile(out)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestExternalReceivesPayloadAndEnv(t *testing.T) {
	a, out := extApp(t, nil)
	e := event("budget.warn", events.Warn, "acme")
	e.Link = "https://example.test/x"
	publish(t, a, e)

	lines := runs(t, out)
	if len(lines) != 2 {
		t.Fatalf("output = %q, want the JSON line then the env line", lines)
	}
	var p Payload
	if err := json.Unmarshal([]byte(lines[0]), &p); err != nil {
		t.Fatalf("stdin is not the payload JSON: %v: %q", err, lines[0])
	}
	if p.ID != e.ID || p.Kind != "budget.warn" || p.Severity != "warn" || p.Project != "acme" ||
		p.Subject != "subj" || p.Title != "title of budget.warn" || p.Detail != "line1\nline2" || p.Link != e.Link {
		t.Errorf("payload = %+v", p)
	}
	if lines[1] != "env:budget.warn:warn:acme" {
		t.Errorf("env line = %q", lines[1])
	}
}

func TestExternalMasksCredentials(t *testing.T) {
	a, out := extApp(t, nil)
	e := event("budget.warn", events.Warn, "acme")
	e.Detail = "clone https://user:s3cretpassword@git.example.test/x.git failed"
	publish(t, a, e)
	if got := strings.Join(runs(t, out), "\n"); strings.Contains(got, "s3cretpassword") {
		t.Errorf("credential reached the external command: %s", got)
	}
}

func TestExternalFilter(t *testing.T) {
	a, out := extApp(t, func(x *config.NotifyExternal) {
		x.Kinds = []string{"budget.*", "sync.stalled"}
		x.MinSeverity = "warn"
	})
	transient := event("budget.warn", events.Error, "t")
	transient.Transient = true
	publish(t, a,
		event("budget.warn", events.Warn, "a"),    // sent
		event("budget.stop", events.Error, "b"),   // sent
		event("sync.stalled", events.Warn, "c"),   // sent
		event("ci.failed", events.Error, "d"),     // kind not listed
		event("budget.ok", events.Info, "e"),      // below the minimum severity
		event("budget.done", events.Success, "f"), // below the minimum severity
		transient, // status, not news
	)
	var got []string
	for _, l := range runs(t, out) {
		if strings.HasPrefix(l, "env:") {
			got = append(got, l)
		}
	}
	want := []string{"env:budget.warn:warn:a", "env:budget.stop:error:b", "env:sync.stalled:warn:c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sent %v, want %v", got, want)
	}
}

func TestExternalDedupe(t *testing.T) {
	a, out := extApp(t, func(x *config.NotifyExternal) { x.Dedupe = config.Duration{Duration: time.Hour} })
	publish(t, a,
		event("sync.stalled", events.Warn, "acme"),
		event("sync.stalled", events.Warn, "acme"),  // same kind, project, severity: held back
		event("sync.stalled", events.Warn, "other"), // another project
		event("sync.stalled", events.Error, "acme"), // escalation passes
	)
	// A later ade process (a new Attach on the same cache) honours the window too.
	publish(t, a, event("sync.stalled", events.Warn, "acme"))

	var got []string
	for _, l := range runs(t, out) {
		if strings.HasPrefix(l, "env:") {
			got = append(got, l)
		}
	}
	want := []string{"env:sync.stalled:warn:acme", "env:sync.stalled:warn:other", "env:sync.stalled:error:acme"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sent %v, want %v", got, want)
	}
}

func TestExternalDedupeZeroSendsEverything(t *testing.T) {
	a, out := extApp(t, func(x *config.NotifyExternal) { x.Dedupe = config.Duration{} })
	publish(t, a, event("budget.stop", events.Error, "a"), event("budget.stop", events.Error, "a"))
	if n := len(runs(t, out)); n != 4 {
		t.Errorf("output lines = %d, want 2 runs of 2 lines", n)
	}
}

func TestExternalDedupeWindowExpires(t *testing.T) {
	a, _ := extApp(t, nil)
	st, _ := a.Cache()
	x := &external{cfg: a.Config.Notify.External, db: st.DB}
	now := time.Now()
	x.now = func() time.Time { return now }
	e := event("sync.stalled", events.Warn, "acme")
	if !x.claim(e) || x.claim(e) {
		t.Fatal("first claim must pass and the repeat must not")
	}
	now = now.Add(61 * time.Minute)
	if !x.claim(e) {
		t.Error("claim after the window must pass")
	}
}

func TestExternalTimeoutKillsAndIsAudited(t *testing.T) {
	a, _ := extApp(t, func(x *config.NotifyExternal) {
		x.Command = "sleep 30"
		x.Timeout = config.Duration{Duration: 150 * time.Millisecond}
	})
	start := time.Now()
	publish(t, a, event("budget.stop", events.Error, "acme"))
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("a hung command held ade for %s", took)
	}
	es, err := a.Audit.Query(audit.Filter{Action: ExternalAction})
	if err != nil || len(es) != 1 {
		t.Fatalf("audit = %v, %v", es, err)
	}
	if es[0].Outcome != "error" || !strings.Contains(es[0].Error, "timed out") || es[0].Target != "budget.stop" {
		t.Errorf("entry = %+v", es[0])
	}
}

func TestExternalFailureIsLoggedNotFatal(t *testing.T) {
	a, _ := extApp(t, func(x *config.NotifyExternal) { x.Command = "echo broken pipe to inbox >&2; exit 3" })
	publish(t, a, event("budget.warn", events.Warn, "acme"))
	// The inbox still recorded it.
	st, _ := a.Cache()
	if n, _ := NewInbox(st).UnreadCount(); n != 1 {
		t.Errorf("inbox unread = %d, want 1", n)
	}
	es, _ := a.Audit.Query(audit.Filter{Action: ExternalAction})
	if len(es) != 1 || es[0].Outcome != "error" || !strings.Contains(es[0].Error, "broken pipe to inbox") {
		t.Errorf("audit = %+v", es)
	}
}

func TestExternalSuccessIsAudited(t *testing.T) {
	a, _ := extApp(t, nil)
	publish(t, a, event("budget.warn", events.Warn, "acme"))
	es, _ := a.Audit.Query(audit.Filter{Action: ExternalAction})
	if len(es) != 1 || es[0].Outcome != "ok" || es[0].Project != "acme" {
		t.Errorf("audit = %+v", es)
	}
}

func TestExternalSlowCommandDoesNotBlockPublishing(t *testing.T) {
	a, _ := extApp(t, func(x *config.NotifyExternal) {
		x.Command = "sleep 30"
		x.Timeout = config.Duration{Duration: 20 * time.Second}
		x.Dedupe = config.Duration{}
	})
	stop, err := Attach(context.Background(), a, Channels{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for range 100 { // more than the queue holds
		a.Bus.Publish(event("budget.stop", events.Error, "acme"))
	}
	// Wait for the consumer to take them all without waiting for the command.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := a.Cache()
		if n, _ := NewInbox(st).UnreadCount(); n == 100 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("publishing waited for the command: %s", took)
	}
	stopped := time.Now()
	stop() // abandons the hung command after the drain grace
	if took := time.Since(stopped); took > drainGrace+3*time.Second {
		t.Errorf("stop took %s", took)
	}
}

func TestExternalOffWithoutCommand(t *testing.T) {
	a, out := extApp(t, func(x *config.NotifyExternal) { x.Command = "" })
	publish(t, a, event("budget.warn", events.Warn, "acme"))
	if got := runs(t, out); got != nil {
		t.Errorf("ran with no command: %v", got)
	}
	if es, _ := a.Audit.Query(audit.Filter{Action: ExternalAction}); len(es) != 0 {
		t.Errorf("audited without a command: %v", es)
	}
}

func TestRunReportsErrors(t *testing.T) {
	x := config.NotifyExternal{Command: "exit 7", Timeout: config.Duration{Duration: time.Second}}
	if err := Run(context.Background(), x, event("k.v", events.Warn, "")); err == nil {
		t.Error("a non-zero exit must be an error")
	}
	if err := Run(context.Background(), config.NotifyExternal{}, event("k.v", events.Warn, "")); err == nil {
		t.Error("no command must be an error")
	}
}
