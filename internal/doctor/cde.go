package doctor

import (
	"runtime"

	"github.com/coredev-uk/ade/internal/editor"
)

const cdeGroup = "cde"

// editorEnv is the process environment as the editor resolution sees it.
func editorEnv(o Options) editor.Env {
	return editor.Env{
		TermProgram: o.Getenv("TERM_PROGRAM"), VSCodeIPC: o.Getenv("VSCODE_IPC_HOOK_CLI"),
		Visual: o.Getenv("VISUAL"), Editor: o.Getenv("EDITOR"),
		Headless: headless(o),
		Home:     o.Getenv("HOME"), RuntimeDir: o.Getenv("XDG_RUNTIME_DIR"),
		LookPath: o.LookPath,
	}
}

// headless is a Linux session with no display.
func headless(o Options) bool {
	return runtime.GOOS == "linux" && o.Getenv("DISPLAY") == "" && o.Getenv("WAYLAND_DISPLAY") == ""
}

// cdeChecks covers what a headless cloud development environment (Coder over
// VS Code's remote terminal or a web tty) needs: how ADE opens windows and
// links without a desktop, and the tools repos bring their shells with. All
// of it is advice: nothing here makes ADE unusable, so none of it is an error.
func cdeChecks(o Options) []Check {
	env := editorEnv(o)
	return []Check{
		displayCheck(o),
		vscodeRemoteCheck(o, env),
		{
			Group: cdeGroup, Name: "clipboard", Status: OK,
			Detail: "`y` and `o` (no browser) copy through OSC 52: VS Code's terminal and Coder's web terminal pass it to your browser; " +
				"if a paste comes back empty the terminal blocks it, and the URL is shown in the pop-up instead",
		},
		toolCheck(o, "nix", "devshell: repos with a flake.nix devShell run their panes and tasks through `nix develop`",
			"install nix, or set repos.<r>.devshell = \"none\" in the manifest"),
		toolCheck(o, "direnv", "devshell: repos with an .envrc run their panes and tasks through `direnv exec`",
			"install direnv, or set repos.<r>.devshell = \"none\" in the manifest"),
	}
}

func displayCheck(o Options) Check {
	c := Check{Group: cdeGroup, Name: "display", Status: OK}
	switch {
	case headless(o):
		c.Detail = "headless: no DISPLAY or WAYLAND_DISPLAY; links are copied (OSC 52) instead of opened, and no desktop notifier is needed"
	case runtime.GOOS == "linux":
		c.Detail = "display available"
	default:
		c.Detail = "desktop session"
	}
	return c
}

func vscodeRemoteCheck(o Options, env editor.Env) Check {
	c := Check{Group: cdeGroup, Name: "vscode remote CLI"}
	ed := editor.Resolve(editor.SettingCode, env)
	switch {
	case ed.Kind == editor.Code && ed.Remote:
		c.Status, c.Detail = OK, ed.String()+": `v` and `ade vscode workspace --open` open in the connected VS Code window"
		if ed.IPC != "" {
			c.Detail += "; the inherited VSCODE_IPC_HOOK_CLI is stale, using " + ed.IPC
		}
	case ed.Kind == editor.Code && env.InVSCode():
		c.Status, c.Detail = OK, ed.String()+" (the VS Code window's own CLI)"
	case ed.Kind == editor.Code:
		c.Status, c.Detail = OK, ed.String()+" (outside a VS Code terminal)"
	case env.InVSCode():
		c.Status = Warn
		c.Detail = "in a VS Code terminal but no `code` CLI was found"
		c.Fix = "open a new VS Code terminal (it puts the remote `code` on PATH), or run: Shell Command: Install 'code' command in PATH"
	case headless(o):
		c.Status = OK
		c.Detail = "no VS Code client on this terminal (web tty or plain ssh): `v` prints the workspace path and how to open it"
	default:
		c.Status, c.Detail = OK, "not in a VS Code terminal"
	}
	return c
}

func toolCheck(o Options, name, ok, fix string) Check {
	c := Check{Group: cdeGroup, Name: name}
	if p, err := o.LookPath(name); err == nil {
		c.Status, c.Detail = OK, p+" — "+ok
		return c
	}
	c.Status, c.Detail, c.Fix = Warn, "not on PATH", fix
	return c
}
