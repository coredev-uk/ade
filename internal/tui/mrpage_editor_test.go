package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func typeInto(e *editor, keys ...string) {
	for _, k := range keys {
		e.handle(keyMsg(k))
	}
}

func TestEditorEditing(t *testing.T) {
	e := newEditor("")
	typeInto(&e, "h", "e", "l", "l", "o")
	if e.String() != "hello" {
		t.Fatalf("text = %q", e.String())
	}
	e.handle(tea.KeyPressMsg{Code: tea.KeyLeft})
	e.handle(tea.KeyPressMsg{Code: tea.KeyLeft})
	typeInto(&e, "X")
	if e.String() != "helXlo" {
		t.Errorf("insert in the middle = %q", e.String())
	}
	typeInto(&e, "backspace")
	if e.String() != "hello" {
		t.Errorf("backspace = %q", e.String())
	}
	e.handle(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	typeInto(&e, "w", "o", "r", "l", "d")
	if e.String() != "hel\nworldlo" {
		t.Errorf("alt+enter should insert a newline at the cursor: %q", e.String())
	}
	g := newEditor("a")
	g.handle(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	typeInto(&g, "b")
	if g.String() != "a\nb" {
		t.Errorf("ctrl+j should insert a newline: %q", g.String())
	}
}

func TestEditorWordMovementAndDeletion(t *testing.T) {
	f := newEditor("keep this, drop that")
	f.handle(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if f.String() != "keep this, drop " {
		t.Errorf("ctrl+w = %q", f.String())
	}
	f.handle(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt}) // word back
	typeInto(&f, "X")
	if f.String() != "keep this, Xdrop " {
		t.Errorf("alt+left then X = %q", f.String())
	}
	f.handle(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if !strings.HasSuffix(f.String(), "drop ") || strings.Contains(f.String(), "keep") {
		t.Errorf("ctrl+u should drop what is before the cursor: %q", f.String())
	}
}

func TestEditorMovesBetweenLines(t *testing.T) {
	e := newEditor("one\ntwo\nthree")
	e.handle(tea.KeyPressMsg{Code: tea.KeyUp})
	e.handle(tea.KeyPressMsg{Code: tea.KeyHome})
	typeInto(&e, "X")
	if e.String() != "one\nXtwo\nthree" {
		t.Errorf("up, home, X = %q", e.String())
	}
}

func TestEditorDialogKeysAreNotSwallowed(t *testing.T) {
	e := newEditor("x")
	for _, k := range []string{"enter", "esc", "tab", "shift+tab"} {
		if e.handle(keyMsg(k)) {
			t.Errorf("the editor swallowed %s", k)
		}
	}
	if e.String() != "x" {
		t.Errorf("a dialog key changed the text: %q", e.String())
	}
}

func TestEditorUnicode(t *testing.T) {
	u := newEditor("héllo wörld ✓")
	u.handle(keyMsg("backspace"))
	u.handle(keyMsg("backspace"))
	if u.String() != "héllo wörld" {
		t.Errorf("backspace on multi-byte text = %q", u.String())
	}
}

func TestEditorPasteKeepsNewlines(t *testing.T) {
	e := newEditor("a ")
	e.handle(tea.PasteMsg{Content: "first line\nsecond line"})
	if e.String() != "a first line\nsecond line" {
		t.Errorf("paste = %q", e.String())
	}
}

// The text wraps at words inside the box, every row fits it, the cursor is a
// reverse-video cell even after the last character, and a very long word is
// cut rather than overflowing.
func TestEditorViewWrapsFitsAndShowsCursor(t *testing.T) {
	e := newEditor("one two three four five six seven eight nine ten")
	rows := e.view(20, 5, "")
	if len(rows) < 3 {
		t.Fatalf("a 48-cell text in 20 cells took %d rows: %q", len(rows), rows)
	}
	for _, r := range rows {
		if lipgloss.Width(r) > 20 {
			t.Errorf("row %q overflows 20 cells", ansi.Strip(r))
		}
	}
	if joined := strings.Join(strings.Fields(ansi.Strip(strings.Join(rows, " "))), " "); joined != "one two three four five six seven eight nine ten" {
		t.Errorf("wrapping lost or cut words: %q", joined)
	}
	if !strings.Contains(strings.Join(rows, "\n"), "\x1b[7m") {
		t.Errorf("no reverse-video cursor in %q", rows)
	}
	long := newEditor(strings.Repeat("x", 50))
	lrows := long.view(20, 3, "")
	if len(lrows) > 3 {
		t.Errorf("%d rows, want at most 3", len(lrows))
	}
	for _, r := range lrows {
		if lipgloss.Width(r) > 20 {
			t.Errorf("row %q overflows 20 cells", ansi.Strip(r))
		}
	}
}

func TestEditorViewScrollsWithCursorAndPaintsNoBackground(t *testing.T) {
	e := newEditor(strings.Join([]string{"l1", "l2", "l3", "l4", "l5", "l6", "l7"}, "\n"))
	rows := e.view(30, 3, "")
	if len(rows) != 3 || !strings.Contains(ansi.Strip(rows[2]), "l7") {
		t.Errorf("the view should end on the cursor line, got %q", rows)
	}
	for _, r := range rows {
		if strings.Contains(r, "\x1b[4"+"8;") || strings.Contains(r, "\x1b[40m") || strings.Contains(r, "\x1b[100m") {
			t.Errorf("editor painted a background: %q", r)
		}
	}
}

func TestEditorEmptyShowsFaintPlaceholder(t *testing.T) {
	e := newEditor("")
	empty := e.view(30, 3, "your reply")
	if len(empty) != 1 || !strings.Contains(ansi.Strip(empty[0]), "your reply") {
		t.Errorf("empty editor = %q", empty)
	}
	if !strings.Contains(empty[0], "\x1b[2m") {
		t.Errorf("placeholder is not faint: %q", empty[0])
	}
}

// --- the job log viewport -----------------------------------------------------------

func logModal(lines int, wide string) *mrModal {
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&b, "\x1b[31mline %s\x1b[0m #%d\n", wide, i)
	}
	md := &mrModal{kind: modalLog}
	md.setLog(mrResultMsg{out: jobLog{text: b.String()}})
	return md
}

func TestJobLogOpensOnTheTailAndScrolls(t *testing.T) {
	m := model{g: unicodeGlyphs, mr: &mrPage{}}
	md := logModal(100, "")
	_, got := m.logDialog(md, 60, 24) // 12 rows
	if len(got) != 12 || !strings.HasSuffix(strings.TrimSpace(ansi.Strip(got[len(got)-1])), "#100") {
		t.Fatalf("the log should open on its last lines, got %d rows ending %q", len(got), ansi.Strip(got[len(got)-1]))
	}
	if strings.Contains(strings.Join(got, ""), "\x1b[31m") {
		t.Errorf("CI colour leaked into the dialog: %q", got[0])
	}
	m.mr.modal = md
	m.modalKey(keyMsg("k"))
	_, got = m.logDialog(md, 60, 24)
	if !strings.HasSuffix(strings.TrimSpace(ansi.Strip(got[len(got)-1])), "#99") {
		t.Errorf("k should scroll up one line, last row %q", ansi.Strip(got[len(got)-1]))
	}
	m.modalKey(tea.KeyPressMsg{Code: 'g', Text: "g"})
	_, got = m.logDialog(md, 60, 24)
	if !strings.HasSuffix(strings.TrimSpace(ansi.Strip(got[0])), "#1") {
		t.Errorf("g should go to the top, first row %q", ansi.Strip(got[0]))
	}
	m.modalKey(tea.KeyPressMsg{Code: 'G', Text: "G"})
	_, got = m.logDialog(md, 60, 24)
	if !strings.HasSuffix(strings.TrimSpace(ansi.Strip(got[len(got)-1])), "#100") {
		t.Errorf("G should go to the bottom, last row %q", ansi.Strip(got[len(got)-1]))
	}
}

func TestJobLogShortLogMakesAShortDialogAndLongLinesScrollSideways(t *testing.T) {
	m := model{g: unicodeGlyphs, mr: &mrPage{}}
	md := logModal(2, strings.Repeat("w", 100))
	m.mr.modal = md
	_, got := m.logDialog(md, 40, 40)
	if len(got) != 2 {
		t.Fatalf("a 2-line log took %d rows", len(got))
	}
	for _, r := range got {
		if lipgloss.Width(r) > 40 {
			t.Errorf("row %q overflows the dialog", ansi.Strip(r))
		}
	}
	before := ansi.Strip(got[0])
	m.modalKey(keyMsg("l"))
	_, got = m.logDialog(md, 40, 40)
	if ansi.Strip(got[0]) == before {
		t.Errorf("l should scroll a long line sideways")
	}
}
