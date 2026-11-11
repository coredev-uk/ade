package overview

import (
	"hash/fnv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A small local copy of internal/tui's theme (DESIGN.md: line inks, state
// marks, the unicode and ascii glyph sets). internal/tui keeps its own
// unexported, and importing it here would pull the whole dashboard into a
// read-only pane.

var lineInks = []string{"#e0453a", "#3b7dd8", "#e8a33d", "#3fa66b", "#b05fd0", "#2bb3b3"}

// ink returns the stable line colour for a repo, the same one the dashboard
// draws it in.
func ink(repo string) lipgloss.Style {
	h := fnv.New32a()
	h.Write([]byte(repo)) //nolint:errcheck // hash writes never fail
	return lipgloss.NewStyle().Foreground(lipgloss.Color(lineInks[int(h.Sum32())%len(lineInks)]))
}

const (
	colWarn = "#e8a33d"
	colOK   = "#3fa66b"
	colBad  = "#e0453a"
)

var (
	stBold  = lipgloss.NewStyle().Bold(true)
	stFaint = lipgloss.NewStyle().Faint(true)
	stWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color(colWarn))
	stOK    = lipgloss.NewStyle().Foreground(lipgloss.Color(colOK))
	stBad   = lipgloss.NewStyle().Foreground(lipgloss.Color(colBad))
)

// glyphs is the part of the dashboard's glyph vocabulary this pane draws.
type glyphs struct {
	Done, Train, Interchange, Ahead string
	Travelled, ToCome, Vertical     string
	Sep, Rule                       string
	OK, Warn, Bad, Sync             string
	Thread                          string
}

var unicodeGlyphs = glyphs{
	Done: "●", Train: "◉", Interchange: "◎", Ahead: "○",
	Travelled: "━", ToCome: "─", Vertical: "┃",
	Sep: "·", Rule: "─",
	OK: "✓", Warn: "▲", Bad: "✕", Sync: "⟳", Thread: "✉",
}

var asciiGlyphs = glyphs{
	Done: "*", Train: "@", Interchange: "%", Ahead: "o",
	Travelled: "=", ToCome: "-", Vertical: "|",
	Sep: "-", Rule: "-",
	OK: "+", Warn: "!", Bad: "x", Sync: "~", Thread: "m",
}

// glyphSet picks the vocabulary named by host.glyphs ("ascii", else unicode).
func glyphSet(name string) glyphs {
	if name == "ascii" {
		return asciiGlyphs
	}
	return unicodeGlyphs
}

// section is a rule line: a bold title, a faint rule, faint guide words at the
// right. With a faint title (the notes) the whole line recedes.
func section(title, guide string, w int, g glyphs, faintTitle bool) string {
	ts := stBold
	if faintTitle {
		ts = stFaint
	}
	t := " " + ts.Render(title) + " "
	gd := ""
	if guide != "" {
		gd = " " + stFaint.Render(guide)
	}
	fill := max(1, w-lipgloss.Width(t)-lipgloss.Width(gd)-1)
	return t + stFaint.Render(strings.Repeat(g.Rule, fill)) + gd
}

// wrap breaks prose at word boundaries into lines of at most w cells; a word
// longer than w (a URL) is broken rather than cut. Blank lines are kept.
func wrap(s string, w int) []string {
	w = max(w, 8)
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(ansi.Wrap(para, w, ""), "\n")...)
	}
	return out
}
