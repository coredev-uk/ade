package zellij

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	cases := map[string]string{
		`/usr/bin/ade`:     `"/usr/bin/ade"`,
		`/my dir/ade`:      `"/my dir/ade"`,
		`/a"b/ade`:         `"/a\"b/ade"`,
		`C:\x`:             `"C:\\x"`,
		"/a\nb":            `"/a\nb"`,
		"/a\x01b":          `"/a\u{1}b"`,
		`/ünï/ade`:         `"/ünï/ade"`,
		`/a"; pane "x/ade`: `"/a\"; pane \"x/ade"`,
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestLayout(t *testing.T) {
	got := Layout(`/opt/my "ade"/ade`)
	for _, want := range []string{
		`tab name="home" focus=true`,
		`pane borderless=true focus=true command="/opt/my \"ade\"/ade"`,
		`args "dash"`,
		`plugin location="zellij:tab-bar"`,
		`plugin location="zellij:status-bar"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("layout missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "{") != strings.Count(got, "}") {
		t.Errorf("unbalanced braces:\n%s", got)
	}
}

func TestSessionState(t *testing.T) {
	const listing = `ade-old [Created 1h ago] 
quiet-horse [Created 4m 57s ago] (current)
my-ade [Created 2h ago] 
ade [Created 5days ago] (EXITED - attach to resurrect)
`
	cases := []struct {
		name, listing string
		want          State
	}{
		{"ade", listing, Exited},
		{"ade", "ade [Created 1m ago] (current)\nade-old [Created 1h ago] (EXITED - attach to resurrect)\n", Live},
		{"ade", "ade-old [Created 1h ago] \nxade [Created 1h ago] \n", Absent},
		{"ade", "", Absent},
		{"ade", "\n  \n", Absent},
		{"ade", "ade\n", Live},
		{"quiet-horse", listing, Live},
	}
	for _, c := range cases {
		if got := SessionState(c.listing, c.name); got != c.want {
			t.Errorf("SessionState(%q, %q) = %v, want %v", c.listing, c.name, got, c.want)
		}
	}
}

func TestUnlockFirstPresetIsShippedAndWrittenIdempotently(t *testing.T) {
	for _, want := range []string{`default_mode "locked"`, `bind "Ctrl g" { SwitchToMode "Normal"; }`} {
		if !strings.Contains(UnlockFirstConfig, want) {
			t.Errorf("embedded preset lacks %s", want)
		}
	}
	dir := filepath.Join(t.TempDir(), "ade")
	path, err := WriteUnlockFirst(dir)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, ConfigFileName) {
		t.Fatalf("path %s", path)
	}
	if got, _ := os.ReadFile(path); string(got) != UnlockFirstConfig {
		t.Fatalf("written file differs from the embedded preset")
	}
	// An older ADE's copy is replaced; an identical one is left alone.
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteUnlockFirst(dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != UnlockFirstConfig {
		t.Fatal("stale copy kept")
	}
	before, _ := os.Stat(path)
	if _, err := WriteUnlockFirst(dir); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Error("an identical file was rewritten")
	}
}
