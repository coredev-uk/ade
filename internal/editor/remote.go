package editor

import (
	"net"
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
// Outside VS Code, a running code-server whose session socket accepts
// connections opens it in its browser window (see codeServerEditor). Failing
// that, a `code` is only useful with a display: on a headless session (web
// tty, plain ssh) no window can open, so there is no editor. With a display
// the desktop `code` comes first.
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
	if !e.Headless {
		if p := e.look("code"); p != "" {
			return Editor{Kind: Code, Cmd: p, Name: "code"}, true
		}
	}
	return e.codeServerEditor()
}

// codeServerLauncherGlobs locate code-server's own launcher (not the remote
// CLI its terminals get): the Coder module's /tmp/code-server, install.sh's
// standalone release under $HOME and the system package.
var codeServerLauncherGlobs = []string{
	"/tmp/code-server/bin/code-server",
	"~/.local/lib/code-server-*/bin/code-server",
	"/usr/lib/code-server/bin/code-server",
}

// codeServerLauncher finds code-server's launcher: from PATH unless that is
// the remote CLI, else in an install location. "" when there is none.
func (e Env) codeServerLauncher() string {
	if p := e.look("code-server"); p != "" && !isRemoteCLI(p) {
		return p
	}
	var all []string
	for _, g := range codeServerLauncherGlobs {
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

// CodeServerSocket is the session socket a running code-server listens on:
// $CODE_SERVER_SESSION_SOCKET (code-server sets it for its children), else
// code-server-ipc.sock in its default user data directory
// ($XDG_DATA_HOME/code-server, ~/.local/share/code-server). "" when neither
// can be named.
func (e Env) CodeServerSocket() string {
	if e.CodeServerSession != "" {
		return e.CodeServerSession
	}
	data := e.DataHome
	if data == "" {
		if e.Home == "" {
			return ""
		}
		data = filepath.Join(e.Home, ".local", "share")
	}
	return filepath.Join(data, "code-server", "code-server-ipc.sock")
}

func (e Env) socketOpen(path string) bool {
	if e.SocketOpen != nil {
		return e.SocketOpen(path)
	}
	c, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// codeServerEditor is code-server's launcher talking to the running
// code-server through its session socket: `code-server --session-socket S
// --reuse-window <path>` opens path in the code-server browser window, which
// is how a CDE without a VS Code client (a web tty) reaches the editor. It
// needs the launcher and a session socket that accepts connections; the
// launcher itself picks the browser window, and fails when none is open.
func (e Env) codeServerEditor() (Editor, bool) {
	p := e.codeServerLauncher()
	if p == "" {
		return Editor{}, false
	}
	sock := e.CodeServerSocket()
	if sock == "" || !e.socketOpen(sock) {
		return Editor{}, false
	}
	return Editor{Kind: Code, Cmd: p, Name: "code-server", Session: sock}, true
}

// NoCodeHint says how to open target in VS Code by hand when ADE cannot do it
// itself: there is no `code` launcher, or no VS Code window is connected to
// this terminal (a web tty).
func (e Env) NoCodeHint(target string) string {
	switch {
	case !e.InVSCode() && e.Headless:
		return "no VS Code window is connected to this terminal (a web tty has none) and no running code-server was found: " +
			"open " + target + " from your VS Code window (File > Open Workspace from File…), " +
			"or run `code " + target + "` in a VS Code terminal on this machine; " +
			"with code-server running (its session socket " + e.CodeServerSocket() + " open), ADE opens it in code-server's browser window"
	case e.InVSCode():
		return "the VS Code remote CLI was not found: open " + target +
			" from the VS Code window (File > Open…), or open a new VS Code terminal so it puts `code` " +
			"(`code-server` in code-server) on PATH"
	}
	return "code is not on PATH: open " + target + " in VS Code"
}
