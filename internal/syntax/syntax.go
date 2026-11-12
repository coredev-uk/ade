// Package syntax is ADE's chroma wrapper: terminal syntax highlighting for the
// merge request diff, for code blocks in rendered markdown, and for a patch
// handed to a pager that cannot colour it.
//
// The palette follows DESIGN.md's Interchange direction: the terminal's own
// ground (no background is ever painted) and a few of the line inks for
// tokens, none of them the red and green that mark removed and added lines.
// Without colour (NO_COLOR, or a caller that asks for none) tokens keep only
// their weight: keywords bold, comments italic, so code still reads.
package syntax

import (
	"os"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// Style names, registered so glamour's code blocks can use them by name.
const (
	ColourStyle = "ade"
	MonoStyle   = "ade-mono"
)

// Formatter is the chroma formatter ADE writes through: true colour, which
// Bubble Tea's output downsamples to what the terminal has.
const Formatter = "terminal16m"

var (
	colourStyle = chroma.MustNewStyle(ColourStyle, chroma.StyleEntries{
		chroma.Comment:             "italic #7a7a7a",
		chroma.CommentPreproc:      "#7a7a7a",
		chroma.Keyword:             "bold #b05fd0",
		chroma.KeywordType:         "#3b7dd8",
		chroma.Operator:            "#999999",
		chroma.Punctuation:         "#999999",
		chroma.NameBuiltin:         "#3b7dd8",
		chroma.NameFunction:        "#3b7dd8",
		chroma.NameClass:           "bold #3b7dd8",
		chroma.NameDecorator:       "#2bb3b3",
		chroma.NameConstant:        "#e8a33d",
		chroma.NameTag:             "#b05fd0",
		chroma.NameAttribute:       "#3b7dd8",
		chroma.LiteralString:       "#2bb3b3",
		chroma.LiteralStringEscape: "#e8a33d",
		chroma.LiteralNumber:       "#e8a33d",
		chroma.GenericHeading:      "bold",
		chroma.GenericSubheading:   "bold #3b7dd8",
		chroma.GenericEmph:         "italic",
		chroma.GenericStrong:       "bold",
	})
	monoStyle = chroma.MustNewStyle(MonoStyle, chroma.StyleEntries{
		chroma.Comment:        "italic",
		chroma.CommentPreproc: "italic",
		chroma.Keyword:        "bold",
		chroma.NameClass:      "bold",
		chroma.GenericHeading: "bold",
		chroma.GenericEmph:    "italic",
		chroma.GenericStrong:  "bold",
	})
)

func init() {
	styles.Register(colourStyle)
	styles.Register(monoStyle)
}

// NoColor reports whether the environment asks for no colour (NO_COLOR set to
// anything, https://no-color.org).
func NoColor() bool { return os.Getenv("NO_COLOR") != "" }

// StyleName is the registered chroma style for a colour choice.
func StyleName(noColor bool) string {
	if noColor {
		return MonoStyle
	}
	return ColourStyle
}

// Highlighter colours single lines of one language, picked by file name.
type Highlighter struct {
	lexer chroma.Lexer
	style *chroma.Style
	fmtr  chroma.Formatter
	name  string
}

// highlighters are built once per path and colour choice: a diff asks for
// one on every row of every frame.
var highlighters sync.Map // forKey -> *Highlighter

type forKey struct {
	path    string
	noColor bool
}

// For returns the highlighter for a path. A path whose language is not known
// gets a highlighter that leaves lines as they are.
func For(path string, noColor bool) *Highlighter {
	k := forKey{path, noColor}
	if h, ok := highlighters.Load(k); ok {
		return h.(*Highlighter)
	}
	h := &Highlighter{fmtr: formatters.Get(Formatter), style: colourStyle}
	if noColor {
		h.style = monoStyle
	}
	if l := lexers.Match(path); l != nil {
		h.lexer = chroma.Coalesce(l)
		h.name = l.Config().Name
	}
	highlighters.Store(k, h)
	return h
}

// Known reports whether the path's language is recognised.
func (h *Highlighter) Known() bool { return h != nil && h.lexer != nil }

// cache keeps highlighted lines: a diff redraws every line on every frame.
var (
	cacheMu sync.Mutex
	cache   = map[string]string{}
)

const cacheMax = 4096

// Line colours one line of code (no newline in it). Unknown languages and
// lexer failures return the line unchanged.
func (h *Highlighter) Line(s string) string {
	if !h.Known() || strings.TrimSpace(s) == "" {
		return s
	}
	key := h.style.Name + "\x00" + h.name + "\x00" + s
	cacheMu.Lock()
	out, ok := cache[key]
	cacheMu.Unlock()
	if ok {
		return out
	}
	out = h.format(s)
	cacheMu.Lock()
	if len(cache) >= cacheMax {
		clear(cache)
	}
	cache[key] = out
	cacheMu.Unlock()
	return out
}

func (h *Highlighter) format(s string) string {
	it, err := h.lexer.Tokenise(nil, s)
	if err != nil {
		return s
	}
	var b strings.Builder
	if err := h.fmtr.Format(&b, h.style, it); err != nil {
		return s
	}
	return b.String()
}

// Patch colours a unified diff for a pager that does not (git's default
// `less -R`): file headers bold, hunk headers in the info ink, removed and
// added lines in their marks' colours. Without colour it returns the patch
// untouched, since +/- already carry the meaning.
func Patch(patch string, noColor bool) string {
	if noColor {
		return patch
	}
	l := lexers.Get("diff")
	if l == nil {
		return patch
	}
	it, err := chroma.Coalesce(l).Tokenise(nil, patch)
	if err != nil {
		return patch
	}
	var b strings.Builder
	if err := formatters.Get(Formatter).Format(&b, patchStyle, it); err != nil {
		return patch
	}
	return b.String()
}

var patchStyle = chroma.MustNewStyle("ade-patch", chroma.StyleEntries{
	chroma.GenericHeading:    "bold",
	chroma.GenericSubheading: "#3b7dd8",
	chroma.GenericDeleted:    "#e0453a",
	chroma.GenericInserted:   "#3fa66b",
	chroma.Comment:           "bold",
})
