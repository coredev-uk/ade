package tui

import (
	"hash/fnv"
	"strings"

	"charm.land/lipgloss/v2"
)

// Interchange (see DESIGN.md): the terminal's own
// ground and ink; colour only as line inks, state marks, and the train.

var lineInks = []string{"#e0453a", "#3b7dd8", "#e8a33d", "#3fa66b", "#b05fd0", "#2bb3b3"}

// ink returns the stable line colour for a repo (same repo, same colour,
// across projects and sessions).
func ink(repo string) lipgloss.Style {
	h := fnv.New32a()
	h.Write([]byte(repo)) //nolint:errcheck // hash writes never fail
	return lipgloss.NewStyle().Foreground(lipgloss.Color(lineInks[int(h.Sum32())%len(lineInks)]))
}

// State and overlay colours (DESIGN.md); line inks are above.
const (
	colWarn = "#e8a33d"
	colOK   = "#3fa66b"
	colBad  = "#e0453a"
	colRule = "#666666"
)

var (
	stBold  = lipgloss.NewStyle().Bold(true)
	stFaint = lipgloss.NewStyle().Faint(true)
	stWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color(colWarn))
	stOK    = lipgloss.NewStyle().Foreground(lipgloss.Color(colOK))
	stBad   = lipgloss.NewStyle().Foreground(lipgloss.Color(colBad))
	stFound = lipgloss.NewStyle().Reverse(true) // a search match in the job log
	stRule  = lipgloss.NewStyle().Faint(true)
)

// glyphs is one glyph vocabulary; host.glyphs picks unicode or ascii.
type glyphs struct {
	Done, Train, Interchange, Ahead                string // stations
	Travelled, ToCome, Vertical, Branch, BranchEnd string // connectors
	Selected, Sep, Rule                            string
	OK, Warn, Bad, Info, Unread                    string
	Up, Down, Dirty, Sync                          string
	Thread                                         string          // unresolved review threads on a row
	Thin, Pane, Prompt, Cursor, Ell                string          // faint connector, pane separator, palette prompt, input cursor, ellipsis
	Border                                         lipgloss.Border // dialogs and pop-ups
	ASCII                                          bool
}

// T renders a literal of UI text in this glyph set: in ascii every
// non-ASCII chrome rune becomes its ASCII stand-in. User and forge text never
// goes through it.
func (g glyphs) T(s string) string {
	if !g.ASCII {
		return s
	}
	return asciiRunes.Replace(s)
}

var asciiRunes = strings.NewReplacer("…", "...", "·", "-", "→", "->", "←", "<-", "–", "-", "›", ">", "▏", "|", "✉", "m", "✓", "+", "▲", "!", "✕", "x", "✎", "~", "⟳", "~")

var unicodeGlyphs = glyphs{
	Done: "●", Train: "◉", Interchange: "◎", Ahead: "○",
	Travelled: "━", ToCome: "─", Vertical: "┃", Branch: "┣━", BranchEnd: "┗━",
	Selected: "▸", Sep: "·", Rule: "─",
	OK: "✓", Warn: "▲", Bad: "✕", Info: "●", Unread: "●",
	Up: "↑", Down: "↓", Dirty: "✎", Sync: "⟳", Thread: "✉",
	Thin: "│", Pane: " │ ", Prompt: "›", Cursor: "▏", Ell: "…", Border: lipgloss.RoundedBorder(),
}

var asciiGlyphs = glyphs{
	Done: "*", Train: "@", Interchange: "%", Ahead: "o",
	Travelled: "=", ToCome: "-", Vertical: "|", Branch: "+-", BranchEnd: "`-",
	Selected: ">", Sep: "-", Rule: "-",
	OK: "+", Warn: "!", Bad: "x", Info: "*", Unread: "*",
	Up: "^", Down: "v", Dirty: "~", Sync: "~", Thread: "m",
	Thin: "|", Pane: " | ", Prompt: ">", Cursor: "|", Ell: "...", ASCII: true,
	Border: lipgloss.Border{Top: "-", Bottom: "-", Left: "|", Right: "|", TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+"},
}

func glyphSet(name string) glyphs {
	if name == "ascii" {
		return asciiGlyphs
	}
	return unicodeGlyphs
}
