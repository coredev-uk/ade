package editor

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// remoteCLIGlobs locate the editor server's remote CLI, newest server layout
// first: VS Code's server under $HOME (`code`), then code-server's (`code-server`,
// the browser VS Code a CDE serves when the desktop app cannot connect) in its
// Coder module, install.sh and package locations. A leading "~/" is $HOME. The
// integrated terminal puts one of them on PATH, but a shell that outlives the
// window (a zellij session, tmux) or a process started by another tool may not
// have it.
var remoteCLIGlobs = []string{
	"~/.vscode-server/cli/servers/*/server/bin/remote-cli/code",
	"~/.vscode-server/bin/*/bin/remote-cli/code",
	"/tmp/code-server/lib/vscode/bin/remote-cli/code-server",
	"~/.local/lib/code-server-*/lib/vscode/bin/remote-cli/code-server",
	"/usr/lib/code-server/lib/vscode/bin/remote-cli/code-server",
}

// ipcSocketGlobs find the sockets VS Code's remote CLI talks to, relative to
// the runtime directory and /tmp.
var ipcSocketGlobs = []string{"vscode-ipc-*.sock"}

func (e Env) glob(pattern string) []string {
	f := e.Glob
	if f == nil {
		f = filepath.Glob
	}
	m, err := f(pattern)
	if err != nil {
		return nil
	}
	return m
}

func (e Env) modTime(path string) (time.Time, bool) {
	if e.ModTime != nil {
		return e.ModTime(path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// newest picks the most recently modified of paths ("" for none).
func (e Env) newest(paths []string) string {
	best, bestAt := "", time.Time{}
	for _, p := range paths {
		if at, ok := e.modTime(p); ok && (best == "" || at.After(bestAt)) {
			best, bestAt = p, at
		}
	}
	return best
}

// isRemoteCLI reports whether path is the VS Code server's remote CLI rather
// than a desktop launcher: the server ships it under .../bin/remote-cli/.
func isRemoteCLI(path string) bool {
	return strings.Contains(filepath.ToSlash(path), "/remote-cli/")
}

// findRemoteCLI looks for a remote CLI an editor server installed.
func (e Env) findRemoteCLI() string {
	var all []string
	for _, g := range remoteCLIGlobs {
		if rest, ok := strings.CutPrefix(g, "~/"); ok {
			if e.Home == "" {
				continue
			}
			g = filepath.Join(e.Home, rest)
		}
		all = append(all, e.glob(g)...)
	}
	return e.newest(all)
}

// RemoteCLI finds a remote CLI an editor server installed on disk (VS Code's
// server or code-server), "" when there is none. home is $HOME.
func RemoteCLI(home string) string { return Env{Home: home}.findRemoteCLI() }

// remoteOnPath is the remote CLI the integrated terminal put on PATH: `code`
// in VS Code's, `code-server` in code-server's. "" when PATH names neither.
func (e Env) remoteOnPath() string {
	for _, name := range []string{"code", "code-server"} {
		if p := e.look(name); isRemoteCLI(p) {
			return p
		}
	}
	return ""
}

// liveIPC returns the socket the remote CLI must use. The inherited
// VSCODE_IPC_HOOK_CLI is kept while it exists; once VS Code reconnected it
// points at a dead socket, and the newest vscode-ipc-*.sock is the live one.
// "" means: keep the environment as it is.
func (e Env) liveIPC() string {
	if e.VSCodeIPC == "" {
		return ""
	}
	if _, ok := e.modTime(e.VSCodeIPC); ok {
		return ""
	}
	var all []string
	for _, dir := range []string{e.RuntimeDir, os.TempDir()} {
		if dir == "" {
			continue
		}
		for _, g := range ipcSocketGlobs {
			all = append(all, e.glob(filepath.Join(dir, g))...)
		}
	}
	return e.newest(all)
}

// codeEditor resolves the `code` launcher for this environment.
//
// Inside a VS Code terminal it is the window's own CLI: on a remote machine
// (a Coder CDE, SSH, WSL) the remote CLI, which opens files and folders in the
// connected window. In code-server (VS Code in the browser) that CLI is called
// `code-server` and opens them in the browser tab. PATH is trusted when it
// already names a remote CLI; when `code` there is something else, or missing,
// the server's remote CLI found on disk is used. A dead inherited IPC socket is
// replaced by the newest live one.
//
// Outside VS Code a `code` is only useful with a display: on a headless
// session (web tty, plain ssh) no window can open, so there is no editor.
func (e Env) codeEditor() (Editor, bool) {
	if e.InVSCode() {
		p := e.remoteOnPath()
		if p == "" && e.VSCodeIPC != "" {
			p = e.findRemoteCLI()
		}
		if p == "" {
			p = e.look("code")
		}
		if p == "" {
			return Editor{}, false
		}
		return Editor{Kind: Code, Cmd: p, Name: filepath.Base(p), Remote: isRemoteCLI(p), IPC: e.liveIPC()}, true
	}
	if e.Headless {
		return Editor{}, false
	}
	if p := e.look("code"); p != "" {
		return Editor{Kind: Code, Cmd: p, Name: "code"}, true
	}
	return Editor{}, false
}

// NoCodeHint says how to open target in VS Code by hand when ADE cannot do it
// itself: there is no `code` launcher, or no VS Code window is connected to
// this terminal (a web tty).
func (e Env) NoCodeHint(target string) string {
	switch {
	case !e.InVSCode() && e.Headless:
		return "no VS Code window is connected to this terminal (a web tty has none): " +
			"open " + target + " from your VS Code window (File > Open Workspace from File…), " +
			"or run `code " + target + "` in a VS Code terminal on this machine"
	case e.InVSCode():
		return "the VS Code remote CLI was not found: open " + target +
			" from the VS Code window (File > Open…), or open a new VS Code terminal so it puts `code` " +
			"(`code-server` in code-server) on PATH"
	}
	return "code is not on PATH: open " + target + " in VS Code"
}
