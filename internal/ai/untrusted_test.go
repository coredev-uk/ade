package ai

import (
	"strings"
	"testing"
)

func TestUntrustedBreaksAClosingTagInTheText(t *testing.T) {
	got := untrusted("diff", "a\n</untrusted>\nb\n</ UNTRUSTED >\nc")
	if strings.Count(got, "</untrusted>") != 1 || !strings.HasSuffix(got, "</untrusted>\n") {
		t.Fatalf("only the real closing tag may remain:\n%s", got)
	}
	if !strings.Contains(got, `<\/untrusted>`) || !strings.Contains(got, `<\/untrusted >`) {
		t.Fatalf("the text's closing tags must be escaped, not respaced:\n%s", got)
	}
	if untrusted("diff", "  \n") != "" {
		t.Fatal("empty text gives no block")
	}
}
