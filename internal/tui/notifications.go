package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/notify"
)

// ADE shows notifications only inside the dashboard: a stack of pop-ups,
// a notification centre (inbox drawer), the unread count in the header and a
// marker on the row each unread notification is about. Events recorded by
// other ade processes (CLI commands, tasks, agents) are picked up by polling
// the inbox and popped up like local ones.

const (
	toastTTL    = 6 * time.Second
	maxToasts   = 3
	inboxWindow = 200 // newest rows the centre keeps in memory
)

type toast struct {
	ev      events.Event
	expires time.Time // zero: stays until dismissed (failures)
}

type notifFilter int

const (
	filterAll notifFilter = iota
	filterUnread
	filterProblems
)

func (f notifFilter) String() string {
	return [...]string{"all", "unread", "problems"}[f]
}

// notifCenter is shared by pointer so value-receiver models can mutate it.
type notifCenter struct {
	in     *notify.Inbox
	items  []notify.Item // newest first
	unread int
	seen   map[string]bool // ids already popped up (or present at start)
	primed bool            // first poll done: older rows never pop up
	toasts []toast
	open   bool
	sel    int
	filter notifFilter
	now    func() time.Time
}

func newNotifCenter(in *notify.Inbox) *notifCenter {
	return &notifCenter{in: in, seen: map[string]bool{}, now: time.Now}
}

type inboxMsg struct {
	items  []notify.Item
	unread int
	err    error
}

func (c *notifCenter) poll() tea.Cmd {
	return func() tea.Msg {
		items, err := c.in.List(notify.ListOpts{Limit: inboxWindow})
		if err != nil {
			return inboxMsg{err: err}
		}
		n, err := c.in.UnreadCount()
		return inboxMsg{items: items, unread: n, err: err}
	}
}

// onInbox refreshes the centre and pops up rows this process has not shown
// yet: those were recorded by another ade process.
func (c *notifCenter) onInbox(msg inboxMsg) {
	if msg.err != nil {
		return
	}
	c.items, c.unread = msg.items, msg.unread
	for i := len(msg.items) - 1; i >= 0; i-- { // oldest first, so the newest ends on top
		it := msg.items[i]
		if c.seen[it.ID] {
			continue
		}
		c.seen[it.ID] = true
		if c.primed && !it.Read {
			c.push(it.Event)
		}
	}
	c.primed = true
	c.sel = min(c.sel, max(len(c.visible())-1, 0))
}

// onEvent pops up an event published in this process.
func (c *notifCenter) onEvent(e events.Event) {
	c.seen[e.ID] = true
	c.push(e)
}

func (c *notifCenter) push(e events.Event) {
	t := toast{ev: e}
	// Failures stay, and so do a blocked or exited task: their pop-ups say what to do next.
	if e.Severity != events.Error && !strings.HasSuffix(e.Kind, ".blocked") && !strings.HasSuffix(e.Kind, ".exited") {
		t.expires = c.now().Add(toastTTL)
	}
	c.toasts = append(c.toasts, t)
}

func (c *notifCenter) tick(now time.Time) {
	out := c.toasts[:0]
	for _, t := range c.toasts {
		if t.expires.IsZero() || now.Before(t.expires) {
			out = append(out, t)
		}
	}
	c.toasts = out
}

func (c *notifCenter) dismissToasts() bool {
	if len(c.toasts) == 0 {
		return false
	}
	c.toasts = c.toasts[:0]
	return true
}

func (c *notifCenter) visible() []notify.Item {
	if c.filter == filterAll {
		return c.items
	}
	var out []notify.Item
	for _, it := range c.items {
		if (c.filter == filterUnread && !it.Read) || (c.filter == filterProblems && it.Severity >= events.Warn) {
			out = append(out, it)
		}
	}
	return out
}

// markKey identifies a row a notification is about.
func markKey(project, subject string) string { return project + "\x00" + subject }

// marks counts unread notifications per (project, subject).
func (c *notifCenter) marks() map[string]int {
	m := map[string]int{}
	for _, it := range c.items {
		if !it.Read && it.Subject != "" {
			m[markKey(it.Project, it.Subject)]++
		}
	}
	return m
}

// key handles keys while the centre is open. jump is the item to navigate to
// on enter (the caller selects its subject); it is marked read.
func (c *notifCenter) key(k string) (cmd tea.Cmd, jump *notify.Item) {
	vis := c.visible()
	switch k {
	case "j", "down":
		c.sel = min(c.sel+1, max(len(vis)-1, 0))
	case "k", "up":
		c.sel = max(c.sel-1, 0)
	case "f":
		c.filter = (c.filter + 1) % 3
		c.sel = 0
	case "r":
		if c.sel < len(vis) {
			c.in.MarkRead(vis[c.sel].ID) //nolint:errcheck // reflected on the next poll
			return c.poll(), nil
		}
	case "R":
		c.in.MarkAllRead() //nolint:errcheck // reflected on the next poll
		return c.poll(), nil
	case "enter":
		if c.sel < len(vis) {
			it := vis[c.sel]
			c.in.MarkRead(it.ID) //nolint:errcheck // reflected on the next poll
			c.open = false
			return c.poll(), &it
		}
	case "i", "esc", "q":
		c.open = false
	}
	return nil, nil
}

// --- rendering ------------------------------------------------------------------

func (c *notifCenter) drawer(g glyphs, w, h int) string {
	vis := c.visible()
	title := stBold.Render("Notifications") + stFaint.Render(fmt.Sprintf(" %s %d unread %s showing %s", g.Sep, c.unread, g.Sep, c.filter))
	l := []string{title, ""}
	if len(vis) == 0 {
		msg := "Nothing here yet. Updates, CI results, review activity, task and agent results land here."
		if c.filter != filterAll {
			msg = fmt.Sprintf("No %s notifications. f cycles the filter.", c.filter)
		}
		l = append(l, lipgloss.Wrap(stFaint.Render(msg), w-4, " "))
	}
	room := h - 6
	for i, it := range vis {
		if len(l) >= room {
			l = append(l, stFaint.Render(fmt.Sprintf(g.T("  … %d more"), len(vis)-i)))
			break
		}
		l = append(l, inboxLine(g, it, w-4, i == c.sel))
		meta := it.Kind
		if it.Subject != "" {
			meta += " " + g.Sep + " " + it.Subject
		}
		l = append(l, "    "+stFaint.Render(ansi.Truncate(meta, w-8, g.Ell)))
		if i == c.sel && it.Detail != "" {
			lines := strings.Split(strings.TrimSpace(it.Detail), "\n")
			for _, d := range lines[:min(4, len(lines))] {
				l = append(l, "    "+stRule.Render(g.Vertical)+" "+ansi.Truncate(d, w-10, g.Ell))
			}
		}
	}
	return lipgloss.NewStyle().Border(g.Border).BorderForeground(lipgloss.Color("#666666")).
		Padding(0, 1).Width(w).Height(h).Render(strings.Join(l, "\n"))
}

// toastStack renders the newest pop-ups, newest at the bottom, as one block.
func (c *notifCenter) toastStack(g glyphs, w int) string {
	if len(c.toasts) == 0 {
		return ""
	}
	shown := c.toasts[max(0, len(c.toasts)-maxToasts):]
	var boxes []string
	if hidden := len(c.toasts) - len(shown); hidden > 0 {
		boxes = append(boxes, stFaint.Render(fmt.Sprintf("+%d earlier %s esc dismisses all", hidden, g.Sep)))
	}
	for _, t := range shown {
		boxes = append(boxes, toastBox(g, t, w))
	}
	return lipgloss.JoinVertical(lipgloss.Right, boxes...)
}

func toastBox(g glyphs, t toast, w int) string {
	inner := w - 4
	var meta []string // what it is about, never the internal event id
	if t.ev.Subject != "" {
		meta = append(meta, t.ev.Subject)
	}
	persistent := t.expires.IsZero()
	// Text wraps (five lines at most) rather than being cut at the box edge: the
	// end of a title or subject is often the instruction.
	body := wrapHanging(g, sevGlyph(g, t.ev.Severity)+" ", t.ev.Title, inner, false, 5)
	if len(meta) > 0 {
		body += "\n" + wrapHanging(g, "  ", strings.Join(meta, " "+g.Sep+" "), inner, true, 5)
	}
	if persistent {
		body += "\n" + stFaint.Render("  esc dismiss")
	}
	return lipgloss.NewStyle().Border(g.Border).BorderForeground(sevBorder(t.ev.Severity)).
		Padding(0, 1).Width(w).Render(body)
}

// wrapHanging wraps text to w cells under a lead (a glyph or indent) that only
// the first line carries; later lines are indented to match. At most maxLines
// lines are kept, the last one cut with an ellipsis. faint dims the text.
func wrapHanging(g glyphs, lead, text string, w int, faint bool, maxLines int) string {
	room := max(w-lipgloss.Width(lead), 8)
	lines := strings.Split(lipgloss.Wrap(text, room, " "), "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] = ansi.Truncate(lines[maxLines-1]+g.T(" …"), room, g.Ell)
	}
	indent := strings.Repeat(" ", lipgloss.Width(lead))
	for i, l := range lines {
		if faint {
			l = stFaint.Render(l)
		}
		if i == 0 {
			lines[i] = lead + l
		} else {
			lines[i] = indent + l
		}
	}
	return strings.Join(lines, "\n")
}

func sevBorder(s events.Severity) color.Color {
	switch s {
	case events.Error:
		return lipgloss.Color(colBad)
	case events.Warn:
		return lipgloss.Color(colWarn)
	case events.Success:
		return lipgloss.Color(colOK)
	default:
		return lipgloss.Color(colRule)
	}
}

func inboxLine(g glyphs, it notify.Item, w int, selected bool) string {
	title := it.Title
	if !it.Read {
		title = stBold.Render(title)
	}
	when := stFaint.Render(ago(it.Time))
	mark := "  "
	if selected {
		mark = stBold.Render(g.Selected) + " "
	}
	left := mark + sevGlyph(g, it.Severity) + " "
	room := max(4, w-lipgloss.Width(left)-lipgloss.Width(when)-2)
	return left + pad(g, ansi.Truncate(title, room, g.Ell), room) + "  " + when
}
