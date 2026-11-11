package notify

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
)

// Channels selects which delivery channels are live in this process. The inbox
// is always written; a zero Channels is what CLI commands use. Notifications
// are shown only inside ADE: terminal escape sequences and desktop notifiers
// are unreliable across the terminals ADE runs in (VS Code, browser terminals,
// zellij), so ADE never emits them.
type Channels struct {
	// Toast receives every event whose kind matches notify.toast; the
	// dashboard shows it as a pop-up and marks the subject's row.
	Toast func(events.Event)
}

// busBuffer is large so a burst (an update across many repos) is not dropped
// before the consumer goroutine catches up.
const busBuffer = 4096

type notifier struct {
	inbox *Inbox
	cfg   config.Notify
	ch    Channels
}

// Attach subscribes to the app's bus, records every non-transient event in the
// inbox and delivers toasts per config rules. stop unsubscribes and returns
// only after every event published before the call has been handled.
func Attach(ctx context.Context, a *app.App, ch Channels) (stop func(), err error) {
	st, err := a.Cache()
	if err != nil {
		return nil, fmt.Errorf("open inbox: %w", err)
	}
	n := &notifier{inbox: NewInbox(st), cfg: a.Config.Notify, ch: ch}
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

func (n *notifier) handle(_ context.Context, e events.Event) {
	if !e.Transient {
		// Delivery is best-effort; a full disk must not stop toasts.
		if err := n.inbox.Record(e); err != nil {
			fmt.Fprintln(os.Stderr, "ade: notify:", err)
		}
	}
	if n.ch.Toast != nil && matchAny(n.cfg.Toast, e.Kind) {
		n.ch.Toast(e)
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
