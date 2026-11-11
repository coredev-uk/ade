package notify

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
)

// Channels selects which delivery channels are live in this process. The inbox
// is always written; a zero Channels is what CLI commands use.
type Channels struct {
	Toast    func(events.Event)
	Terminal io.Writer
	Desktop  bool
}

// busBuffer is large so a burst (an update across many repos) is not dropped
// before the consumer goroutine catches up.
const busBuffer = 4096

type notifier struct {
	inbox *Inbox
	cfg   config.Notify
	ch    Channels
	now   func() time.Time
	// notifySend is the resolved desktop notifier binary, empty when desktop
	// delivery is unavailable.
	notifySend string
}

// Attach subscribes to the app's bus, records every event in the inbox and
// delivers to channels per config rules. stop unsubscribes and returns only
// after every event published before the call has been handled.
func Attach(ctx context.Context, a *app.App, ch Channels) (stop func(), err error) {
	st, err := a.Cache()
	if err != nil {
		return nil, fmt.Errorf("open inbox: %w", err)
	}
	n := &notifier{inbox: NewInbox(st), cfg: a.Config.Notify, ch: ch, now: time.Now}
	if ch.Desktop {
		n.notifySend = desktopBinary()
	}
	sub, cancel := a.Bus.Subscribe(busBuffer)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range sub { // cancel closes sub; buffered events still drain
			n.handle(ctx, e)
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}, nil
}

func desktopBinary() string {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return ""
	}
	p, err := exec.LookPath("notify-send")
	if err != nil {
		return ""
	}
	return p
}

func (n *notifier) handle(ctx context.Context, e events.Event) {
	if e.Transient {
		if n.ch.Toast != nil && matchAny(n.cfg.Toast, e.Kind) {
			n.ch.Toast(e)
		}
		return
	}
	// Delivery is best-effort; a full disk must not stop toasts.
	if err := n.inbox.Record(e); err != nil {
		fmt.Fprintln(os.Stderr, "ade: notify:", err)
	}
	if n.ch.Toast != nil && matchAny(n.cfg.Toast, e.Kind) {
		n.ch.Toast(e)
	}
	if inQuiet(n.cfg.Quiet, n.now()) {
		return
	}
	if n.ch.Terminal != nil && matchAny(n.cfg.Terminal, e.Kind) {
		_, _ = io.WriteString(n.ch.Terminal, terminalSequence(e))
	}
	if n.notifySend != "" && matchAny(n.cfg.Desktop, e.Kind) {
		sendDesktop(ctx, n.notifySend, e)
	}
}

func matchAny(globs []string, kind string) bool {
	for _, g := range globs {
		if audit.GlobMatch(g, kind) {
			return true
		}
	}
	return false
}

// inQuiet reports whether t falls in the "HH:MM-HH:MM" window (local time),
// which may wrap midnight. Empty or malformed windows are never quiet.
func inQuiet(window string, t time.Time) bool {
	from, to, ok := parseWindow(window)
	if !ok || from == to {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	if from < to {
		return m >= from && m < to
	}
	return m >= from || m < to
}

func parseWindow(w string) (from, to int, ok bool) {
	a, b, found := strings.Cut(w, "-")
	if !found {
		return 0, 0, false
	}
	from, ok1 := parseClock(a)
	to, ok2 := parseClock(b)
	return from, to, ok1 && ok2
}

func parseClock(s string) (int, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// terminalSequence builds one OSC 9 desktop notification ("title: body").
// One sequence, not several: terminals that understand both OSC 9 and OSC 777
// would otherwise notify twice, and zellij rewrites a pane's OSC 777 into
// OSC 9 anyway (verified with zellij 0.45). No BEL: zellij drops it and
// elsewhere it only beeps.
func terminalSequence(e events.Event) string {
	title, body := sanitize(e.Title), sanitize(e.Detail)
	if body == "" {
		body = sanitize(e.Subject)
	}
	msg := title
	if body != "" {
		msg += ": " + body
	}
	return "\x1b]9;" + msg + "\a"
}

// sanitize drops control characters (and ';', the OSC 777 field separator) so
// event text cannot terminate the sequence early or inject escapes. Only the
// first line is kept: notifications are single-line.
func sanitize(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == ';':
			return ','
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

func sendDesktop(ctx context.Context, bin string, e events.Event) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	urgency := map[events.Severity]string{events.Warn: "normal", events.Error: "critical"}[e.Severity]
	if urgency == "" {
		urgency = "low"
	}
	body := sanitize(e.Detail)
	if body == "" {
		body = sanitize(e.Subject)
	}
	cmd := exec.CommandContext(ctx, bin, "--app-name=ade", "--urgency="+urgency, "--", sanitize(e.Title), body)
	_ = cmd.Run() // best effort
}
