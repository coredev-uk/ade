package notify

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/store"
)

func init() {
	store.Register("notify.external_sent", `CREATE TABLE external_sent (
		key  TEXT PRIMARY KEY,
		time INTEGER NOT NULL
	)`)
}

// ExternalAction is the audit action written for every run of the command.
const ExternalAction = "notify.external"

const (
	// externalQueue bounds the events waiting for the command. A burst past it
	// is dropped and audited: the inbox still holds every event.
	externalQueue = 32
	// drainGrace is how long stop waits for deliveries still running when
	// ADE exits; after that they are killed, so quitting never hangs on a
	// slow notifier.
	drainGrace = 3 * time.Second
	// stderrKeep is how much of a failing command's stderr goes to the audit log.
	stderrKeep = 512
)

// Payload is the JSON an external command reads on stdin.
type Payload struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"`
	Project  string    `json:"project,omitempty"`
	Subject  string    `json:"subject,omitempty"`
	Title    string    `json:"title"`
	Detail   string    `json:"detail,omitempty"`
	Link     string    `json:"link,omitempty"`
}

// NewPayload is e with credentials masked, the way the inbox stores it.
func NewPayload(e events.Event) Payload {
	return Payload{
		ID: e.ID, Time: e.Time.UTC(), Kind: e.Kind, Severity: e.Severity.String(),
		Project: e.Project, Subject: e.Subject,
		Title: audit.Redact(e.Title), Detail: audit.Redact(e.Detail), Link: audit.Redact(e.Link),
	}
}

// Env is the payload as ADE_EVENT_* variables, for commands that would
// rather not parse JSON.
func (p Payload) Env() []string {
	return []string{
		"ADE_EVENT_ID=" + p.ID,
		"ADE_EVENT_TIME=" + p.Time.Format(time.RFC3339),
		"ADE_EVENT_KIND=" + p.Kind,
		"ADE_EVENT_SEVERITY=" + p.Severity,
		"ADE_EVENT_PROJECT=" + p.Project,
		"ADE_EVENT_SUBJECT=" + p.Subject,
		"ADE_EVENT_TITLE=" + p.Title,
		"ADE_EVENT_DETAIL=" + p.Detail,
		"ADE_EVENT_LINK=" + p.Link,
	}
}

// Wanted reports whether the external command takes e: not transient, of a
// listed kind and at least the minimum severity.
func Wanted(c config.NotifyExternal, e events.Event) bool {
	if strings.TrimSpace(c.Command) == "" || e.Transient {
		return false
	}
	if e.Severity < events.ParseSeverity(c.MinSeverity) {
		return false
	}
	return matchAny(c.Kinds, e.Kind)
}

// Run executes the command once for e, waiting for it, and reports what it
// did. The command gets the JSON payload on stdin and ADE_EVENT_* variables;
// it runs in its own process group so a timeout takes its children along.
func Run(ctx context.Context, c config.NotifyExternal, e events.Event) error {
	if strings.TrimSpace(c.Command) == "" {
		return errors.New("notify.external.command is not set")
	}
	body, err := json.Marshal(NewPayload(e))
	if err != nil {
		return err
	}
	timeout := c.Timeout.Duration
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", c.Command)
	cmd.Stdin = bytes.NewReader(append(body, '\n'))
	cmd.Env = append(os.Environ(), NewPayload(e).Env()...)
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("timed out after %s", timeout)
	case err != nil:
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// limitedBuffer keeps the first stderrKeep bytes written to it.
type limitedBuffer struct{ buf bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := stderrKeep - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

// external delivers events to the configured command from one goroutine, so a
// slow or hung command never delays the bus consumer, the toasts or the TUI.
type external struct {
	cfg   config.NotifyExternal
	db    *sql.DB
	audit *audit.Log
	now   func() time.Time

	queue  chan events.Event
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func newExternal(ctx context.Context, cfg config.NotifyExternal, db *sql.DB, al *audit.Log) *external {
	ctx, cancel := context.WithCancel(ctx)
	x := &external{
		cfg: cfg, db: db, audit: al, now: time.Now,
		queue: make(chan events.Event, externalQueue), cancel: cancel, done: make(chan struct{}),
	}
	go x.loop(ctx)
	return x
}

// offer queues e when it matches the filter; it never blocks.
func (x *external) offer(e events.Event) {
	if !Wanted(x.cfg, e) {
		return
	}
	select {
	case x.queue <- e:
	default:
		x.record(e, 0, errors.New("dropped: the notification queue is full"))
	}
}

// stop closes the queue and waits for what is queued, at most drainGrace.
func (x *external) stop() {
	x.once.Do(func() {
		close(x.queue)
		select {
		case <-x.done:
		case <-time.After(drainGrace):
			x.cancel()
			<-x.done
		}
		x.cancel()
	})
}

func (x *external) loop(ctx context.Context) {
	defer close(x.done)
	for e := range x.queue {
		if ctx.Err() != nil {
			continue // abandoned at exit; the inbox has the event
		}
		if !x.claim(e) {
			continue
		}
		start := x.now()
		err := Run(ctx, x.cfg, e)
		x.record(e, x.now().Sub(start), err)
	}
}

// claim applies the dedupe window: it reports whether e may be sent, and
// remembers the send. The window is kept in cache.db so a sync outage that
// outlives one ade process (or one per cron run) stays quiet. A cache that
// cannot be read lets the event through.
func (x *external) claim(e events.Event) bool {
	window := x.cfg.Dedupe.Duration
	if window <= 0 {
		return true
	}
	key := e.Kind + "|" + e.Project + "|" + e.Severity.String()
	now := x.now()
	var last int64
	err := x.db.QueryRow(`SELECT time FROM external_sent WHERE key = ?`, key).Scan(&last)
	if err == nil && now.Sub(time.Unix(0, last)) < window {
		return false
	}
	if _, err := x.db.Exec(`INSERT INTO external_sent (key, time) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET time = excluded.time`, key, now.UnixNano()); err != nil {
		return true
	}
	return true
}

// record writes the delivery to the audit log; a failing command is logged
// there and nowhere else, so it never disturbs the screen or the command that
// raised the event.
func (x *external) record(e events.Event, took time.Duration, err error) {
	if x.audit == nil {
		return
	}
	entry := audit.Entry{
		Action: ExternalAction, Target: e.Kind, Project: e.Project, Item: e.Subject,
		Params: map[string]any{"event": e.ID, "kind": e.Kind, "severity": e.Severity.String(), "took_ms": took.Milliseconds()},
	}
	if err != nil {
		entry.Outcome, entry.Error = "error", err.Error()
	}
	x.audit.Append(entry) //nolint:errcheck // a failing audit write cannot be reported anywhere better
}
