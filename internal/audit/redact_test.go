package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	tests := map[string]string{
		"git clone https://oauth2:glpat-abcdefghij0123456789@gitlab.com/a/b.git failed": "git clone https://***@gitlab.com/a/b.git failed",
		"fatal: unable to access 'https://tok123@host/x.git/'":                          "fatal: unable to access 'https://***@host/x.git/'",
		"remote is ssh://git@github.com/a/b.git":                                        "remote is ssh://git@github.com/a/b.git",
		"Authorization: Basic b2F1dGgyOnNlY3JldHRva2Vu":                                 "Authorization: Basic ***",
		"http.extraHeader=Authorization: Bearer abcdef0123456789":                       "http.extraHeader=Authorization: Bearer ***",
		"token glpat-abcdefghij0123456789 in text":                                      "token *** in text",
		"ghp_abcdefghij0123456789ABCDEFGHIJ and github_pat_11AAAAAAA0abcdefghijkl_xyz":  "*** and ***",
		"plain text with glob-patterns and nothing secret":                              "plain text with glob-patterns and nothing secret",
	}
	for in, want := range tests {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestAppendRedactsEntry(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, "user")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "glpat-abcdefghij0123456789"
	err = l.Append(Entry{
		Action: "x.y", Target: "https://u:" + secret + "@h/r.git", Error: "boom " + secret,
		Params: map[string]any{"args": []string{"--token", secret}, "nested": map[string]any{"k": []any{secret}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(l.Dir, "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("secret reached disk: %s", raw)
	}
	if !strings.Contains(string(raw), "boom ***") {
		t.Errorf("error not redacted in place: %s", raw)
	}
}
