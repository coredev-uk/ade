// Package clipboard copies text to the user's clipboard with the OSC 52
// terminal escape. It is a clipboard request, not a notification: nothing is
// shown, and it needs no desktop service, so it also works over SSH.
package clipboard

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// MaxBytes is the largest text copied. Terminals and multiplexers cap an
// OSC 52 payload, and a silent truncation would paste half a report.
const MaxBytes = 70_000

// ErrTooLarge says the text exceeds MaxBytes.
var ErrTooLarge = errors.New("clipboard: text too large for an OSC 52 copy")

// Sequence returns the OSC 52 escape that sets the system clipboard to text.
// zellij and the terminals ADE supports pass it through as is.
func Sequence(text string) (string, error) {
	if len(text) > MaxBytes {
		return "", fmt.Errorf("%w (%d bytes, limit %d)", ErrTooLarge, len(text), MaxBytes)
	}
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07", nil
}

// Copy writes the copy request to w, normally the terminal (stdout or
// /dev/tty).
func Copy(w io.Writer, text string) error {
	seq, err := Sequence(text)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, seq)
	return err
}
