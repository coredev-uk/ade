// Package host decides how bare `ade` presents the dashboard: inside zellij,
// by attaching to or creating the `ade` session, or directly in this process.
package host

import (
	"github.com/coredev-uk/ade/internal/host/zellij"
)

// SessionName is the zellij session ADE owns.
const SessionName = "ade"

// Mode values from config [host].mode.
const (
	ModeAuto   = "auto"
	ModeZellij = "zellij"
	ModeVSCode = "vscode"
	ModePlain  = "plain"
)

// Action is what the launcher should do.
type Action int

const (
	// RunHere runs the dashboard in this process.
	RunHere Action = iota
	// Attach execs zellij to attach to the existing (or resurrectable) session.
	Attach
	// Create execs zellij to create the session from the generated layout.
	Create
	// FocusHome runs inside the `ade` session: focus (or recreate) its home
	// tab, which already shows the dashboard, and exit.
	FocusHome
	// OpenHome runs inside another zellij session, which cannot be nested:
	// open (or focus) a home tab running `ade dash` in that session and exit.
	OpenHome
)

// Env is the environment the decision depends on.
type Env struct {
	Mode         string
	InsideZellij bool // $ZELLIJ is set
	// CurrentSession is $ZELLIJ_SESSION_NAME, the session ade runs inside.
	CurrentSession  string
	ZellijAvailable bool // zellij is on PATH
	// InsideVSCode is true in a terminal VS Code started (also the case for a
	// new zellij session created from it); such a session runs with ADE's
	// unlock-first config so no key collides with VS Code.
	InsideVSCode bool
	// WebTTY is true in a browser terminal, which captures keys of its own.
	WebTTY  bool
	Session zellij.State // state of SessionName; ignored unless zellij is usable
}

// Decide maps the environment to an action. mode "zellij" behaves like auto
// but is not silently downgraded by the caller: it reports ok=false when
// zellij is required and missing.
func Decide(e Env) (a Action, ok bool) {
	switch e.Mode {
	case ModePlain, ModeVSCode:
		return RunHere, true
	case ModeAuto, "", ModeZellij:
	default:
		return RunHere, true
	}
	if e.InsideZellij {
		switch e.CurrentSession {
		case "":
			return RunHere, true // cannot tell which session this is: leave the pane alone
		case SessionName:
			return FocusHome, true
		}
		return OpenHome, true
	}
	if !e.ZellijAvailable {
		return RunHere, e.Mode != ModeZellij
	}
	if e.Session == zellij.Absent {
		return Create, true
	}
	return Attach, true
}

// FocusesHomeFirst says whether the launcher moves the session to its home
// tab before attaching: a live session otherwise opens on whatever tab was
// focused last, often a workspace tab. An exited session is resurrected by
// attaching and cannot be addressed until then.
func FocusesHomeFirst(a Action, state zellij.State) bool {
	return a == Attach && state == zellij.Live
}

// PresetEnv is the environment variable that forces zellij's keybinding
// preset: "unlock-first" always uses ADE's preset, "default" (or "none")
// never does. Unset, the preset follows the terminal (see UsesUnlockFirst).
const PresetEnv = "ADE_ZELLIJ_PRESET"

// WebTTY reports whether the terminal looks like a browser terminal (Coder's
// web terminal, ttyd). A browser takes Ctrl-W, Ctrl-T, Ctrl-N and friends for
// itself, so such a terminal wants the same unlock-first preset as VS Code.
// ADE_WEB_TTY=1 says so explicitly. Without it the guess is Coder's
// reconnecting-pty: a Coder workspace ($CODER=true) whose terminal is not an
// ssh session and not VS Code's.
func WebTTY(getenv func(string) string) bool {
	switch getenv("ADE_WEB_TTY") {
	case "1", "true":
		return true
	case "0", "false":
		return false
	}
	return getenv("CODER") == "true" && getenv("SSH_CONNECTION") == "" && getenv("SSH_TTY") == "" &&
		getenv("TERM_PROGRAM") != "vscode" && getenv("VSCODE_IPC_HOOK_CLI") == ""
}

// UsesUnlockFirst says whether zellij should run with ADE's unlock-first
// config: inside VS Code or a web tty, unless the user pointed zellij at a
// config of their own with $ZELLIJ_CONFIG_FILE. ADE_ZELLIJ_PRESET overrides
// the guess in both directions, and unlike detection it also beats
// $ZELLIJ_CONFIG_FILE: asking for the preset by name is explicit.
func (e Env) UsesUnlockFirst(getenv func(string) string) bool {
	switch getenv(PresetEnv) {
	case "unlock-first":
		return true
	case "default", "none":
		return false
	}
	return (e.InsideVSCode || e.WebTTY) && getenv("ZELLIJ_CONFIG_FILE") == ""
}

// Argv builds the zellij command line for a Create or Attach action.
// layoutFile is only used by Create; a non-empty configFile is passed as
// --config.
func Argv(a Action, state zellij.State, layoutFile, configFile string) []string {
	argv := []string{"zellij"}
	if configFile != "" {
		argv = append(argv, "--config", configFile)
	}
	switch a {
	case Create:
		return append(argv, "--session", SessionName, "--new-session-with-layout", layoutFile)
	case Attach:
		if state == zellij.Exited {
			// Resurrecting would otherwise wait for ENTER before running the dashboard.
			return append(argv, "attach", "--force-run-commands", SessionName)
		}
		return append(argv, "attach", SessionName)
	}
	return nil
}
