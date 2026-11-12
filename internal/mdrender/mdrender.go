// Package mdrender renders markdown (issue descriptions, acceptance lists,
// notes, AI summaries, merge request descriptions) for ADE's terminal
// surfaces through glamour, in a style built from the Interchange theme
// (DESIGN.md): the terminal's own ground and ink, so no background is ever
// painted and the only colour is inside code blocks; emphasis is weight, links
// are underlined with a faint address, quotes and rules are faint. The style
// uses only attributes, so NO_COLOR loses nothing but the code colours, and
// with Ascii set every glyph it draws is plain ASCII.
package mdrender

import (
	"regexp"
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/syntax"
)

// Options says how to render.
type Options struct {
	Width   int  // cells per line, wrapped to it (at least 10)
	Ascii   bool // plain ASCII glyphs (host.glyphs = "ascii")
	NoColor bool // no colour at all (NO_COLOR): code blocks keep weight only
}

// FromEnv returns options for a width with NoColor taken from the environment.
func FromEnv(width int, ascii bool) Options {
	return Options{Width: width, Ascii: ascii, NoColor: syntax.NoColor()}
}

// Style is the glamour style for the options.
func Style(o Options) ansi.StyleConfig {
	bullet, enum := "• ", ". "
	quote, rule := "┃ ", "─"
	table := [3]string{"│", "─", "┼"}
	if o.Ascii {
		bullet = "- "
		quote, rule = "| ", "-"
		table = [3]string{"|", "-", "+"}
	}
	bold := ansi.StylePrimitive{Bold: new(true)}
	heading := ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: new(true), BlockSuffix: "\n"}}
	h1 := heading
	h1.Underline = new(true)
	return ansi.StyleConfig{
		Document:      ansi.StyleBlock{Margin: new(uint(0))},
		BlockQuote:    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Faint: new(true)}, Indent: new(uint(1)), IndentToken: new(quote)},
		List:          ansi.StyleList{LevelIndent: 2},
		Heading:       heading,
		H1:            h1,
		H2:            ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: new(true)}},
		H3:            ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: new(true)}},
		H4:            ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: new(true)}},
		H5:            ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: new(true)}},
		H6:            ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: new(true)}},
		Strikethrough: ansi.StylePrimitive{CrossedOut: new(true)},
		Emph:          ansi.StylePrimitive{Italic: new(true)},
		Strong:        bold,
		HorizontalRule: ansi.StylePrimitive{
			Faint:  new(true),
			Format: "\n" + strings.Repeat(rule, max(o.Width, 10)) + "\n",
		},
		Item:        ansi.StylePrimitive{BlockPrefix: bullet},
		Enumeration: ansi.StylePrimitive{BlockPrefix: enum},
		Task:        ansi.StyleTask{Ticked: "[x] ", Unticked: "[ ] "},
		Link:        ansi.StylePrimitive{Faint: new(true)},
		LinkText:    ansi.StylePrimitive{Underline: new(true)},
		Image:       ansi.StylePrimitive{Faint: new(true)},
		ImageText:   ansi.StylePrimitive{Format: "[image: {{.text}}]"},
		Code:        ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "`", Suffix: "`"}},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{Margin: new(uint(2))},
			Theme:      syntax.StyleName(o.NoColor),
		},
		Table: ansi.StyleTable{
			CenterSeparator: new(table[2]),
			ColumnSeparator: new(table[0]),
			RowSeparator:    new(table[1]),
		},
		DefinitionTerm: ansi.StylePrimitive{Bold: new(true)},
	}
}

// renderers are built once per option set: a goldmark parser and a style are
// not free, and the dashboard renders on every frame.
type rkey struct {
	width          int
	ascii, noColor bool
}

var (
	mu      sync.Mutex
	renders = map[rkey]*glamour.TermRenderer{}
	memo    = map[memoKey][]string{}
)

type memoKey struct {
	src string
	k   rkey
}

const memoMax = 256

// Render renders markdown into lines of at most o.Width cells, without
// leading or trailing blank lines and without the trailing padding glamour
// adds. Text glamour cannot render comes back wrapped, unstyled.
func Render(src string, o Options) []string {
	o.Width = max(o.Width, 10)
	src = strings.TrimSpace(strings.ReplaceAll(src, "\r\n", "\n"))
	if src == "" {
		return nil
	}
	k := rkey{o.Width, o.Ascii, o.NoColor}
	mu.Lock()
	defer mu.Unlock()
	if out, ok := memo[memoKey{src, k}]; ok {
		return append([]string(nil), out...)
	}
	out := render(src, k, o)
	if len(memo) >= memoMax {
		clear(memo)
	}
	memo[memoKey{src, k}] = out
	return append([]string(nil), out...)
}

func render(src string, k rkey, o Options) []string {
	r, ok := renders[k]
	if !ok {
		var err error
		r, err = glamour.NewTermRenderer(
			glamour.WithStyles(Style(o)),
			glamour.WithWordWrap(o.Width),
			glamour.WithChromaFormatter(syntax.Formatter),
		)
		if err != nil {
			return plain(src, o.Width)
		}
		renders[k] = r
	}
	s, err := r.Render(src)
	if err != nil {
		return plain(src, o.Width)
	}
	return tidy(s, o.Width)
}

// plain is the fallback: the text, wrapped.
func plain(src string, w int) []string {
	return tidy(xansi.Wrap(src, w, ""), w)
}

// osc8 matches a terminal hyperlink sequence. glamour wraps every link in
// one; ADE keeps the visible address (terminals link it themselves) and drops
// the sequence, which a web tty or a multiplexer may print as garbage.
var osc8 = regexp.MustCompile("\x1b\\]8;[^\x07\x1b]*(?:\x07|\x1b\\\\)")

// itemLead matches the start of a list item line in glamour's output: the
// indent, then a bullet, a number or a task box.
var itemLead = regexp.MustCompile(`^( *)(• |- |\d+\. |\[[ x]\] )`)

// tidy trims glamour's padding and margins: trailing spaces on every line,
// blank lines at both ends and runs of blank lines; wrapped list items hang
// under their text; a line wider than w (a code line, a table) is cut into
// rows rather than overflowing.
func tidy(s string, w int) []string {
	s = osc8.ReplaceAllString(strings.ReplaceAll(s, "\t", "    "), "")
	var lines []string
	blank := true
	for _, ln := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		ln = trimRight(ln)
		if xansi.Strip(ln) == "" {
			if !blank {
				lines = append(lines, "")
			}
			blank = true
			continue
		}
		blank = false
		lines = append(lines, ln)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var out []string
	for _, ln := range hang(lines, w) {
		if xansi.StringWidth(ln) > w {
			out = append(out, strings.Split(xansi.Hardwrap(ln, w, true), "\n")...)
			continue
		}
		out = append(out, ln)
	}
	return out
}

// hang re-wraps list items so the lines after the first sit under the item's
// text instead of under its bullet, which glamour does not do. An item is a
// line with a bullet, number or task box and the lines after it that start at
// the item's own indent; blank lines and anything indented differently (code
// blocks) end it.
func hang(lines []string, w int) []string {
	var out []string
	for i := 0; i < len(lines); i++ {
		m := itemLead.FindStringSubmatch(lines[i])
		if m == nil {
			out = append(out, lines[i])
			continue
		}
		indent, lead := m[1], m[2]
		parts := []string{strings.TrimPrefix(lines[i], indent+lead)}
		j := i + 1
		for ; j < len(lines); j++ {
			l := lines[j]
			if l == "" || itemLead.MatchString(l) || !strings.HasPrefix(l, indent) || strings.HasPrefix(l, indent+" ") {
				break
			}
			parts = append(parts, strings.TrimPrefix(l, indent))
		}
		i = j - 1
		if len(parts) == 1 {
			out = append(out, lines[i-len(parts)+1])
			continue
		}
		pad := strings.Repeat(" ", len(indent)+xansi.StringWidth(lead))
		wrapped := strings.Split(xansi.Wrap(strings.Join(parts, " "), max(w-len(pad), 8), ""), "\n")
		for k, l := range wrapped {
			if k == 0 {
				out = append(out, indent+lead+l)
			} else {
				out = append(out, pad+l)
			}
		}
	}
	return out
}

// trimRight drops trailing spaces from a styled line: spaces may be followed
// by escape sequences (a reset), which are kept so styling still closes.
func trimRight(s string) string {
	plainText := xansi.Strip(s)
	trimmed := strings.TrimRight(plainText, " \t")
	if len(trimmed) == len(plainText) {
		return s
	}
	n := xansi.StringWidth(trimmed)
	cut := xansi.Truncate(s, n, "")
	// Truncate keeps zero-width sequences that follow the last cell it kept,
	// but not the styling reset of the dropped padding: close it explicitly.
	if strings.Contains(cut, "\x1b[") && !strings.HasSuffix(cut, "\x1b[m") && !strings.HasSuffix(cut, "\x1b[0m") {
		cut += "\x1b[m"
	}
	return cut
}
