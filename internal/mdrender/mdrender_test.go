package mdrender

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

const sample = "# Title\n\nSome **bold** and *it* and `code` with a [link](https://example.com/x) that wraps around the width of the terminal nicely.\n\n## Acceptance\n\n- [x] done item\n- [ ] open item that is quite long and will wrap onto the next line for sure\n\n1. one\n2. two\n\n> quoted\n\n```go\nfunc main() {}\n```\n\n---\n\n<!-- hidden -->\n"

func stripped(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

func TestRenderFitsWidthAndTrims(t *testing.T) {
	for _, w := range []int{20, 40, 80} {
		lines := Render(sample, Options{Width: w})
		if len(lines) == 0 || lines[0] == "" || lines[len(lines)-1] == "" {
			t.Fatalf("w=%d: padded start or end: %q", w, lines)
		}
		for _, l := range lines {
			if got := ansi.StringWidth(l); got > w {
				t.Errorf("w=%d: line is %d cells: %q", w, got, ansi.Strip(l))
			}
			if p := ansi.Strip(l); p != strings.TrimRight(p, " ") {
				t.Errorf("w=%d: trailing spaces: %q", w, p)
			}
		}
	}
}

func TestRenderHangsWrappedItems(t *testing.T) {
	got := stripped(Render("- [ ] open item that is quite long and will wrap onto the next line for sure", Options{Width: 30}))
	if len(got) < 3 {
		t.Fatalf("not wrapped: %q", got)
	}
	for _, l := range got[1:] {
		if !strings.HasPrefix(l, "    ") {
			t.Errorf("continuation does not sit under the item text: %q", l)
		}
	}
}

func TestRenderStructure(t *testing.T) {
	got := strings.Join(stripped(Render(sample, Options{Width: 60})), "\n")
	for _, want := range []string{"Title", "[x] done item", "[ ] open item", "1. one", "2. two", "┃ quoted", "`code`", "https://example.com/x", "func main() {}"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "hidden") || strings.Contains(got, "#") {
		t.Errorf("html comment or heading marks leaked:\n%s", got)
	}
}

func TestRenderPaintsNoBackgroundAndNoHyperlinks(t *testing.T) {
	raw := strings.Join(Render(sample, Options{Width: 60}), "\n")
	for _, seq := range []string{"\x1b[4" + "8;", "\x1b[40m", "\x1b[41m", "\x1b[47m", "\x1b]8;"} {
		if strings.Contains(raw, seq) {
			t.Errorf("output holds %q (a painted background or an OSC 8 link)", seq)
		}
	}
	if !strings.Contains(raw, "\x1b[1m") {
		t.Errorf("strong text is not bold: %q", raw)
	}
}

func TestRenderNoColorKeepsWeightOnly(t *testing.T) {
	src := "text **bold**\n\n```go\nfunc main() { return \"x\" }\n```\n"
	colour := strings.Join(Render(src, Options{Width: 40}), "\n")
	if !strings.Contains(colour, "38;2;") {
		t.Fatalf("colour code block carries no colour: %q", colour)
	}
	mono := strings.Join(Render(src, Options{Width: 40, NoColor: true}), "\n")
	if strings.Contains(mono, "38;") || strings.Contains(mono, "48;") || strings.Contains(mono, "\x1b[3") && !strings.Contains(mono, "\x1b[3m") {
		t.Errorf("NO_COLOR output carries colour: %q", mono)
	}
	if !strings.Contains(mono, "\x1b[1m") {
		t.Errorf("NO_COLOR output lost the weight: %q", mono)
	}
}

func TestRenderAsciiUsesAsciiGlyphs(t *testing.T) {
	src := "- one\n- two\n\n> q\n\n---\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n![alt](x.png)\n"
	for _, l := range Render(src, Options{Width: 40, Ascii: true}) {
		for _, r := range ansi.Strip(l) {
			if r > 127 {
				t.Errorf("non-ASCII %q in %q", r, ansi.Strip(l))
			}
		}
	}
	if got := strings.Join(Render("- one", Options{Width: 40}), ""); !strings.Contains(got, "• one") {
		t.Errorf("unicode bullet missing: %q", got)
	}
}

func TestRenderEmptyAndCRLF(t *testing.T) {
	if got := Render("  \n\n ", Options{Width: 20}); got != nil {
		t.Errorf("blank text rendered %q", got)
	}
	got := stripped(Render("one\r\n\r\ntwo\r\n", Options{Width: 20}))
	if strings.Join(got, "|") != "one||two" {
		t.Errorf("CRLF text = %q", got)
	}
}

func TestRenderLongCodeLineIsCutNotOverflowing(t *testing.T) {
	lines := Render("```\n"+strings.Repeat("x", 100)+"\n```", Options{Width: 30})
	for _, l := range lines {
		if ansi.StringWidth(l) > 30 {
			t.Errorf("overflow: %d cells", ansi.StringWidth(l))
		}
	}
	if len(lines) < 4 {
		t.Errorf("long line not cut into rows: %d lines", len(lines))
	}
}
