package clipboard

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSequenceBareOSC52(t *testing.T) {
	got, err := Sequence("héllo\nworld")
	if err != nil {
		t.Fatal(err)
	}
	// base64 of the UTF-8 bytes, BEL-terminated, clipboard selection "c".
	if want := "\x1b]52;c;aMOpbGxvCndvcmxk\x07"; got != want {
		t.Fatalf("got %q, want %q", got, want)
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
