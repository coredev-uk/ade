package tui

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/agent"
	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
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
	item      domain.WorkItem // as the cache holds it: the record is made from it when missing
	// missing is true when the record could not be made or read.
	missing bool
	scroll  int
	warn    string

	// adding is the quick-add form (`N`): one line of text, saved as a dated
	// note. A leading "!" or tab marks it blocked.
	adding  bool
	input   string
	blocked bool
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
func (m model) openNotes() (model, tea.Cmd) { return m.openNotesDialog(false) }

// openNoteAdd is Home's `N`: the one-line form that adds a dated note.
func (m model) openNoteAdd() (model, tea.Cmd) { return m.openNotesDialog(true) }

func (m model) openNotesDialog(adding bool) (model, tea.Cmd) {
	if m.page != pageHome || m.mr != nil || m.isAgilePage() || m.sw != nil || m.cl != nil || m.agt != nil || m.nt != nil || m.wt != nil ||
		m.homeSel >= len(m.homeRows) || m.homeRows[m.homeSel].item == nil {
		return m, nil
	}
	it := m.homeRows[m.homeSel].item
	d := &notesDialog{ref: it.Ref, title: it.Issue.Title, item: *it, adding: adding}
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
		d.missing, d.warn = true, m.g.T("the item's forge is unknown · press U to sync, then n again")
		return
	}
	st := records.New(m.app.RecordsDir)
	rec, err := st.Read(d.forgeName, d.ref)
	if errors.Is(err, records.ErrNoRecord) {
		// The record is written by a sync, but notes must not wait for one:
		// make it from the cached item (the next sync adds the comments).
		if _, _, werr := st.WriteSnapshot(d.forgeName, d.item, nil, m.app.Config.Records.SnapshotComments); werr == nil {
			rec, err = st.Read(d.forgeName, d.ref)
		}
	}
	switch {
	case errors.Is(err, records.ErrNoRecord):
		d.missing, d.rec = true, rec
	case err != nil:
		d.warn = m.g.T("the record could not be read · check the records directory (ade doctor)")
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
		m.loadNotes()
		if msg.err != nil {
			d.warn = m.g.T("the editor did not finish cleanly · what you saved is kept; press e to open it again")
		}
		return m, nil, true
	case tea.KeyPressMsg:
		if d == nil || msg.String() == "ctrl+c" {
			return m, nil, false
		}
		if d.adding {
			next, cmd := m.noteAddKey(msg)
			return next, cmd, true
		}
		next, cmd := m.notesKey(msg.String())
		return next, cmd, true
	}
	return m, nil, false
}

// noteAddKey edits the quick-add line: enter saves, esc drops it.
func (m model) noteAddKey(k tea.KeyPressMsg) (model, tea.Cmd) {
	d := m.nt
	switch k.String() {
	case "esc":
		m.nt = nil
	case "enter":
		return m.noteSave(d)
	case "tab":
		d.blocked = !d.blocked
	case "ctrl+u":
		d.input = ""
	case "backspace", "ctrl+h":
		r := []rune(d.input)
		if len(r) > 0 {
			d.input = string(r[:len(r)-1])
		}
	default:
		if k.Text != "" && !strings.ContainsAny(k.Text, "\r\n\t") {
			d.input += k.Text
		}
	}
	return m, nil
}

// noteSave appends the line to the item's notes and refreshes the notes the
// panel and rows show. A failed write keeps the form open with the reason.
func (m model) noteSave(d *notesDialog) (model, tea.Cmd) {
	text, blocked := strings.TrimSpace(d.input), d.blocked
	if rest, ok := strings.CutPrefix(text, "!"); ok {
		text, blocked = strings.TrimSpace(rest), true
	}
	switch {
	case d.missing:
		d.warn = m.g.T("the record could not be made · check the records directory (ade doctor)")
	case text == "":
		d.warn = m.g.T("write the note first · esc cancels")
	default:
		if err := records.New(m.app.RecordsDir).AppendNote(d.forgeName, d.ref, text, time.Now().In(ai.Location(m.app.Config)), blocked); err != nil {
			d.warn = m.g.T("the note could not be saved · check the records directory (ade doctor)")
			break
		}
		m.loadNotes()
		m.nt = nil
		m.notice(events.Info, "Note saved on "+d.ref.String())
	}
	return m, nil
}

// loadNotes reads every listed item's dated notes once, into m.notes; Home's
// rows and detail pane read that map, never the files. It runs on each
// refresh and after a note is added or edited.
func (m *model) loadNotes() {
	m.notes = map[string][]records.Note{}
	if m.app == nil {
		return
	}
	st := records.New(m.app.RecordsDir)
	for _, r := range m.results {
		if r.Forge == "" {
			continue
		}
		for i := range r.Items {
			text, err := st.ReadNotes(r.Forge, r.Items[i].Ref)
			if err != nil {
				continue
			}
			if notes := records.ParseNotes(text); len(notes) > 0 {
				m.notes[markKey(r.Project, r.Items[i].Ref.String())] = notes
			}
		}
	}
}

// itemDatedNotes is the item's dated notes, newest first.
func (m model) itemDatedNotes(it *domain.WorkItem) []records.Note {
	return m.notes[markKey(it.Project, it.Ref.String())]
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
		d.warn = m.g.T("the record could not be made · check the records directory (ade doctor)")
		return m, nil
	}
	st := records.New(m.app.RecordsDir)
	if d.rec.NotesLine == 0 {
		if err := st.WriteNotes(d.forgeName, d.ref, d.rec.Notes); err != nil {
			d.warn = m.g.T("the record could not be written · check the records directory (ade doctor)")
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
	if d.adding {
		return m.noteAddOverlay(d, dw, inner)
	}
	l := []string{stFaint.Render(ansi.Truncate(d.title, inner, m.g.Ell)), ""}
	guide := m.g.T("yours · ADE never rewrites them")
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
	return m.dialog(m.g.T("Notes · ")+d.ref.String(), l, dw)
}

// noteAddOverlay is the quick-add form: the item, the newest note for
// context, the input line and its keys.
func (m model) noteAddOverlay(d *notesDialog, dw, inner int) string {
	g := m.g
	l := []string{stFaint.Render(ansi.Truncate(d.title, inner, g.Ell)), ""}
	if prev := m.itemDatedNotes(&d.item); len(prev) > 0 {
		l = append(l, section("Last note", "", inner, g), noteLine(g, prev[0], inner), "")
	}
	input := d.input
	if rest, ok := strings.CutPrefix(strings.TrimSpace(input), "!"); ok && !d.blocked {
		input = "[blocked] " + strings.TrimSpace(rest)
	} else if d.blocked {
		input = "[blocked] " + input
	}
	l = append(l, g.Prompt+" "+inputTail(input, max(inner-3, 1), g.Ell)+g.Cursor)
	if d.warn != "" {
		l = append(l, "", stWarn.Render(ansi.Wrap(g.Warn+" "+d.warn, inner, "")))
	}
	l = append(l, "", keys("enter", "save", "tab or !", "blocked", "esc", "cancel"))
	return m.dialog(m.g.T("Note · ")+d.ref.String(), l, dw)
}

// inputTail is the end of the text typed so far, w columns at most: the part
// being typed stays visible.
func inputTail(s string, w int, ell string) string {
	if n := ansi.StringWidth(s); n > w {
		return ell + ansi.TruncateLeft(s, n-w+ansi.StringWidth(ell), "")
	}
	return s
}

// noteLine is one dated note on one line, blocked ones in the warn style.
func noteLine(g glyphs, n records.Note, w int) string {
	stamp := n.At.Format("2006-01-02 15:04")
	if n.Blocked {
		return stWarn.Render(ansi.Truncate(g.Warn+" "+stamp+" blocked: "+n.Text, w, g.Ell))
	}
	return stFaint.Render(stamp+" ") + ansi.Truncate(n.Text, max(w-len(stamp)-1, 1), g.Ell)
}

// notesBody wraps the notes into at most rows lines from the scroll offset,
// clamping the offset to what exists and marking what is cut.
func (m model) notesBody(d *notesDialog, inner, rows int) []string {
	g := m.g
	if d.missing {
		return []string{stFaint.Render(g.Info + m.g.T(" no record for this item, and it could not be made · check the records directory (ade doctor)"))}
	}
	text := strings.TrimSpace(d.rec.Notes)
	if text == "" {
		return []string{stFaint.Render(g.Info + m.g.T(" no notes yet · press e to write some"))}
	}
	lines := wrap(text, inner)
	d.scroll = min(d.scroll, max(len(lines)-rows, 0))
	end := min(len(lines), d.scroll+rows)
	out := make([]string, 0, rows+2)
	if d.scroll > 0 {
		out = append(out, stFaint.Render(fmt.Sprintf("%s %d %s above", g.Up, d.scroll, plural(d.scroll, "line"))))
	}
	for _, ln := range lines[d.scroll:end] {
		out = append(out, ansi.Truncate(ln, inner, m.g.Ell))
	}
	if end < len(lines) {
		out = append(out, stFaint.Render(fmt.Sprintf(m.g.T("%s %d more %s · j scrolls"), g.Down, len(lines)-end, plural(len(lines)-end, "line"))))
	}
	return out
}

func (m model) notesOverlayLayer(w, h int) *lipgloss.Layer {
	return blankFieldLayer(m.g, m.notesOverlay(w, h), w, h)
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
