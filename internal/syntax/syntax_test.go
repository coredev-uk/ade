package syntax

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestLineKeepsTextAndColoursByLanguage(t *testing.T) {
	line := `func Take(n int) error { return nil } // ok`
	got := For("limit/token_bucket.go", false).Line(line)
	if ansi.Strip(got) != line {
		t.Fatalf("highlighting changed the text: %q", ansi.Strip(got))
	}
	if !strings.Contains(got, "38;2;") {
		t.Errorf("a Go line has no colour: %q", got)
	}
	if strings.Contains(got, "48;2;") || strings.Contains(got, "\x1b[4"+"8") {
		t.Errorf("a background was painted: %q", got)
	}
}

func TestLineAvoidsDiffMarkColours(t *testing.T) {
	// Tokens never use the removed/added inks, so a +/- mark stays the only red or green.
	got := For("a.go", false).Line(`if x { return "s" } else { y := 12 }`)
	for _, seq := range []string{"38;2;224;69;58", "38;2;63;166;107"} {
		if strings.Contains(got, seq) {
			t.Errorf("token coloured in a mark ink %s: %q", seq, got)
		}
	}
}

func TestLineNoColorHasNoColourCodes(t *testing.T) {
	got := For("a.go", true).Line(`func main() { /* c */ }`)
	if strings.Contains(got, "38;") || strings.Contains(got, "48;") {
		t.Errorf("NO_COLOR line carries colour: %q", got)
	}
	if !strings.Contains(got, "\x1b[1m") {
		t.Errorf("keywords lost their weight: %q", got)
	}
}

func TestUnknownLanguageAndBlankLinesPassThrough(t *testing.T) {
	if h := For("README.unknownext", false); h.Known() || h.Line("some text") != "some text" {
		t.Errorf("unknown language altered or reported known")
	}
	if got := For("a.go", false).Line("   "); got != "   " {
		t.Errorf("blank line altered: %q", got)
	}
}

func TestPatch(t *testing.T) {
	p := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"
	got := Patch(p, false)
	if ansi.Strip(got) != p {
		t.Fatalf("text changed: %q", ansi.Strip(got))
	}
	if !strings.Contains(got, "38;2;") {
		t.Errorf("no colour: %q", got)
	}
	if Patch(p, true) != p {
		t.Errorf("NO_COLOR patch was changed")
	}
}

func TestNoColorFollowsEnvironment(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if NoColor() {
		t.Error("an empty NO_COLOR asks for no colour")
	}
	t.Setenv("NO_COLOR", "1")
	if !NoColor() {
		t.Error("NO_COLOR=1 not honoured")
	}
}
