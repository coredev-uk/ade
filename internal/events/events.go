// Package events is ADE's in-process event bus. Subsystems publish typed events
// (git.pull.failed, task.done, ci.failed, ...); the notifier and TUI subscribe.
package events

import (
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

type Severity int

const (
	Info Severity = iota
	Success
	Warn
	Error
)

func (s Severity) String() string {
	switch s {
	case Success:
		return "success"
	case Warn:
		return "warn"
	case Error:
		return "error"
	default:
		return "info"
	}
}

// ParseSeverity is the inverse of String; unknown values map to Info.
func ParseSeverity(s string) Severity {
	switch s {
	case "success":
		return Success
	case "warn":
		return Warn
	case "error":
		return Error
	default:
		return Info
	}
}

// Event is one notification-worthy occurrence.
type Event struct {
	ID       string
	Time     time.Time
	Kind     string // dotted, e.g. "git.pull.failed"
	Severity Severity
	Project  string // project name, when scoped to one
	Subject  string // what it is about: "api/main", "acme/api!12", task ref
	Title    string // one line, shown in toasts and the inbox list
	Detail   string // multi-line detail (stderr, log tail)
	Link     string // URL or ade-internal target
}

// New fills ID and Time.
func New(kind string, sev Severity, title string) Event {
	return Event{ID: ulid.Make().String(), Time: time.Now(), Kind: kind, Severity: sev, Title: title}
}

// Bus fans events out to subscribers. Publish never blocks: a subscriber whose
// buffer is full misses the event (the inbox store remains the durable record).
type Bus struct {
	mu   sync.Mutex
	subs map[int]chan Event
	next int
}

func NewBus() *Bus { return &Bus{subs: map[int]chan Event{}} }

// Publish delivers e to every subscriber, filling ID/Time when empty.
func (b *Bus) Publish(e Event) {
	if e.ID == "" {
		e.ID = ulid.Make().String()
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribe returns a channel of events and a cancel func that closes it.
func (b *Bus) Subscribe(buf int) (<-chan Event, func()) {
	ch := make(chan Event, buf)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
			close(ch)
		})
	}
}
