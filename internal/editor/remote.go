package editor

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// remoteCLIGlobs locate the VS Code server's remote CLI under $HOME, newest
// server layout first. The integrated terminal puts one of them on PATH, but
// a shell that outlives the window (a zellij session, tmux) or a process
// started by another tool may not have it.
var remoteCLIGlobs = []string{
	".vscode-server/cli/servers/*/server/bin/remote-cli/code",
	".vscode-server/bin/*/bin/remote-cli/code",
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

// findRemoteCLI looks for the remote CLI the server installed under $HOME.
func (e Env) findRemoteCLI() string {
	if e.Home == "" {
		return ""
	}
	var all []string
	for _, g := range remoteCLIGlobs {
		all = append(all, e.glob(filepath.Join(e.Home, g))...)
	}
	return e.newest(all)
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
// connected window. PATH is trusted when it already names the remote CLI;
// when `code` there is something else, or missing, the server's remote CLI
// under $HOME is used. A dead inherited IPC socket is replaced by the newest
// live one.
//
// Outside VS Code a `code` is only useful with a display: on a headless
// session (web tty, plain ssh) no window can open, so there is no editor.
func (e Env) codeEditor() (Editor, bool) {
	if e.InVSCode() {
		p := e.look("code")
		if e.VSCodeIPC != "" && !isRemoteCLI(p) {
			if r := e.findRemoteCLI(); r != "" {
				p = r
			}
		}
		if p == "" {
			return Editor{}, false
		}
		return Editor{Kind: Code, Cmd: p, Name: "code", Remote: isRemoteCLI(p), IPC: e.liveIPC()}, true
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
			" from the VS Code window (File > Open…), or open a new VS Code terminal so it puts `code` on PATH"
	}
	return "code is not on PATH: open " + target + " in VS Code"
}
