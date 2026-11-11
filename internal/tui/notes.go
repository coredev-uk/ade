package tui

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/agent"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/records"
)

// The notes dialog (PLAN 4/6.1). `n` on a Home item shows the notes that live
// below `## Notes` in the item's record: yours, never rewritten by ADE. `e`
// opens the record in your editor at the notes (host.editor, else
// $VISUAL/$EDITOR). The notes are added to agent plan/run/address prompts and
// to AI summaries, draft replies and reviews, which the dialog says.

type notesDialog struct {
	ref       forge.Ref
	title     string
	forgeName string
	rec       records.Record
	// missing is true when the item has no record yet (the first sync writes it).
	missing bool
	scroll  int
	warn    string
}

// notesEditedMsg ends the editor: the record is read again.
type notesEditedMsg struct {
	d   *notesDialog
	err error
}

// notesExec hands the terminal to the editor. Tests replace it: no real
// editor is ever started.
var notesExec = func(c *exec.Cmd, done func(error) tea.Msg) tea.Cmd {
	return tea.ExecProcess(c, done)
}

// openNotes is Home's `n`: the notes of the selected item. It does nothing
// without an item row, or while another dialog is open.
func (m model) openNotes() (model, tea.Cmd) {
	if m.page != pageHome || m.mr != nil || m.isAgilePage() || m.sw != nil || m.cl != nil || m.agt != nil || m.nt != nil || m.wt != nil ||
		m.homeSel >= len(m.homeRows) || m.homeRows[m.homeSel].item == nil {
		return m, nil
	}
	it := m.homeRows[m.homeSel].item
	d := &notesDialog{ref: it.Ref, title: it.Issue.Title}
	for _, r := range m.results {
		if r.Project == it.Project {
			d.forgeName = r.Forge
			break
		}
	}
	m.nt = d
	m.notesLoad(d)
	return m, nil
}

// notesLoad reads the record from disk (a local file: no command needed).
func (m model) notesLoad(d *notesDialog) {
	d.warn, d.missing = "", false
	if m.app == nil || d.forgeName == "" {
		d.missing, d.warn = true, "the item's forge is unknown · press U to sync, then n again"
		return
	}
	rec, err := records.New(m.app.RecordsDir).Read(d.forgeName, d.ref)
	switch {
	case errors.Is(err, records.ErrNoRecord):
		d.missing, d.rec = true, rec
	case err != nil:
		d.warn = "the record could not be read · check the records directory (ade doctor)"
	default:
		d.rec = rec
	}
}

// notesUpdate routes the dialog's keys and the editor's end; while it is open
// every key but ctrl+c belongs to it.
func (m model) notesUpdate(msg tea.Msg) (model, tea.Cmd, bool) {
	d := m.nt
	switch msg := msg.(type) {
	case notesEditedMsg:
		if d == nil || msg.d != d {
			return m, nil, true
		}
		m.notesLoad(d)
		if msg.err != nil {
			d.warn = "the editor did not finish cleanly · what you saved is kept; press e to open it again"
		}
		return m, nil, true
	case tea.KeyPressMsg:
		if d == nil || msg.String() == "ctrl+c" {
			return m, nil, false
		}
		next, cmd := m.notesKey(msg.String())
		return next, cmd, true
	}
	return m, nil, false
}

func (m model) notesKey(ks string) (model, tea.Cmd) {
	d := m.nt
	switch ks {
	case "esc", "q":
		m.nt = nil
	case "j", "down":
		d.scroll++
	case "k", "up":
		d.scroll = max(d.scroll-1, 0)
	case "pgdown", "ctrl+d", "space", " ":
		d.scroll += 8
	case "pgup", "ctrl+u":
		d.scroll = max(d.scroll-8, 0)
	case "g", "home":
		d.scroll = 0
	case "e", "enter":
		return m.notesEdit(d)
	}
	return m, nil
}

// notesEdit opens the record in the editor at the notes. A record that lost
// its heading gets one first, so the cursor lands where the notes go.
func (m model) notesEdit(d *notesDialog) (model, tea.Cmd) {
	if d.missing {
		d.warn = "no record yet · press U to sync; the first sync writes it"
		return m, nil
	}
	st := records.New(m.app.RecordsDir)
	if d.rec.NotesLine == 0 {
		if err := st.WriteNotes(d.forgeName, d.ref, d.rec.Notes); err != nil {
			d.warn = "the record could not be written · check the records directory (ade doctor)"
			return m, nil
		}
		m.notesLoad(d)
	}
	c := m.hostEditor().Command(d.rec.Path, d.rec.NotesLine, true)
	return m, notesExec(c, func(err error) tea.Msg { return notesEditedMsg{d: d, err: err} })
}

// --- view --------------------------------------------------------------------

func (m model) notesOverlay(w, h int) string {
	d := m.nt
	if d == nil {
		return ""
	}
	dw := min(w, 100) // the shared dialog measure; blankFieldLayer centres it
	inner := dw - 4
	g := m.g
	l := []string{stFaint.Render(ansi.Truncate(d.title, inner, "…")), ""}
	guide := "yours · ADE never rewrites them"
	l = append(l, section("Notes", guide, inner, g))
	body := m.notesBody(d, inner, max(4, h-16))
	l = append(l, body...)
	l = append(l, "")
	l = append(l, stFaint.Render(ansi.Wrap("Added to agent plans, runs and address-comments prompts, and to AI summaries, draft replies and reviews. --no-notes leaves them out; they are part of the AI cache key.", inner, "")))
	if d.warn != "" {
		l = append(l, "", stWarn.Render(ansi.Wrap(g.Warn+" "+d.warn, inner, "")))
	}
	hint := keys("e", "edit in "+m.hostEditor().Name, "j/k", "scroll", "esc", "close")
	if d.missing {
		hint = keys("esc", "close")
	}
	l = append(l, "", hint)
	return m.dialog("Notes · "+d.ref.String(), l, dw)
}

// notesBody wraps the notes into at most rows lines from the scroll offset,
// clamping the offset to what exists and marking what is cut.
func (m model) notesBody(d *notesDialog, inner, rows int) []string {
	g := m.g
	if d.missing {
		return []string{stFaint.Render(g.Info + " no record for this item yet · press U to sync; the first sync writes it")}
	}
	text := strings.TrimSpace(d.rec.Notes)
	if text == "" {
		return []string{stFaint.Render(g.Info + " no notes yet · press e to write some")}
	}
	lines := wrap(text, inner)
	d.scroll = min(d.scroll, max(len(lines)-rows, 0))
	end := min(len(lines), d.scroll+rows)
	out := make([]string, 0, rows+2)
	if d.scroll > 0 {
		out = append(out, stFaint.Render(fmt.Sprintf("%s %d %s above", g.Up, d.scroll, plural(d.scroll, "line"))))
	}
	for _, ln := range lines[d.scroll:end] {
		out = append(out, ansi.Truncate(ln, inner, "…"))
	}
	if end < len(lines) {
		out = append(out, stFaint.Render(fmt.Sprintf("%s %d more %s · j scrolls", g.Down, len(lines)-end, plural(len(lines)-end, "line"))))
	}
	return out
}

func (m model) notesOverlayLayer(w, h int) *lipgloss.Layer {
	return blankFieldLayer(m.notesOverlay(w, h), w, h)
}

// itemNotes is the text of the item's notes ("" when none or unreadable): what
// the agent menu offers to include in a run.
func (m model) itemNotes(it *domain.WorkItem) string {
	if m.app == nil || it == nil {
		return ""
	}
	for _, r := range m.results {
		if r.Project == it.Project && r.Forge != "" {
			n, _ := records.New(m.app.RecordsDir).ReadNotes(r.Forge, it.Ref)
			return strings.TrimSpace(n)
		}
	}
	return ""
}

// agentNotes adds the user's notes on the target's records to a run's context.
func agentNotes(c *agent.Context, a *app.App, t agent.Target) error {
	return c.AddNotes(records.New(a.RecordsDir), t)
}
