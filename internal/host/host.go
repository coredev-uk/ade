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
)

// Env is the environment the decision depends on.
type Env struct {
	Mode            string
	InsideZellij    bool // $ZELLIJ is set
	ZellijAvailable bool // zellij is on PATH
	// InsideVSCode is true in a terminal VS Code started (also the case for a
	// new zellij session created from it); such a session runs with ADE's
	// unlock-first config so no key collides with VS Code.
	InsideVSCode bool
	Session      zellij.State // state of SessionName; ignored unless zellij is usable
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
		return RunHere, true
	}
	if !e.ZellijAvailable {
		return RunHere, e.Mode != ModeZellij
	}
	if e.Session == zellij.Absent {
		return Create, true
	}
	return Attach, true
}

// UsesUnlockFirst says whether zellij should run with ADE's unlock-first
// config: inside VS Code, unless the user pointed zellij at a config of their
// own with $ZELLIJ_CONFIG_FILE.
func (e Env) UsesUnlockFirst(getenv func(string) string) bool {
	return e.InsideVSCode && getenv("ZELLIJ_CONFIG_FILE") == ""
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
