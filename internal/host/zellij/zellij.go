// Package zellij builds the layout and parses session state for the zellij
// host. It is pure: all process execution lives in the caller.
package zellij

import (
	"bytes"
	_ "embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// UnlockFirstConfig is ADE's zellij config for sessions inside a VS Code
// terminal: zellij starts locked so no key collides with VS Code, neovim or
// lazygit, and Ctrl-g unlocks (PLAN 6.7).
//
//go:embed unlock-first.kdl
var UnlockFirstConfig string

// ConfigFileName is the preset's file name in ADE's config directory.
const ConfigFileName = "zellij-unlock-first.kdl"

// WriteUnlockFirst writes the preset to dir/ConfigFileName (replacing a stale
// copy from an older ADE) and returns its path.
func WriteUnlockFirst(dir string) (string, error) {
	path := filepath.Join(dir, ConfigFileName)
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, []byte(UnlockFirstConfig)) {
		return path, nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ConfigFileName+".tmp*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after a successful rename
	if _, err := tmp.WriteString(UnlockFirstConfig); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(tmp.Name(), path)
}

// State is a named session's liveness.
type State int

const (
	// Absent means no session with the name is known.
	Absent State = iota
	// Live means the session is running and can be attached.
	Live
	// Exited means the session is resurrectable but not running.
	Exited
)

// TabTemplate is the KDL of zellij's standard tab chrome: the tab bar on top
// (1 row) and the status bar below (2 rows) around every tab's own panes. A
// layout that names tabs without it (new-tab --layout, --new-session-with-layout)
// gives those tabs no chrome, which hides the session's other tabs. Both
// layouts ADE writes start with it.
const TabTemplate = `    default_tab_template {
        pane size=1 borderless=true {
            plugin location="zellij:tab-bar"
        }
        children
        pane size=2 borderless=true {
            plugin location="zellij:status-bar"
        }
    }
`

// HomeTab is the name of the tab that runs the dashboard.
const HomeTab = "home"

// Layout returns the KDL layout for a session: one borderless "home" tab
// running `<ade> dash`. Paths come from the filesystem, so every string is
// escaped rather than assumed safe.
func Layout(ade string) string {
	var b strings.Builder
	b.WriteString("layout {\n")
	b.WriteString(TabTemplate)
	b.WriteString("    tab name=" + Quote(HomeTab) + " focus=true {\n")
	b.WriteString("        pane borderless=true focus=true command=" + Quote(ade) + " {\n")
	b.WriteString("            args \"dash\"\n")
	b.WriteString("        }\n")
	b.WriteString("    }\n")
	b.WriteString("}\n")
	return b.String()
}

// Quote renders s as a KDL quoted string.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 || r == 0x7f {
				b.WriteString(`\u{` + strconv.FormatInt(int64(r), 16) + `}`)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// SessionState finds the exact-named session in `zellij list-sessions
// --no-formatting` output. The plain (non --short) form is needed because
// only it marks resurrectable sessions with "(EXITED". Prefix matches such as
// "ade-old" never count.
func SessionState(listing, name string) State {
	for line := range strings.SplitSeq(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != name {
			continue
		}
		if strings.Contains(line, "(EXITED") {
			return Exited
		}
		return Live
	}
	return Absent
}
