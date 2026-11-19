package tui

import (
	"errors"
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
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/records"
)

// Notes (PLAN 4/6.1) live below `## Notes` in the item's record: yours, never
// rewritten by ADE. Home's detail pane shows them as written. `n` turns that
// pane into an editor in place (noteEdit); ctrl+e there hands the record to
// host.editor (else $VISUAL/$EDITOR) at the notes. `N` adds one dated line
// (the quick-add form), whose [blocked] mark drives the row's marker. The notes
// go into agent plan/run/address prompts and AI summaries, draft replies and
// reviews.

// notesDialog is the one-item dialog in m.nt: the quick-add form (`N`), or the
// item's research when rs is set (research.go).
type notesDialog struct {
	ref       forge.Ref
	title     string
	forgeName string
	item      domain.WorkItem // as the cache holds it: the record is made from it when missing
	// missing is true when the record could not be made or read.
	missing bool
	scroll  int
	warn    string

	// input is the quick-add line, saved as a dated note; blocked (tab, or a
	// leading "!") marks it [blocked].
	input   string
	blocked bool

	rs *researchState
}

// noteEdit is the detail pane while its notes are edited (`n`): the textarea
// holds the text until it is saved.
type noteEdit struct {
	key       string // markKey(project, ref)
	forgeName string
	item      domain.WorkItem
	orig      string // the notes when the edit began: an unchanged text is not written
	ed        editor
	warn      string
}

// itemNote is one item's notes as Home shows them: the text under "## Notes"
// (trimmed) and its dated lines, newest first.
type itemNote struct {
	text  string
	dated []records.Note
}

// notesEditedMsg ends $EDITOR: the notes are read again.
type notesEditedMsg struct{ err error }

// recordsLoadedMsg is the listed items' notes and research, read off the UI
// loop after the records directory changed. gen is m.notesGen when the read
// began: an answer older than a local update is dropped.
type recordsLoadedMsg struct {
	gen      int
	notes    map[string]itemNote
	research map[string]records.Research
}

// notesExec hands the terminal to the editor. Tests replace it: no real
// editor is ever started.
var notesExec = func(c *exec.Cmd, done func(error) tea.Msg) tea.Cmd {
	return tea.ExecProcess(c, done)
}

// itemForge is the forge the item's project syncs from ("" when unknown).
func (m model) itemForge(it *domain.WorkItem) string {
	for _, r := range m.results {
		if r.Project == it.Project {
			return r.Forge
		}
	}
	return ""
}

// itemRecord reads the item's record. A record no sync has written yet is made
// from the cached item first (the next sync adds the comments): notes never
// wait for a sync.
func (m model) itemRecord(forgeName string, it domain.WorkItem) (records.Record, error) {
	st := records.New(m.app.RecordsDir)
	rec, err := st.Read(forgeName, it.Ref)
	if errors.Is(err, records.ErrNoRecord) {
		if _, _, werr := st.WriteSnapshot(forgeName, it, nil, m.app.Config.Records.SnapshotComments); werr == nil {
			rec, err = st.Read(forgeName, it.Ref)
		}
	}
	return rec, err
}

// homeItem is the selected Home item, nil when the page, a dialog or the row
// rules one out.
func (m model) homeItem() *domain.WorkItem {
	if m.page != pageHome || m.mr != nil || m.isAgilePage() || m.sw != nil || m.cl != nil || m.agt != nil || m.nt != nil || m.wt != nil || m.sm != nil || m.cm != nil ||
		m.homeSel >= len(m.homeRows) {
		return nil
	}
	return m.homeRows[m.homeSel].item
}

// openNoteEdit is Home's `n`: the selected item's notes become editable in
// the detail pane.
func (m model) openNoteEdit() (model, tea.Cmd) {
	it := m.homeItem()
	if it == nil {
		return m, nil
	}
	return m.beginNoteEdit(it), nil
}

// beginNoteEdit opens the editor on the item's notes, prefilled with what the
// record holds, the cursor on a fresh line after it. A record that cannot be
// read or made says why and opens nothing.
func (m model) beginNoteEdit(it *domain.WorkItem) model {
	fn := ""
	if m.app != nil {
		fn = m.itemForge(it)
	}
	if fn == "" {
		m.notice(events.Warn, m.g.T("The item's forge is unknown · press U to sync, then n again"))
		return m
	}
	rec, err := m.itemRecord(fn, *it)
	if err != nil {
		m.notice(events.Warn, m.g.T("The item's record could not be read · check the records directory (ade doctor)"))
		return m
	}
	text := strings.TrimRight(rec.Notes, " \t\r\n")
	if text != "" {
		text += "\n"
	}
	m.ne = &noteEdit{key: markKey(it.Project, it.Ref.String()), forgeName: fn, item: *it, orig: text, ed: newEditor(text)}
	return m
}

// noteEditKey is a key while the notes are edited: every key but ctrl+c goes
// to the editor. enter is a newline; ctrl+s and esc save and leave (private
// notes: typing is never thrown away); ctrl+e saves and opens $EDITOR.
func (m model) noteEditKey(k tea.KeyPressMsg) (model, tea.Cmd, bool) {
	ne := m.ne
	switch k.String() {
	case "ctrl+c":
		if m.saveNoteEdit() {
			m.ne = nil
		}
		return m, nil, false // quits
	case "ctrl+s", "esc":
		if m.saveNoteEdit() {
			m.ne = nil
		}
	case "ctrl+e":
		if !m.saveNoteEdit() {
			return m, nil, true
		}
		st := records.New(m.app.RecordsDir)
		rec, err := st.Read(ne.forgeName, ne.item.Ref)
		if err == nil && rec.NotesLine == 0 { // a record that lost its heading gets one, so the cursor lands at the notes
			if err = st.WriteNotes(ne.forgeName, ne.item.Ref, rec.Notes); err == nil {
				rec, err = st.Read(ne.forgeName, ne.item.Ref)
			}
		}
		if err != nil {
			ne.warn = m.g.T("the record could not be opened · check the records directory (ade doctor)")
			return m, nil, true
		}
		m.ne = nil
		c := m.hostEditor().Command(rec.Path, rec.NotesLine, true)
		return m, notesExec(c, func(err error) tea.Msg { return notesEditedMsg{err: err} }), true
	case "enter":
		ne.ed.handle(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	default:
		ne.ed.handle(k)
	}
	return m, nil, true
}

// saveNoteEdit writes the editor's text as the item's notes (only the
// `## Notes` section, under the record lock) and shows it at once. An unchanged
// text writes nothing. A failed write keeps the editor open with the reason.
func (m *model) saveNoteEdit() bool {
	ne := m.ne
	text := ne.ed.String()
	if strings.TrimRight(text, " \t\r\n") == strings.TrimRight(ne.orig, " \t\r\n") {
		return true
	}
	st := records.New(m.app.RecordsDir)
	err := st.WriteNotes(ne.forgeName, ne.item.Ref, text)
	if errors.Is(err, records.ErrNoRecord) { // removed while editing: made again from the cache
		if _, err = m.itemRecord(ne.forgeName, ne.item); err == nil {
			err = st.WriteNotes(ne.forgeName, ne.item.Ref, text)
		}
	}
	if err != nil {
		ne.warn = m.g.T("the notes could not be saved · check the records directory (ade doctor); esc tries again")
		return false
	}
	m.setItemNotes(ne.key, text)
	return true
}

// setItemNotes replaces one item's notes in m.notes after this process wrote
// them, so the pane shows them without a read.
func (m *model) setItemNotes(key, text string) {
	if m.notes == nil {
		m.notes = map[string]itemNote{}
	}
	if t := strings.TrimSpace(text); t != "" {
		m.notes[key] = itemNote{text: t, dated: records.ParseNotes(t)}
	} else {
		delete(m.notes, key)
	}
	m.notesGen++
}

// notesUpdate routes the notes editor's keys, $EDITOR's end and the quick-add
// and research dialogs' keys; while one is open every key but ctrl+c belongs
// to it.
func (m model) notesUpdate(msg tea.Msg) (model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case researchStartedMsg:
		return m.researchUpdate(msg), nil, true
	case notesEditedMsg:
		m.loadNotes()
		if msg.err != nil {
			m.notice(events.Warn, m.g.T("The editor did not finish cleanly · what you saved is kept; n edits again"))
		}
		return m, nil, true
	case tea.KeyPressMsg:
		if m.ne != nil {
			return m.noteEditKey(msg)
		}
		d := m.nt
		if d == nil || msg.String() == "ctrl+c" {
			return m, nil, false
		}
		if d.rs != nil {
			next, cmd := m.researchKey(msg.String())
			return next, cmd, true
		}
		next, cmd := m.noteAddKey(msg)
		return next, cmd, true
	}
	return m, nil, false
}

// openNoteAdd is Home's `N`: the one-line form that adds a dated note.
func (m model) openNoteAdd() (model, tea.Cmd) {
	it := m.homeItem()
	if it == nil {
		return m, nil
	}
	d := &notesDialog{ref: it.Ref, title: it.Issue.Title, item: *it}
	if m.app != nil {
		d.forgeName = m.itemForge(it)
	}
	if d.forgeName == "" {
		d.missing, d.warn = true, m.g.T("the item's forge is unknown · press U to sync, then N again")
	} else if _, err := m.itemRecord(d.forgeName, d.item); err != nil {
		d.missing = true
	}
	m.nt = d
	return m, nil
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
// pane and rows show. A failed write keeps the form open with the reason.
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
		st := records.New(m.app.RecordsDir)
		if err := st.AppendNote(d.forgeName, d.ref, text, time.Now().In(ai.Location(m.app.Config)), blocked); err != nil {
			d.warn = m.g.T("the note could not be saved · check the records directory (ade doctor)")
			break
		}
		if all, err := st.ReadNotes(d.forgeName, d.ref); err == nil {
			m.setItemNotes(markKey(d.item.Project, d.ref.String()), all)
		}
		m.nt = nil
		m.notice(events.Info, "Note saved on "+d.ref.String())
	}
	return m, nil
}

// readRecords reads every listed item's notes and research job; Home's rows
// and detail pane read the maps, never the files. It does no forge call and
// runs either on the loop (loadNotes) or in a command (reloadRecords).
func readRecords(a *app.App, rs []forgesync.Result) (map[string]itemNote, map[string]records.Research) {
	notes, research := map[string]itemNote{}, map[string]records.Research{}
	if a == nil {
		return notes, research
	}
	st := records.New(a.RecordsDir)
	svc := &ai.Service{Records: st, Now: time.Now}
	for _, r := range rs {
		if r.Forge == "" {
			continue
		}
		for i := range r.Items {
			key := markKey(r.Project, r.Items[i].Ref.String())
			if text, err := st.ReadNotes(r.Forge, r.Items[i].Ref); err == nil {
				if t := strings.TrimSpace(text); t != "" {
					notes[key] = itemNote{text: t, dated: records.ParseNotes(t)}
				}
			}
			if v, err := svc.ReadResearch(r.Forge, r.Items[i].Ref); err == nil {
				research[key] = v.Research
			}
		}
	}
	return notes, research
}

// loadNotes reads every listed item's notes and research once, into m.notes
// and m.research. It runs on each refresh and after $EDITOR returns.
func (m *model) loadNotes() {
	m.notes, m.research = readRecords(m.app, m.results)
	m.notesGen++
}

// reloadRecords is loadNotes off the UI loop, for a change in the records
// directory (`ade note` in another pane, a hand edit, a research worker).
func (m model) reloadRecords() tea.Cmd {
	a, rs, gen := m.app, m.results, m.notesGen
	return func() tea.Msg {
		n, r := readRecords(a, rs)
		return recordsLoadedMsg{gen: gen, notes: n, research: r}
	}
}

// onRecordsLoaded applies a reload unless something newer replaced the maps
// meanwhile (its own write signals again, so nothing is missed).
func (m *model) onRecordsLoaded(msg recordsLoadedMsg) {
	if msg.gen != m.notesGen {
		return
	}
	m.notes, m.research = msg.notes, msg.research
	m.notesGen++
}

// itemDatedNotes is the item's dated notes, newest first.
func (m model) itemDatedNotes(it *domain.WorkItem) []records.Note {
	return m.notes[markKey(it.Project, it.Ref.String())].dated
}

// --- view --------------------------------------------------------------------

// noteEditPane is the detail pane while the notes are edited: the item's
// title, then the editor in all the room below it.
func (m model) noteEditPane(w, h int) string {
	ne, g := m.ne, m.g
	it := &ne.item
	l := []string{
		stBold.Render(ansi.Truncate(it.Ref.String()+"  "+it.Issue.Title, w, g.Ell)),
		"",
		stBold.Render("notes") + stFaint.Render("  editing"),
	}
	var warn []string
	if ne.warn != "" {
		for _, ln := range wrap(g.Warn+" "+ne.warn, w) {
			warn = append(warn, stWarn.Render(ln))
		}
	}
	rows := max(h-len(l)-len(warn), 1)
	bar := stRule.Render(g.Vertical) + " "
	for _, ln := range ne.ed.view(max(w-2, 4), rows, g.T("your notes on this item")) {
		l = append(l, bar+ln)
	}
	return strings.Join(append(l, warn...), "\n")
}

// noteEditFooter is the footer while the notes are edited.
func (m model) noteEditFooter(w int) string {
	return fitKeys(w, nil, []string{"ctrl+s", "save", "esc", "save and close", "enter", "newline", "ctrl+e", "edit in " + m.hostEditor().Name})
}

func (m model) notesOverlay(w, h int) string {
	d := m.nt
	if d == nil {
		return ""
	}
	if d.rs != nil {
		return m.researchOverlay(d, w, h)
	}
	dw := min(w, 100) // the shared dialog measure; blankFieldLayer centres it
	return m.noteAddOverlay(d, dw, dw-4)
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
