package tui

import (
	"hash/fnv"

	"charm.land/lipgloss/v2"
)

// Interchange (see .impeccable/surfaces/internal-tui.md): the terminal's own
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
	stRule  = lipgloss.NewStyle().Faint(true)
)

// glyphs is one glyph vocabulary; host.glyphs picks unicode or ascii.
type glyphs struct {
	Done, Train, Interchange, Ahead                string // stations
	Travelled, ToCome, Vertical, Branch, BranchEnd string // connectors
	Selected, Sep, Rule                            string
	OK, Warn, Bad, Info, Unread                    string
	Up, Down, Dirty, Sync                          string
}

var unicodeGlyphs = glyphs{
	Done: "●", Train: "◉", Interchange: "◎", Ahead: "○",
	Travelled: "━", ToCome: "─", Vertical: "┃", Branch: "┣━", BranchEnd: "┗━",
	Selected: "▸", Sep: "·", Rule: "─",
	OK: "✓", Warn: "▲", Bad: "✕", Info: "●", Unread: "●",
	Up: "↑", Down: "↓", Dirty: "✎", Sync: "⟳",
}

var asciiGlyphs = glyphs{
	Done: "*", Train: "@", Interchange: "%", Ahead: "o",
	Travelled: "=", ToCome: "-", Vertical: "|", Branch: "+-", BranchEnd: "`-",
	Selected: ">", Sep: "-", Rule: "-",
	OK: "+", Warn: "!", Bad: "x", Info: "*", Unread: "*",
	Up: "^", Down: "v", Dirty: "~", Sync: "~",
}

func glyphSet(name string) glyphs {
	if name == "ascii" {
		return asciiGlyphs
	}
	return unicodeGlyphs
}
