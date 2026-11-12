// Package goldentest drives a Bubble Tea model through a real tea.Program
// (charmbracelet's teatest) and compares what it shows with golden files, for
// the dashboard and the overview pane. Test code only: nothing outside
// *_test.go files imports it.
//
// Golden files live in testdata/golden/<name>.golden of the calling package.
// Run the tests with -update to rewrite them, then read the diff: a golden
// file is the screen, so a changed one is a changed UI.
package goldentest

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

// Drive is what a scenario does with the running program: send keys, wait.
type Drive func(tm *teatest.TestModel)

// prog hosts a model in a real program without its Init: the scenarios start
// from a state they built (data already loaded, no timers, no network), and
// every tick or spinner message the model would schedule is dropped so the
// final frame is the same on every run.
type prog struct {
	inner tea.Model
	drop  func(tea.Msg) bool
}

func (p prog) Init() tea.Cmd { return nil }

func (p prog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if p.drop != nil && p.drop(msg) {
		return p, nil
	}
	next, cmd := p.inner.Update(msg)
	p.inner = next
	return p, cmd
}

func (p prog) View() tea.View { return p.inner.View() }

// Run starts the model in a program of the given size, applies drive, quits,
// and returns the last frame the model drew. drop filters messages (ticks)
// out of the model's loop.
func Run(t testing.TB, m tea.Model, w, h int, drop func(tea.Msg) bool, drive Drive) string {
	t.Helper()
	tm := teatest.NewTestModel(t, prog{inner: m, drop: drop}, teatest.WithInitialTermSize(w, h))
	if drive != nil {
		drive(tm)
	}
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	fin, ok := tm.FinalModel(t, teatest.WithFinalTimeout(10*time.Second)).(prog)
	if !ok {
		t.Fatal("the program ended without its model")
	}
	return fin.inner.View().Content
}

// Plain is a frame as text: no escape sequences, no trailing spaces.
func Plain(frame string) string {
	lines := strings.Split(ansi.Strip(frame), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

// NoColor is the frame as a terminal with NO_COLOR shows it: Bubble Tea's own
// colour downsampling at the Ascii profile, which drops every colour and keeps
// bold, faint, italic, underline and reverse. The escape sequences stay in the
// result so the golden file proves what is (and is not) there.
func NoColor(frame string) string {
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.Ascii}
	if _, err := w.WriteString(frame); err != nil {
		panic(err)
	}
	lines := strings.Split(buf.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

// HasColour reports whether s carries a colour sequence (SGR 30-37, 38, 40-47,
// 48, 90-97, 100-107).
func HasColour(s string) bool {
	for rest := s; ; {
		i := strings.Index(rest, "\x1b[")
		if i < 0 {
			return false
		}
		rest = rest[i+2:]
		j := strings.IndexFunc(rest, func(r rune) bool { return r >= 0x40 && r <= 0x7e })
		if j < 0 || rest[j] != 'm' {
			continue
		}
		for _, p := range strings.Split(rest[:j], ";") {
			n, err := strconv.Atoi(p)
			if err != nil {
				continue
			}
			if n >= 30 && n <= 38 || n >= 40 && n <= 48 || n >= 90 && n <= 107 {
				return true
			}
		}
	}
}

// Equal compares got with testdata/golden/<name>.golden, or rewrites the file
// under -update. Escape sequences are compared as written; the failure shows
// the first differing line with the sequences spelled out.
func Equal(t testing.TB, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".golden")
	if f := flag.Lookup("update"); f != nil && f.Value.String() == "true" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\nrun the tests with -update to create it", err)
	}
	if string(want) == got {
		return
	}
	wl, gl := strings.Split(string(want), "\n"), strings.Split(got, "\n")
	for i := 0; i < max(len(wl), len(gl)); i++ {
		var a, b string
		if i < len(wl) {
			a = wl[i]
		}
		if i < len(gl) {
			b = gl[i]
		}
		if a != b {
			t.Fatalf("%s differs from the golden file at line %d (run with -update to accept):\n golden: %s\n    got: %s\n\nfull frame:\n%s",
				name, i+1, strconv.Quote(a), strconv.Quote(b), ansi.Strip(got))
		}
	}
}
