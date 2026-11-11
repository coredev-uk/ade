// Package clipboard copies text to the user's clipboard with the OSC 52
// terminal escape. It is a clipboard request, not a notification: nothing is
// shown, and it needs no desktop service, so it also works over SSH.
package clipboard

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// MaxBytes is the largest text copied. Terminals and multiplexers cap an
// OSC 52 payload (tmux drops it above roughly 100 KB), and a silent
// truncation would paste half a report.
const MaxBytes = 70_000

// ErrTooLarge says the text exceeds MaxBytes.
var ErrTooLarge = errors.New("clipboard: text too large for an OSC 52 copy")

// Sequence returns the escape sequence that sets the system clipboard to
// text. Under tmux (TMUX set) it is wrapped in tmux's DCS passthrough, which
// needs `set -g allow-passthrough on`; elsewhere (including zellij) it is the
// bare OSC 52.
func Sequence(text string) (string, error) { return sequence(text, os.Getenv("TMUX") != "") }

func sequence(text string, tmux bool) (string, error) {
	if len(text) > MaxBytes {
		return "", fmt.Errorf("%w (%d bytes, limit %d)", ErrTooLarge, len(text), MaxBytes)
	}
	osc := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
	if tmux {
		// Inside the DCS every ESC is doubled.
		return "\x1bPtmux;" + strings.ReplaceAll(osc, "\x1b", "\x1b\x1b") + "\x1b\\", nil
	}
	return osc, nil
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
