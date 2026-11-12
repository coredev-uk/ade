package overview

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Options configure the live pane.
type Options struct {
	Loader *Loader
	// Glyphs is host.glyphs ("ascii", else unicode).
	Glyphs string
	// Changes signals that the cache or the records may have changed (see
	// internal/watch); the pane re-reads on each signal, and only when the
	// cache or the record really changed. Nil never refreshes by itself.
	Changes <-chan struct{}
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Edit builds the command that opens the item's record at its notes; nil
	// leaves `e` out.
	Edit func(Snapshot) (*exec.Cmd, error)
}

type (
	changedMsg struct{ open bool } // the watcher signalled (open false: it is gone)
	loadedMsg  struct {
		snap     Snapshot
		reloaded bool
		err      error
	}
	editedMsg struct{}
)

type model struct {
	opts Options
	g    glyphs
	snap Snapshot
	gone bool  // the item was cached once and no longer is
	err  error // the last reload failure; the last good view stays
	w, h int
	off  int
}

func newModel(o Options) model {
	if o.Now == nil {
		o.Now = time.Now
	}
	return model{opts: o, g: glyphSet(o.Glyphs), snap: Snapshot{Ref: o.Loader.Ref}, w: 80, h: 24}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.pollCmd(), m.waitCmd())
}

// waitCmd waits for the next change signal.
func (m model) waitCmd() tea.Cmd {
	ch := m.opts.Changes
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		_, ok := <-ch
		return changedMsg{open: ok}
	}
}

// pollCmd reads off the UI loop, only when the cache or the record changed.
func (m model) pollCmd() tea.Cmd {
	l := m.opts.Loader
	return func() tea.Msg {
		snap, reloaded, err := l.Poll()
		return loadedMsg{snap: snap, reloaded: reloaded, err: err}
	}
}

func (m model) reloadCmd() tea.Cmd {
	l := m.opts.Loader
	return func() tea.Msg {
		snap, err := l.Load()
		return loadedMsg{snap: snap, reloaded: true, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.off = m.clamp(m.off)
	case changedMsg:
		if !msg.open {
			return m, nil
		}
		return m, tea.Batch(m.pollCmd(), m.waitCmd())
	case loadedMsg:
		m = m.loaded(msg)
	case editedMsg:
		return m, m.reloadCmd()
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m model) loaded(msg loadedMsg) model {
	if !msg.reloaded {
		return m
	}
	switch {
	case msg.err == nil:
		m.snap, m.gone, m.err = msg.snap, false, nil
	case errors.Is(msg.err, ErrNotCached):
		m.err = nil
		if m.snap.Found {
			m.gone = true // keep the last view
		}
	default:
		m.err = msg.err
	}
	m.off = m.clamp(m.off)
	return m
}

func (m model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	page := max(1, m.viewport()-1)
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.off = m.clamp(m.off + 1)
	case "k", "up":
		m.off = m.clamp(m.off - 1)
	case "pgdown", "space", "ctrl+d":
		m.off = m.clamp(m.off + page)
	case "pgup", "ctrl+u":
		m.off = m.clamp(m.off - page)
	case "g", "home":
		m.off = 0
	case "G", "end":
		m.off = m.clamp(1 << 20)
	case "r":
		return m, m.reloadCmd()
	case "e":
		if m.opts.Edit == nil || m.snap.NotesPath == "" {
			return m, nil
		}
		c, err := m.opts.Edit(m.snap)
		if err != nil {
			m.err = err
			return m, nil
		}
		return m, tea.ExecProcess(c, func(error) tea.Msg { return editedMsg{} })
	}
	return m, nil
}

// viewport is the number of body lines that fit above the footer (and the
// failure line, when there is one).
func (m model) viewport() int {
	n := m.h - 1
	if m.err != nil {
		n--
	}
	return max(n, 1)
}

func (m model) lines() []string {
	return body(m.snap, m.g, max(m.w, minWidth), m.gone)
}

func (m model) clamp(off int) int {
	return max(0, min(off, len(m.lines())-m.viewport()))
}

func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "ADE"
	return v
}

func (m model) render() string {
	w := max(m.w, minWidth)
	all := m.lines()
	vp := m.viewport()
	shown := all
	more := 0
	if len(all) > vp {
		off := max(0, min(m.off, len(all)-vp))
		shown = append([]string(nil), all[off:off+vp]...)
		if off > 0 {
			shown[0] = " " + stFaint.Render(fmt.Sprintf(m.g.T(m.g.T(m.g.T("… %d above"))), off))
		}
		if more = len(all) - off - vp; more > 0 {
			shown[len(shown)-1] = " " + stFaint.Render(fmt.Sprintf(m.g.T(m.g.T(m.g.T("… %d more"))), more))
		}
	}
	out := strings.Join(shown, "\n")
	if pad := vp - len(shown); pad > 0 {
		out += strings.Repeat("\n", pad) // the footer sits on the last row
	}
	if m.err != nil {
		out += "\n " + stBad.Render(ansi.Truncate(m.g.Bad+" "+m.err.Error(), w-2, m.g.Ell))
	}
	keys := []string{"j/k scroll", "r reload"}
	if m.opts.Edit != nil && m.snap.NotesPath != "" {
		keys = append(keys, "e notes")
	}
	keys = append(keys, "q quit")
	return out + "\n" + footer(m.snap, w, m.opts.Now(), keys)
}
