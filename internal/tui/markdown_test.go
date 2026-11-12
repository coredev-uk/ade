package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

func TestMarkdownPreviewRendersAndCuts(t *testing.T) {
	m := model{g: unicodeGlyphs}
	src := "intro **bold**\n\n" + strings.Repeat("- item\n", 12)
	got := m.mdPreview(src, 40, 8)
	if len(got) != 8 {
		t.Fatalf("%d lines, want 8", len(got))
	}
	if last := ansi.Strip(got[7]); last != "…" {
		t.Errorf("a cut preview should end in a faint …, got %q", last)
	}
	if !strings.Contains(got[0], "\x1b[1mbold") {
		t.Errorf("**bold** is not bold: %q", got[0])
	}
	if joined := ansi.Strip(strings.Join(got, "\n")); strings.Contains(joined, "**") || !strings.Contains(joined, "• item") {
		t.Errorf("markdown marks leaked or the list lost its bullets:\n%s", joined)
	}
	if short := m.mdPreview("one line", 40, 8); len(short) != 1 {
		t.Errorf("a short text grew: %q", short)
	}
}

func TestMarkdownFollowsGlyphSetAndNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	src := "- item\n\n> quote\n\n```go\nfunc main() {}\n```\n"
	asc := strings.Join(model{g: asciiGlyphs}.mdLines(src, 40), "\n")
	for _, r := range ansi.Strip(asc) {
		if r > 127 {
			t.Errorf("ascii glyph set drew %q in\n%s", r, asc)
		}
	}
	t.Setenv("NO_COLOR", "1")
	mono := strings.Join(model{g: unicodeGlyphs}.mdLines(src, 40), "\n")
	if strings.Contains(mono, "38;") {
		t.Errorf("NO_COLOR still colours the code block: %q", mono)
	}
}

// An AI summary is markdown too: its block renders it the same way.
func TestAISummaryRendersMarkdown(t *testing.T) {
	m := model{g: unicodeGlyphs, mr: &mrPage{}}
	st := &aiText{l: load{st: loadOK}, text: "Adds a **token bucket**:\n\n- limits per tenant\n- returns `429`", model: "small"}
	got := strings.Join(m.aiBlock("Summary", st, 60), "\n")
	plain := ansi.Strip(got)
	for _, want := range []string{"Adds a token bucket:", "• limits per tenant", "`429`"} {
		if !strings.Contains(plain, want) {
			t.Errorf("summary lacks %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "**") {
		t.Errorf("summary shows raw markdown marks:\n%s", plain)
	}
}

// --- syntax highlighting in the diff ----------------------------------------------

func diffLine(m model, path string, l review.Line) string { return m.codeLine(path, l, 100, false) }

func TestDiffLinesAreHighlightedByExtension(t *testing.T) {
	t.Setenv("NO_COLOR", "") // the test environment may have it set
	m := model{g: unicodeGlyphs}
	add := review.Line{New: 3, Kind: '+', Text: "func Take(n int) error { return nil }"}
	del := review.Line{Old: 3, Kind: '-', Text: `x := "old"`}

	got := diffLine(m, "limit/bucket.go", add)
	if ansi.Strip(got) != ansi.Strip(diffLine(m, "limit/NOTES", add)) {
		t.Errorf("highlighting changed the text:\n%q\n%q", ansi.Strip(got), ansi.Strip(diffLine(m, "limit/NOTES", add)))
	}
	if !strings.Contains(got, "38;2;176;95;208") {
		t.Errorf("a Go keyword carries no keyword ink: %q", got)
	}
	// The mark is still the added ink; a removed line keeps the removed ink on its mark.
	if !strings.Contains(got, "\x1b[38;2;63;166;107m+") && !strings.Contains(got, "63;166;107m+") {
		t.Errorf("the + mark lost its ink: %q", got)
	}
	if rm := diffLine(m, "a.go", del); !strings.Contains(rm, "224;69;58m-") {
		t.Errorf("the - mark lost its ink: %q", rm)
	}
	// An unknown extension keeps the line-wide mark colour.
	if plain := diffLine(m, "limit/NOTES", add); !strings.Contains(plain, "63;166;107m+func Take") {
		t.Errorf("a file with no known language lost the added ink: %q", plain)
	}
	// Under NO_COLOR keywords keep their weight, nothing carries colour.
	t.Setenv("NO_COLOR", "1")
	mono := diffLine(m, "limit/bucket.go", add)
	if strings.Contains(strings.ReplaceAll(mono, "63;166;107m+", ""), "38;2;176") {
		t.Errorf("NO_COLOR line has a token colour: %q", mono)
	}
	if !strings.Contains(mono, "\x1b[1m") {
		t.Errorf("NO_COLOR keywords lost their weight: %q", mono)
	}
}

func TestDiffLineTruncatesHighlightedText(t *testing.T) {
	m := model{g: unicodeGlyphs}
	l := review.Line{New: 1, Kind: ' ', Text: `func ` + strings.Repeat("Name", 40) + `() {}`}
	got := m.codeLine("a.go", l, 40, false)
	if w := ansi.StringWidth(got); w > 40 {
		t.Errorf("line is %d cells, want at most 40", w)
	}
	if !strings.Contains(ansi.Strip(got), "…") {
		t.Errorf("a cut line should end in …: %q", ansi.Strip(got))
	}
}

func TestLangPathUsesTheOldPathOfADeletedFile(t *testing.T) {
	if got := langPath(forge.DiffFile{OldPath: "a/b.rs", NewPath: "a/b.rs", Deleted: true}); got != "a/b.rs" {
		t.Errorf("langPath = %q", got)
	}
	if got := langPath(forge.DiffFile{OldPath: "x.py", NewPath: "y.go"}); got != "y.go" {
		t.Errorf("langPath = %q", got)
	}
}

// Without delta and without a pager of the user's own, the patch is coloured
// here for `less -R`; with either it is left for them.
func TestPagerPatchIsColouredOnlyForTheDefaultPager(t *testing.T) {
	none := func(string) string { return "" }
	if got := pagerLine("/tmp/p", false, "", none); got != defaultPagerLine("/tmp/p") {
		t.Errorf("default pager line %q != %q", got, defaultPagerLine("/tmp/p"))
	}
	if got := pagerLine("/tmp/p", true, "", none); got == defaultPagerLine("/tmp/p") {
		t.Errorf("delta must not be treated as the default pager: %q", got)
	}
	if got := pagerLine("/tmp/p", false, "diff-so-fancy | less", none); got == defaultPagerLine("/tmp/p") {
		t.Errorf("a configured pager must not be treated as the default: %q", got)
	}
}
