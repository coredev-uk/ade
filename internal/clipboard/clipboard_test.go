package clipboard

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSequenceBareOSC52(t *testing.T) {
	got, err := sequence("héllo\nworld", false)
	if err != nil {
		t.Fatal(err)
	}
	// base64 of the UTF-8 bytes, BEL-terminated, clipboard selection "c".
	if want := "\x1b]52;c;aMOpbGxvCndvcmxk\x07"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSequenceTmuxPassthroughDoublesEscapes(t *testing.T) {
	got, err := sequence("x", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := "\x1bPtmux;\x1b\x1b]52;c;eA==\x07\x1b\\"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSequenceUsesTmuxFromEnvironment(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	got, _ := Sequence("x")
	if !strings.HasPrefix(got, "\x1bPtmux;") {
		t.Fatalf("under tmux got %q", got)
	}
	t.Setenv("TMUX", "")
	if got, _ = Sequence("x"); strings.HasPrefix(got, "\x1bP") {
		t.Fatalf("outside tmux got %q", got)
	}
}

func TestCopyRefusesOversizedTextInsteadOfTruncating(t *testing.T) {
	var w bytes.Buffer
	err := Copy(&w, strings.Repeat("a", MaxBytes+1))
	if !errors.Is(err, ErrTooLarge) || w.Len() != 0 {
		t.Fatalf("err = %v, wrote %d bytes", err, w.Len())
	}
	if err := Copy(&w, strings.Repeat("a", MaxBytes)); err != nil || w.Len() == 0 {
		t.Fatalf("max size must copy: %v", err)
	}
}
