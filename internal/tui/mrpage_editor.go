package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// editor is the small multi-line text field behind every inline comment,
// reply and summary. Review comments are short by house style (two sentences),
// so there is no scrolling model beyond keeping the cursor line visible; a
// long write-up belongs in $EDITOR (PLAN 6.2).
type editor struct {
	text []rune
	cur  int // rune index in text, 0..len(text)
}

func newEditor(s string) editor {
	r := []rune(s)
	return editor{text: r, cur: len(r)}
}

func (e *editor) String() string { return string(e.text) }

func (e *editor) insert(s string) {
	r := []rune(s)
	e.text = append(e.text[:e.cur], append(r, e.text[e.cur:]...)...)
	e.cur += len(r)
}

// lineBounds is the rune range of the logical line holding the cursor.
func (e *editor) lineBounds() (start, end int) {
	start = e.cur
	for start > 0 && e.text[start-1] != '\n' {
		start--
	}
	end = e.cur
	for end < len(e.text) && e.text[end] != '\n' {
		end++
	}
	return start, end
}

// handle applies one key and reports whether the editor consumed it.
// Enter, esc and tab belong to the dialog around the editor.
func (e *editor) handle(k tea.KeyPressMsg) bool {
	if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		e.insert(k.Text)
		return true
	}
	switch k.String() {
	case "alt+enter", "ctrl+j":
		e.insert("\n")
	case "backspace", "ctrl+h":
		if e.cur > 0 {
			e.text = append(e.text[:e.cur-1], e.text[e.cur:]...)
			e.cur--
		}
	case "delete", "ctrl+d":
		if e.cur < len(e.text) {
			e.text = append(e.text[:e.cur], e.text[e.cur+1:]...)
		}
	case "left", "ctrl+b":
		e.cur = max(0, e.cur-1)
	case "right", "ctrl+f":
		e.cur = min(len(e.text), e.cur+1)
	case "home", "ctrl+a":
		e.cur, _ = e.lineBounds()
	case "end", "ctrl+e":
		_, e.cur = e.lineBounds()
	case "ctrl+u": // to the start of the line
		s, _ := e.lineBounds()
		e.text = append(e.text[:s], e.text[e.cur:]...)
		e.cur = s
	case "ctrl+w", "alt+backspace": // the word before the cursor
		i := e.cur
		for i > 0 && unicode.IsSpace(e.text[i-1]) {
			i--
		}
		for i > 0 && !unicode.IsSpace(e.text[i-1]) {
			i--
		}
		e.text = append(e.text[:i], e.text[e.cur:]...)
		e.cur = i
	case "up":
		e.vertical(-1)
	case "down":
		e.vertical(1)
	default:
		return false
	}
	return true
}

// vertical moves between logical lines, keeping the column where it can.
func (e *editor) vertical(d int) {
	s, _ := e.lineBounds()
	col := e.cur - s
	if d < 0 {
		if s == 0 {
			e.cur = 0
			return
		}
		prevEnd := s - 1
		ps := prevEnd
		for ps > 0 && e.text[ps-1] != '\n' {
			ps--
		}
		e.cur = min(ps+col, prevEnd)
		return
	}
	_, end := e.lineBounds()
	if end >= len(e.text) {
		e.cur = len(e.text)
		return
	}
	ns := end + 1
	ne := ns
	for ne < len(e.text) && e.text[ne] != '\n' {
		ne++
	}
	e.cur = min(ns+col, ne)
}

// vline is one visual line: a rune range of the text.
type vline struct{ start, end int }

// wrap lays the text out in lines of at most w cells, soft-wrapping at spaces
// (a word longer than the line is cut) and breaking at newlines.
func (e *editor) wrap(w int) []vline {
	w = max(w, 4)
	var out []vline
	start, width, lastSpace := 0, 0, -1
	for i, r := range e.text {
		if r == '\n' {
			out = append(out, vline{start, i})
			start, width, lastSpace = i+1, 0, -1
			continue
		}
		rw := lipgloss.Width(string(r))
		if width+rw > w {
			brk := i
			if lastSpace > start {
				brk = lastSpace + 1
			}
			out = append(out, vline{start, brk})
			start = brk
			width = lipgloss.Width(string(e.text[start:i]))
			lastSpace = -1
			for j := start; j < i; j++ {
				if e.text[j] == ' ' {
					lastSpace = j
				}
			}
		}
		if r == ' ' {
			lastSpace = i
		}
		width += rw
	}
	return append(out, vline{start, len(e.text)})
}

// view draws the editor in a box w cells wide and at most rows high, with the
// cursor as a reverse-video cell (visible under NO_COLOR too). A faint
// placeholder shows while it is empty.
func (e *editor) view(w, rows int, placeholder string) []string {
	if len(e.text) == 0 {
		return []string{stBold.Reverse(true).Render(" ") + stFaint.Render(ansi.Truncate(placeholder, w-1, "…"))}
	}
	lines := e.wrap(w - 1) // one spare cell for a cursor at the end of a full line
	cl := len(lines) - 1
	for i, l := range lines {
		if e.cur >= l.start && (e.cur < l.end || (e.cur == l.end && (i == len(lines)-1 || lines[i+1].start != l.end))) {
			cl = i
			break
		}
	}
	top := max(0, min(cl-rows+1, len(lines)-rows))
	var out []string
	for i := top; i < min(len(lines), top+rows); i++ {
		l := lines[i]
		txt := string(e.text[l.start:l.end])
		if i == cl {
			rel := e.cur - l.start
			r := []rune(txt)
			rel = min(rel, len(r))
			cell := " "
			if rel < len(r) {
				cell = string(r[rel])
			}
			txt = string(r[:rel]) + stBold.Reverse(true).Render(cell)
			if rel < len(r) {
				txt += string(r[rel+1:])
			}
		}
		out = append(out, strings.TrimRight(txt, "\n"))
	}
	return out
}
