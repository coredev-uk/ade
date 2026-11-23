package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Breakpoints: every width and height at which the dashboard shows fewer or
// more at once. DESIGN.md (Layout) is the prose; this is the one place the
// numbers live. A region is never squeezed below a usable size: it appears
// whole or not at all, and what is hidden stays one key away.
const (
	// minW and minH are the smallest screen laid out. A terminal below them
	// still gets a minW x minH frame (the terminal clips it).
	minW, minH = 60, 16

	// Width. Below twoPaneMinW the list fills the screen and `tab` swaps it
	// for the detail (single pane); overlays take the full width. At
	// twoPaneMinW the list is 66% of 110 columns (72 cells, a title still
	// readable beside the badge and court columns) and the preview 34.
	twoPaneMinW = 110
	// wideMinW adds a third region, the rail (Activity and Services).
	// Below it Activity is the list's last sub-rule (`e` opens it) and the
	// services strip sits in the preview.
	wideMinW = 140
	railW    = 28
	drawerW  = 60 // the notification centre

	// Height. Below shortH (the classic 24 rows) the blank row under the
	// header goes, the preview's station list drops its connector lines and
	// the pop-up stack shows one box. Below tallH a second pop-up still
	// fits, three do from tallH up.
	shortH = 24
	tallH  = 32

	// minTitleW is the narrowest Home title cell: the court column gives up
	// width before the title goes below it.
	minTitleW = 16
	minCourtW = 14
)

// narrowW is true where the single pane applies.
func narrowW(w int) bool { return w < twoPaneMinW }

// shortScreen is true on a terminal with fewer rows than shortH.
func shortScreen(h int) bool { return h < shortH }

// toastLimit is how many pop-ups fit at once on a screen h rows high.
func toastLimit(h int) int {
	switch {
	case h < shortH:
		return 1
	case h < tallH:
		return 2
	}
	return maxToasts
}

// clipDialog makes a framed dialog (top border, body, bottom border) at most
// rows rows high. Blank spacer rows go first, from the bottom up; when that is
// not enough the middle of the body is cut and one row says how many rows are
// hidden, the key row at the bottom always stays.
func clipDialog(g glyphs, lines []string, rows int) []string {
	if len(lines) <= rows || rows < 6 {
		return lines
	}
	out := append([]string(nil), lines...)
	for i := len(out) - 2; i > 0 && len(out) > rows; i-- {
		if blankRow(out[i]) {
			out = append(out[:i], out[i+1:]...)
		}
	}
	if len(out) <= rows {
		return out
	}
	// top border and title, the cut body, the marker, then the last three rows
	// (a blank, the key row, the bottom border).
	keepTop, keepBot := 2, 3
	body := rows - keepTop - keepBot - 1
	hidden := len(out) - keepTop - keepBot - body
	dw := lipgloss.Width(out[0])
	marker := ansi.Cut(out[1], 0, 2) +
		pad(g, stFaint.Render(g.Ell+fmt.Sprintf(" %d more %s a taller terminal shows them", hidden, plural(hidden, "row"))), dw-4) +
		ansi.Cut(out[1], dw-2, dw)
	res := append([]string(nil), out[:keepTop+body]...)
	res = append(res, marker)
	return append(res, out[len(out)-keepBot:]...)
}

// blankRow is a framed row with nothing in it but the border.
func blankRow(l string) bool {
	return strings.Trim(ansi.Strip(l), "│┃|╭╮╰╯─┌┐└┘+- ") == ""
}

// dims is the size the screen is laid out at: the terminal's, but never below
// minW x minH, and a default before the first WindowSizeMsg.
func (m model) dims() (w, h int) {
	if m.w == 0 {
		return 120, 36
	}
	return max(m.w, minW), max(m.h, minH)
}

// helpWidth is the help dialog's width on a screen w wide: the full width
// where the single pane applies, the MR page's help at a reading measure.
func helpWidth(w int, mr bool) int {
	switch {
	case mr:
		return min(w, 100)
	case narrowW(w):
		return w
	}
	return min(76, w-4)
}

// scrollBody windows a dialog's rows (the first head rows stay fixed) to the
// space a screen h rows high leaves: the frame takes two rows, the header
// and footer two more. Cut rows are one key away: the last row says how many
// are above and below, and j/k move the window (top is the first body row
// shown).
func scrollBody(g glyphs, l []string, head, h, top int) []string {
	room := h - 4 - head
	body := l[head:]
	if len(body) <= room {
		return l
	}
	vis := max(1, room-1)
	top = min(max(top, 0), len(body)-vis)
	var cue []string
	if top > 0 {
		cue = append(cue, fmt.Sprintf("%s %d above", g.Up, top))
	}
	if below := len(body) - top - vis; below > 0 {
		cue = append(cue, fmt.Sprintf("%s %d more", g.Down, below))
	}
	more := strings.Join(append(cue, "j/k scrolls"), " "+g.Sep+" ")
	out := append(append([]string(nil), l[:head]...), body[top:top+vis]...)
	return append(out, stFaint.Render(more))
}

// helpMax is how far the open help dialog scrolls: 0 when it fits whole.
func (m model) helpMax() int {
	w, h := m.dims()
	var l []string
	head := 1
	if m.mr != nil {
		l, head = mrHelpBody(m.g, m.aiOn()), 2
	} else {
		l = helpBody(helpWidth(w, false), h, m.g, m.aiOn())
	}
	body, room := len(l)-head, h-4-head
	if body <= room {
		return 0
	}
	return body - max(1, room-1)
}

// helpScrollKey moves the help dialog's window for a scrolling key; ok is
// false for any other key.
func helpScrollKey(ks string, top, limit, h int) (int, bool) {
	page := max(1, (h-6)/2)
	switch ks {
	case "j", "down":
		top++
	case "k", "up":
		top--
	case "ctrl+d", "pgdown", "space":
		top += page
	case "ctrl+u", "pgup":
		top -= page
	case "g", "home":
		top = 0
	case "G", "end":
		top = limit
	default:
		return top, false
	}
	return min(max(top, 0), limit), true
}

// short is true when this screen has fewer rows than shortH.
func (m model) short() bool {
	_, h := m.dims()
	return shortScreen(h)
}
