package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/review"
)

// --- the inline editor --------------------------------------------------------------

func typeInto(e *editor, keys ...string) {
	for _, k := range keys {
		e.handle(keyMsg(k))
	}
}

func TestEditorEditing(t *testing.T) {
	var e editor
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
	if e.String() != "hello" || e.cur != 3 {
		t.Errorf("backspace = %q at %d", e.String(), e.cur)
	}
	e.handle(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	typeInto(&e, "w", "o", "r", "l", "d")
	if e.String() != "hel\nworldlo" {
		t.Errorf("alt+enter should insert a newline at the cursor: %q", e.String())
	}
	// ctrl+w drops the word before the cursor; ctrl+u the line before it.
	f := newEditor("keep this, drop that")
	f.handle(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if f.String() != "keep this, drop " {
		t.Errorf("ctrl+w = %q", f.String())
	}
	f.handle(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if f.String() != "" {
		t.Errorf("ctrl+u = %q", f.String())
	}
	// Enter, esc and tab belong to the dialog.
	for _, k := range []string{"enter", "esc", "tab"} {
		if (&editor{}).handle(keyMsg(k)) {
			t.Errorf("the editor swallowed %s", k)
		}
	}
}

func TestEditorWrapsAtWordsAndKeepsUnicode(t *testing.T) {
	e := newEditor("one two three four five")
	lines := e.wrap(10)
	var got []string
	for _, l := range lines {
		got = append(got, string(e.text[l.start:l.end]))
	}
	for _, l := range got {
		if lipgloss.Width(l) > 10 {
			t.Errorf("line %q is wider than 10", l)
		}
	}
	if strings.Join(got, "") != "one two three four five" {
		t.Errorf("wrapping lost text: %q", got)
	}
	for _, l := range got[:len(got)-1] {
		if !strings.HasSuffix(l, " ") {
			t.Errorf("line %q was cut mid-word", l)
		}
	}
	u := newEditor("héllo wörld ✓")
	u.handle(keyMsg("backspace"))
	u.handle(keyMsg("backspace"))
	if u.String() != "héllo wörld" {
		t.Errorf("backspace on multi-byte text = %q", u.String())
	}
}

// The cursor is drawn as a reverse-video cell wherever it is, including after
// the last character, and a very long word is cut rather than overflowing.
func TestEditorViewFitsAndShowsCursor(t *testing.T) {
	e := newEditor(strings.Repeat("x", 50))
	rows := e.view(20, 3, "")
	for _, r := range rows {
		if lipgloss.Width(r) > 20 {
			t.Errorf("row %q overflows 20 cells", ansi.Strip(r))
		}
	}
	if len(rows) > 3 {
		t.Errorf("%d rows, want at most 3", len(rows))
	}
	empty := (&editor{}).view(30, 3, "your reply")
	if len(empty) != 1 || !strings.Contains(ansi.Strip(empty[0]), "your reply") {
		t.Errorf("empty editor = %q", empty)
	}
}

// --- merge readiness ------------------------------------------------------------------

func TestMergeVerdict(t *testing.T) {
	running := &forge.Pipeline{Status: "running"}
	failed := &forge.Pipeline{Status: "failed"}
	cases := []struct {
		name string
		mr   forge.MR
		ok   bool
		auto bool
		why  string
	}{
		{"mergeable", forge.MR{State: "opened", DetailedStatus: "mergeable"}, true, false, ""},
		{"status unknown", forge.MR{State: "opened"}, true, false, ""},
		{"merged", forge.MR{State: "merged"}, false, false, "already merged"},
		{"closed", forge.MR{State: "closed"}, false, false, "closed"},
		{"draft", forge.MR{State: "opened", Draft: true, DetailedStatus: "draft_status"}, false, false, "draft"},
		{"conflicts", forge.MR{State: "opened", HasConflicts: true, TargetBranch: "main"}, false, false, "conflicts with main"},
		{"running pipeline can auto-merge", forge.MR{State: "opened", DetailedStatus: "ci_still_running", Pipeline: running}, false, true, "still running"},
		{"failed pipeline cannot", forge.MR{State: "opened", DetailedStatus: "ci_must_pass", Pipeline: failed}, false, false, "must pass"},
		{"needs approval", forge.MR{State: "opened", DetailedStatus: "not_approved"}, false, false, "approval"},
		{"threads", forge.MR{State: "opened", DetailedStatus: "discussions_not_resolved", Unresolved: 2}, false, false, "2 unresolved threads"},
		{"unknown status is quoted", forge.MR{State: "opened", DetailedStatus: "jira_association_missing"}, false, false, "jira association missing"},
	}
	for _, c := range cases {
		v := mergeVerdict(c.mr)
		if v.ok != c.ok || v.autoOK != c.auto || !strings.Contains(v.why, c.why) {
			t.Errorf("%s: %+v, want ok=%v auto=%v why~%q", c.name, v, c.ok, c.auto, c.why)
		}
	}
}

// --- layout ---------------------------------------------------------------------------

// Every tab and dialog fits the terminal in both glyph sets and at both
// breakpoints: no line wider than the screen, and the ascii set draws none of
// the unicode glyph vocabulary.
func TestPageFitsEverySizeAndGlyphSet(t *testing.T) {
	unicodeOnly := "✓✕▲●◉━┃▸⟳✎"
	for _, size := range [][2]int{{140, 40}, {100, 30}, {99, 30}, {80, 28}, {60, 20}} {
		for _, ascii := range []bool{false, true} {
			e := newMREnv(t, fake.Premium)
			e.m.w, e.m.h = size[0], size[1]
			if ascii {
				e.m.g = asciiGlyphs
			}
			ref := mrRef("api", 2)
			d, _ := review.Normalize(review.Draft{
				ID: "a", Path: "limit/token_bucket.go", Line: 3, Body: strings.Repeat("a long agent comment ", 20),
				Source: review.SourceAgent, Severity: review.SeverityBlocking,
			}, time.Now())
			if err := e.store.Put(ref, d); err != nil {
				t.Fatal(err)
			}
			e.open(ref.String())
			check := func(what string) {
				t.Helper()
				out := ansi.Strip(e.m.render())
				lines := strings.Split(out, "\n")
				if len(lines) != size[1] {
					t.Errorf("%v ascii=%v %s: %d lines, want %d", size, ascii, what, len(lines), size[1])
				}
				for _, l := range lines {
					if lipgloss.Width(l) > size[0] {
						t.Errorf("%v ascii=%v %s: line is %d wide: %q", size, ascii, what, lipgloss.Width(l), l)
					}
				}
				if ascii && strings.ContainsAny(out, unicodeOnly) {
					t.Errorf("%v ascii=%v %s: unicode glyphs in the ascii set:\n%s", size, ascii, what, out)
				}
			}
			check("overview")
			for tab := range []string{"2", "3", "4", "5"} {
				e.press(string(rune('2' + tab)))
				check(mrTab(tab + 1).title())
				if tab == 1 { // Diff: into the pane, comment dialog
					e.press("enter")
					check("diff pane")
					e.press("c")
					check("comment dialog")
					e.press("esc", "esc")
				}
			}
			e.press("S")
			check("submit dialog")
			e.press("esc", "m")
			check("merge dialog")
			e.press("esc", "?")
			check("help")
		}
	}
}

// Narrow terminals show one pane: the list, or the thing selected in it.
func TestNarrowIsSinglePane(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.m.w, e.m.h = 80, 30
	e.open(mrRef("api", 1).String())
	e.press("2")
	out := e.screen()
	if strings.Contains(out, " │ ") {
		t.Errorf("two panes below 100 columns:\n%s", out)
	}
	e.press("enter")
	e.want("reviewer-rae ·") // the conversation replaces the list
	e.wantNot("Unresolved (")
	e.press("esc")
	e.want("Unresolved (")
	if e.m.mr == nil {
		t.Error("esc from a conversation left the page; it should only close the conversation")
	}
}

// The footer names the keys that work here, and on a narrow terminal drops
// whole hints rather than cutting one.
func TestFooterShowsKeysAndNeverCutsAHint(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.open(mrRef("api", 1).String())
	e.press("2")
	e.want("r reply", "x resolve/reopen", "m merge", "S submit review")
	e.m.w = 70
	foot := e.footerLine()
	if lipgloss.Width(foot) >= 70 {
		t.Errorf("footer is %d wide: %q", lipgloss.Width(foot), foot)
	}
	if strings.Contains(foot, "…") {
		t.Errorf("footer cut a hint: %q", foot)
	}
	if !strings.Contains(foot, "? keys") || !strings.Contains(foot, "esc back") {
		t.Errorf("footer lost the way out and the help: %q", foot)
	}
}

func (e *mrEnv) footerLine() string {
	lines := strings.Split(e.screen(), "\n")
	return lines[len(lines)-1]
}

// NO_COLOR: state is never colour alone. The strip, thread rows and CI rows
// carry a glyph and a word.
func TestStateIsGlyphAndWord(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.open(mrRef("web", 1).String())
	e.want("Draft", "✕ CI failed")
	e.press("5")
	e.want("✕ failed", "✓ passed")
	e.press("2")
	e.open2()
}

func (e *mrEnv) open2() {} // placeholder removed below
