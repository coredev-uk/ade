package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// editor is the multi-line text field behind every inline comment, reply and
// summary: a bubbles textarea (soft wrap, word movement, selection, paste)
// dressed in the dashboard's ink, with no line numbers, no prompt and no
// painted background. Enter, esc and tab belong to the dialog around it;
// alt+enter or ctrl+j insert a newline. Review comments are short by house
// style (two sentences); a long write-up belongs in $EDITOR (PLAN 6.2).
type editor struct {
	ta textarea.Model
}

// maxEditorLines caps what a paste can bring in; the view shows only the
// last rows that fit and scrolls with the cursor.
const maxEditorLines = 10000

func newEditor(s string) editor {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.EndOfBufferCharacter = ' '
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxContentHeight = maxEditorLines
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	ta.SetStyles(editorStyles())
	ta.SetValue(s)
	_ = ta.Focus() // the cursor is static: no blink command to run
	return editor{ta: ta}
}

// editorStyles is the textarea in the terminal's own ground: no background
// anywhere, a faint placeholder, the cursor a reverse-video cell (it shows
// under NO_COLOR too) that does not blink.
func editorStyles() textarea.Styles {
	plain := textarea.StyleState{
		Base: lipgloss.NewStyle(), Text: lipgloss.NewStyle(), CursorLine: lipgloss.NewStyle(),
		LineNumber: lipgloss.NewStyle(), CursorLineNumber: lipgloss.NewStyle(),
		EndOfBuffer: lipgloss.NewStyle(), Prompt: lipgloss.NewStyle(),
		Placeholder: stFaint, Selection: lipgloss.NewStyle().Reverse(true),
	}
	return textarea.Styles{
		Focused: plain,
		Blurred: plain,
		Cursor:  textarea.CursorStyle{Color: lipgloss.NoColor{}, Shape: tea.CursorBlock, Blink: false},
	}
}

func (e *editor) String() string { return e.ta.Value() }

// handle applies a key press or a paste to the text. Enter, esc and tab are
// left to the dialog: it reports false for them.
func (e *editor) handle(msg tea.Msg) bool {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "enter", "esc", "tab", "shift+tab":
			return false
		}
	}
	e.ta, _ = e.ta.Update(msg)
	return true
}

// view draws the editor in a box w cells wide and at most rows high, one row
// while it is empty or short. The cursor is a reverse-video cell; a faint
// placeholder shows while it is empty.
func (e *editor) view(w, rows int, placeholder string) []string {
	e.ta.Placeholder = placeholder
	e.ta.MaxHeight = max(rows, 1)
	e.ta.SetWidth(max(w, 4))
	e.ta, _ = e.ta.Update(nil) // re-lays out at this width and scrolls to the cursor
	var out []string
	for _, ln := range strings.Split(e.ta.View(), "\n") {
		// The textarea pads every row to its width; the cursor cell is a
		// styled reverse-video space and so survives the trim.
		out = append(out, strings.TrimRight(ln, " "))
	}
	return out
}

// editing is the editor that pasted text goes to: the open comment, reply,
// edit, deny or submit dialog's, or the Start work dialog's MR body.
func (m *model) editing() *editor {
	if m.sw != nil && m.sw.body != nil {
		return m.sw.body
	}
	if m.mr == nil || m.mr.modal == nil {
		return nil
	}
	switch m.mr.modal.kind {
	case modalReply, modalComment, modalEditDraft, modalDeny, modalSubmit:
		return &m.mr.modal.ed
	}
	return nil
}

// paste puts bracketed-paste text into the open editor; the Deny reason is a
// one-liner, so its newlines become spaces.
func (m *model) paste(msg tea.PasteMsg) {
	if m.sw != nil && m.sw.branchIn != nil {
		in, _ := m.sw.branchIn.Update(tea.PasteMsg{Content: strings.Join(strings.Fields(msg.Content), "")})
		m.sw.branchIn = &in
		return
	}
	e := m.editing()
	if e == nil {
		return
	}
	if m.sw == nil && m.mr.modal.kind == modalDeny {
		msg.Content = strings.Join(strings.Fields(msg.Content), " ")
	}
	e.handle(msg)
}
