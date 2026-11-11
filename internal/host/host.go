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
	InsideZellij    bool         // $ZELLIJ is set
	ZellijAvailable bool         // zellij is on PATH
	Session         zellij.State // state of SessionName; ignored unless zellij is usable
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

// Argv builds the zellij command line for a Create or Attach action.
// layoutFile is only used by Create.
func Argv(a Action, state zellij.State, layoutFile string) []string {
	switch a {
	case Create:
		return []string{"zellij", "--session", SessionName, "--new-session-with-layout", layoutFile}
	case Attach:
		if state == zellij.Exited {
			// Resurrecting would otherwise wait for ENTER before running the dashboard.
			return []string{"zellij", "attach", "--force-run-commands", SessionName}
		}
		return []string{"zellij", "attach", SessionName}
	}
	return nil
}
