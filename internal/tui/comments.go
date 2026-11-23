package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/records"
)

// Comments on the tracker issue. Other people add context to a ticket after it
// is planned; the sync keeps the newest human comments in the item's record
// (records.CommentsFile), and Home shows them: the newest two in the detail
// pane, every one in the comments view (`C`). Only the tracker issue's
// comments: child issues and MRs have their own pages. System notes never
// count.

// detailComments is how many comments the detail pane shows.
const detailComments = 2

// loadComments reads every listed item's stored comments once, into
// m.comments, like loadNotes: the detail pane reads the map, never the files.
func (m *model) loadComments() {
	m.comments = map[string][]records.Comment{}
	if m.app == nil {
		return
	}
	st := records.New(m.app.RecordsDir)
	for _, r := range m.results {
		if r.Forge == "" {
			continue
		}
		for i := range r.Items {
			if cs, err := st.ReadComments(r.Forge, r.Items[i].Ref); err == nil && len(cs) > 0 {
				m.comments[markKey(r.Project, r.Items[i].Ref.String())] = cs
			}
		}
	}
}

// itemComments is the item's stored comments, oldest first.
func (m model) itemComments(it *domain.WorkItem) []records.Comment {
	return m.comments[markKey(it.Project, it.Ref.String())]
}

// commentLines is the detail pane's comments block: a faint count, then the
// newest comments one line each, newest first (bold author, faint age, the
// body's first line). Nothing when the issue has no human comments. hint adds
// the key that shows them all (Home only).
func (m model) commentLines(it *domain.WorkItem, w int, hint bool) []string {
	cs := m.itemComments(it)
	if len(cs) == 0 {
		return nil
	}
	g := m.g
	label := fmt.Sprintf("comments %s %d", g.Sep, len(cs))
	if hint {
		label += " " + g.Sep + " C shows all"
	}
	l := []string{"", stFaint.Render(ansi.Truncate(label, w, g.Ell))}
	for i := len(cs) - 1; i >= max(len(cs)-detailComments, 0); i-- {
		l = append(l, commentLine(g, cs[i], w))
	}
	return l
}

// commentLine is one comment on one line: the author in bold, the age in
// faint, then as much of the body's first line as fits, cut with an ellipsis.
func commentLine(g glyphs, c records.Comment, w int) string {
	head := stBold.Render(ansi.Truncate(c.Author, max(w/3, 8), g.Ell)) + stFaint.Render(" "+g.Sep+" "+agoShort(c.At))
	text := c.FirstLine()
	room := w - lipgloss.Width(head) - 2
	if text == "" || room < 4 {
		return head
	}
	return head + "  " + ansi.Truncate(text, room, g.Ell)
}

// --- the comments view (`C`) ----------------------------------------------------

type commentsDialog struct {
	item      domain.WorkItem
	forgeName string
	comments  []records.Comment // oldest first
	// fresh is true once the forge has answered; until then the view shows
	// what the last sync stored.
	fresh  bool
	busy   bool
	warn   string
	scroll int
}

// commentsMsg is the forge's answer: every human comment on the issue.
type commentsMsg struct {
	d        *commentsDialog
	comments []records.Comment
	err      error
}

// openComments is Home's `C`: the stored comments at once, and a read of the
// issue's discussions from the forge in a command.
func (m model) openComments() (model, tea.Cmd) {
	if m.page != pageHome || m.mr != nil || m.isAgilePage() || m.sw != nil || m.cl != nil || m.agt != nil || m.nt != nil || m.wt != nil || m.sm != nil || m.ait != nil || m.cm != nil ||
		m.homeSel >= len(m.homeRows) || m.homeRows[m.homeSel].item == nil {
		return m, nil
	}
	it := m.homeRows[m.homeSel].item
	d := &commentsDialog{item: *it, comments: m.itemComments(it)}
	for _, r := range m.results {
		if r.Project == it.Project {
			d.forgeName = r.Forge
			break
		}
	}
	m.cm = d
	if m.app == nil || d.forgeName == "" {
		return m, nil
	}
	d.busy = true
	return m, m.commentsCmd(d)
}

// commentsCmd reads the issue's discussions off the UI loop.
func (m model) commentsCmd(d *commentsDialog) tea.Cmd {
	ctx, mm, ref, key := m.ctx, m, d.item.Ref, d.forgeName
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		be, err := mm.backend(ctx, ref, key)
		if err != nil {
			return commentsMsg{d: d, err: err}
		}
		ds, err := be.Discussions(ctx, ref)
		if err != nil {
			return commentsMsg{d: d, err: err}
		}
		var notes []forge.Note
		for _, x := range ds {
			notes = append(notes, x.Notes...)
		}
		return commentsMsg{d: d, comments: records.CommentsFrom(notes, len(notes))}
	}
}

// commentsUpdate routes the view's answer and, while it is open, every key but
// ctrl+c.
func (m model) commentsUpdate(msg tea.Msg) (model, tea.Cmd, bool) {
	d := m.cm
	switch msg := msg.(type) {
	case commentsMsg:
		if d == nil || msg.d != d {
			return m, nil, true
		}
		d.busy = false
		if msg.err != nil {
			d.warn = shortCause(msg.err) + " · showing what the last sync kept"
			return m, nil, true
		}
		d.comments, d.fresh, d.warn = msg.comments, true, ""
		return m, nil, true
	case tea.KeyPressMsg:
		if d == nil || msg.String() == "ctrl+c" {
			return m, nil, false
		}
		next, cmd := m.commentsKey(msg.String())
		return next, cmd, true
	}
	return m, nil, false
}

func (m model) commentsKey(ks string) (model, tea.Cmd) {
	d := m.cm
	switch ks {
	case "esc", "q", "C":
		m.cm = nil
	case "o":
		return m.openBrowser()
	case "j", "down":
		d.scroll++
	case "k", "up":
		d.scroll = max(d.scroll-1, 0)
	case "pgdown", "ctrl+d", "space":
		d.scroll += 10
	case "pgup", "ctrl+u":
		d.scroll = max(d.scroll-10, 0)
	case "g", "home":
		d.scroll = 0
	case "G", "end":
		d.scroll = 1 << 30 // the view clamps it
	}
	return m, nil
}

func (m model) commentsOverlay(w, h int) string {
	d := m.cm
	if d == nil {
		return ""
	}
	dw := min(w, 100)
	inner := dw - 4
	g := m.g
	l := []string{stFaint.Render(ansi.Truncate(d.item.Issue.Title, inner, g.Ell)), ""}
	guide := countWord(len(d.comments), "comment")
	switch {
	case d.busy:
		guide += " " + g.Sep + " " + g.Sync + " reading the forge" + g.Ell
	case d.fresh:
		guide += " " + g.Sep + " read just now"
	default:
		guide += " " + g.Sep + " as of the last sync"
	}
	l = append(l, section("Tracker issue", g.T(guide), inner, g))
	l = append(l, m.commentsBody(d, inner, max(4, h-14))...)
	if d.warn != "" {
		l = append(l, "", stWarn.Render(ansi.Wrap(g.T(g.Warn+" "+d.warn), inner, "")))
	}
	l = append(l, "", keys("j/k", "scroll", "o", "browser", "esc", "back"))
	return m.dialog(g.T("Comments · ")+d.item.Ref.String(), l, dw)
}

// commentsBody is every comment, oldest first: a bold author and a faint age,
// then the body as markdown, indented; at most rows lines from the scroll
// offset, the cut edges marked.
func (m model) commentsBody(d *commentsDialog, inner, rows int) []string {
	g := m.g
	if len(d.comments) == 0 {
		if d.busy {
			return []string{stFaint.Render(g.Sync + " " + g.T("reading the comments") + g.Ell)}
		}
		return []string{stFaint.Render(g.Info + g.T(" no comments on the tracker issue yet"))}
	}
	var lines []string
	for i, c := range d.comments {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, stBold.Render(ansi.Truncate(c.Author, max(inner-12, 8), g.Ell))+stFaint.Render(" "+g.Sep+" "+agoShort(c.At)))
		for _, ln := range m.mdLines(c.Body, inner-2) {
			lines = append(lines, "  "+ln)
		}
	}
	d.scroll = clamp(d.scroll, 0, max(len(lines)-rows, 0))
	end := min(len(lines), d.scroll+rows)
	out := make([]string, 0, rows+2)
	if d.scroll > 0 {
		out = append(out, stFaint.Render(fmt.Sprintf("%s %d %s above", g.Up, d.scroll, plural(d.scroll, "line"))))
	}
	out = append(out, lines[d.scroll:end]...)
	if end < len(lines) {
		out = append(out, stFaint.Render(fmt.Sprintf(g.T("%s %d more %s · j scrolls"), g.Down, len(lines)-end, plural(len(lines)-end, "line"))))
	}
	return out
}

func (m model) commentsOverlayLayer(w, h int) *lipgloss.Layer {
	return blankFieldLayer(m.g, m.commentsOverlay(w, h), w, h)
}
