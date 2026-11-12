package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/forge"
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
	m.mr.modal = md
	rows := func(w, h int) []string { return strings.Split(m.logScreen(md, w, h), "\n") }
	got := rows(60, 24) // title, 22 log rows, hint
	if len(got) != 24 || !strings.HasSuffix(strings.TrimSpace(ansi.Strip(got[22])), "#100") {
		t.Fatalf("the log should fill the screen and open on its last lines, got %d rows, last log row %q", len(got), ansi.Strip(got[22]))
	}
	wide := rows(100, 24)
	if !strings.Contains(ansi.Strip(wide[0]), "Job log") || !strings.Contains(ansi.Strip(wide[23]), "/ search") {
		t.Errorf("one-line title and hint expected, got %q and %q", ansi.Strip(wide[0]), ansi.Strip(wide[23]))
	}
	if strings.Contains(strings.Join(got, ""), "\x1b[31m") {
		t.Errorf("CI colour leaked into the log: %q", got[1])
	}
	last := func() string { g := rows(60, 24); return strings.TrimSpace(ansi.Strip(g[22])) }
	m.modalKey(keyMsg("k"))
	if !strings.HasSuffix(last(), "#99") {
		t.Errorf("k should scroll up one line, last row %q", last())
	}
	m.modalKey(keyMsg("g"))
	if g := rows(60, 24); !strings.HasSuffix(strings.TrimSpace(ansi.Strip(g[1])), "#1") {
		t.Errorf("g should go to the top, first row %q", ansi.Strip(g[1]))
	}
	m.modalKey(keyMsg("G"))
	if !strings.HasSuffix(last(), "#100") {
		t.Errorf("G should go to the bottom, last row %q", last())
	}
	m.modalKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if !strings.HasSuffix(last(), "#79") {
		t.Errorf("pgup should move a page less one line, last row %q", last())
	}
}

func TestJobLogUsesTheWholeWidthAndScrollsSideways(t *testing.T) {
	m := model{g: unicodeGlyphs, mr: &mrPage{}}
	md := logModal(2, strings.Repeat("w", 300))
	m.mr.modal = md
	got := strings.Split(m.logScreen(md, 200, 30), "\n")
	if len(got) != 30 {
		t.Fatalf("screen is %d rows, want 30", len(got))
	}
	if w := lipgloss.Width(got[1]); w != 200 || strings.Contains(ansi.Strip(got[1]), "#1") {
		t.Errorf("a long line should run to the screen edge (width %d): %q", w, ansi.Strip(got[1]))
	}
	for _, r := range got {
		if lipgloss.Width(r) > 200 {
			t.Errorf("row %q overflows the screen", ansi.Strip(r))
		}
	}
	before := ansi.Strip(got[1])
	m.modalKey(keyMsg("l"))
	if after := ansi.Strip(strings.Split(m.logScreen(md, 200, 30), "\n")[1]); after == before {
		t.Errorf("l should scroll a long line sideways")
	}
}

func TestJobLogTitleSaysWhenTheStartWasCutOff(t *testing.T) {
	m := model{g: unicodeGlyphs, mr: &mrPage{}}
	md := &mrModal{kind: modalLog}
	md.setLog(mrResultMsg{out: jobLog{text: strings.Repeat("x\n", logTail/2)}})
	m.mr.modal = md
	if title := ansi.Strip(strings.Split(m.logScreen(md, 120, 20), "\n")[0]); !strings.Contains(title, "start cut off") {
		t.Errorf("a log of logTail bytes should say its start was cut off: %q", title)
	}
	short := logModal(5, "")
	if title := ansi.Strip(strings.Split(m.logScreen(short, 120, 20), "\n")[0]); strings.Contains(title, "cut off") {
		t.Errorf("a short log was reported cut off: %q", title)
	}
}

func TestJobLogSearchJumpsBetweenMatches(t *testing.T) {
	m := model{g: unicodeGlyphs, mr: &mrPage{}}
	md := logModal(100, "")
	m.mr.modal = md
	screen := func() []string { return strings.Split(ansi.Strip(m.logScreen(md, 60, 24)), "\n") }
	m.modalKey(keyMsg("g"))
	m.modalKey(keyMsg("/"))
	for _, r := range "#5" {
		m.modalKey(keyMsg(string(r)))
	}
	if !md.logTyping || !strings.Contains(screen()[23], "#5") {
		t.Fatalf("typing a query should show it on the hint line: %q", screen()[23])
	}
	m.modalKey(keyMsg("enter"))
	if md.logTyping || len(md.logHits) != 11 { // #5, #50-#59
		t.Fatalf("enter should keep the query; typing=%v hits=%d", md.logTyping, len(md.logHits))
	}
	if !strings.Contains(screen()[0], "/#5 1 of 11") {
		t.Errorf("title should name the match: %q", screen()[0])
	}
	m.modalKey(keyMsg("n"))
	if md.logHit != 1 {
		t.Errorf("n should step to the next match, at %d", md.logHit)
	}
	m.modalKey(keyMsg("N"))
	m.modalKey(keyMsg("N"))
	if md.logHit != 10 {
		t.Errorf("N should wrap to the last match, at %d", md.logHit)
	}
	m.modalKey(keyMsg("/"))
	m.modalKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.mr.modal == nil || md.logTyping || md.logQuery != "" {
		t.Errorf("esc while typing should clear the search and keep the log open")
	}
	m.modalKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.mr.modal != nil {
		t.Error("esc should close the log")
	}
}

func TestJobLogWithoutTextSaysSo(t *testing.T) {
	m := model{g: unicodeGlyphs, mr: &mrPage{}}
	md := &mrModal{kind: modalLog}
	md.setLog(mrResultMsg{out: jobLog{text: "\n"}})
	m.mr.modal = md
	if out := ansi.Strip(m.logScreen(md, 80, 12)); !strings.Contains(out, "The job has no log.") || strings.Contains(out, "1-1 of 1") {
		t.Errorf("an empty trace should say the job has no log:\n%s", out)
	}
}

func TestJobLogIgnoresALateAnswerForAnotherJob(t *testing.T) {
	ref := mrRef("web", 1)
	m := model{g: unicodeGlyphs, mr: &mrPage{}, mrCache: map[string]*mrData{ref.String(): {}}}
	m.mr.modal = &mrModal{kind: modalLog, job: forge.Job{ID: 2, Name: "lint"}, logLoading: true}
	m.onMRResult(mrResultMsg{ref: ref, what: "log", job: 1, out: jobLog{job: forge.Job{ID: 1}, text: "from job one\n"}})
	if md := m.mr.modal; !md.logLoading || len(md.logRaw) != 0 {
		t.Fatalf("job 1's log landed in job 2's viewer: loading=%v lines=%q", md.logLoading, md.logRaw)
	}
	m.onMRResult(mrResultMsg{ref: ref, what: "log", job: 2, out: jobLog{job: forge.Job{ID: 2}, text: "from job two\n"}})
	if md := m.mr.modal; md.logLoading || !slices.Equal(md.logRaw, []string{"from job two"}) {
		t.Errorf("job 2's own log was not shown: loading=%v lines=%q", md.logLoading, md.logRaw)
	}
}

func TestJobLogKeepsTheMatchCountAndEscOnNarrowScreens(t *testing.T) {
	m := model{g: unicodeGlyphs, mr: &mrPage{}}
	md := logModal(100, "")
	md.job.Name = "integration-tests-with-a-long-name"
	m.mr.modal = md
	md.logCut = true
	md.logSearch("#5")
	for _, w := range []int{40, 60, 80} {
		rows := strings.Split(ansi.Strip(m.logScreen(md, w, 24)), "\n")
		if !strings.Contains(rows[0], "1 of 11") {
			t.Errorf("width %d: the match count is gone from the title %q", w, rows[0])
		}
		if hint := rows[len(rows)-1]; !strings.Contains(hint, "esc close") {
			t.Errorf("width %d: the hint lost `esc close`: %q", w, hint)
		}
	}
	rows := strings.Split(ansi.Strip(m.logScreen(md, 200, 24)), "\n")
	if !strings.Contains(rows[0], "start cut off") || !strings.Contains(rows[0], "1 of 11") {
		t.Errorf("a wide title should keep the range, the cut-off note and the count: %q", rows[0])
	}
}
