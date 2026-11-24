package editor

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeFS is a tiny file system for the remote CLI and socket lookups: path ->
// modification time. Glob matches with a single "*" per pattern segment set.
type fakeFS map[string]time.Time

func (f fakeFS) glob(pattern string) ([]string, error) {
	var out []string
	for p := range f {
		if matchStar(pattern, p) {
			out = append(out, p)
		}
	}
	return out, nil
}

// matchStar matches pattern against p where each "*" stands for one path
// segment's worth of characters (no slash).
func matchStar(pattern, p string) bool {
	ps, ss := strings.Split(pattern, "/"), strings.Split(p, "/")
	if len(ps) != len(ss) {
		return false
	}
	for i := range ps {
		if ps[i] == "*" {
			continue
		}
		if strings.Contains(ps[i], "*") {
			pre, post, _ := strings.Cut(ps[i], "*")
			if !strings.HasPrefix(ss[i], pre) || !strings.HasSuffix(ss[i], post) {
				return false
			}
			continue
		}
		if ps[i] != ss[i] {
			return false
		}
	}
	return true
}

func (f fakeFS) modTime(p string) (time.Time, bool) { at, ok := f[p]; return at, ok }

const remoteCLI = "/home/dev/.vscode-server/cli/servers/Stable-abc/server/bin/remote-cli/code"

// codeServerCLI is the remote CLI code-server puts on its terminal's PATH.
const codeServerCLI = "/tmp/code-server/lib/vscode/bin/remote-cli/code-server"

// lookup finds only the given absolute program paths by base name.
func lookup(progs map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if p, ok := progs[name]; ok {
			return p, nil
		}
		return "", os.ErrNotExist
	}
}

// The editor matrix of a headless Coder CDE reached through VS Code.
func TestResolveRemoteCLI(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		setting string
		env     Env
		fs      fakeFS
		progs   map[string]string
		want    Editor
		open    []string // Unix sockets that accept connections
	}{
		{
			name: "auto in the remote terminal uses the remote CLI from PATH", setting: "auto",
			env:   Env{TermProgram: "vscode", VSCodeIPC: "/run/user/1000/vscode-ipc-live.sock", Headless: true, Home: "/home/dev"},
			fs:    fakeFS{"/run/user/1000/vscode-ipc-live.sock": now},
			progs: map[string]string{"code": remoteCLI, "nvim": "/bin/nvim"},
			want:  Editor{Kind: Code, Cmd: remoteCLI, Name: "code", Remote: true},
		},
		{
			name: "PATH code is a desktop launcher: the server's remote CLI wins", setting: "auto",
			env:   Env{VSCodeIPC: "/run/vscode.sock", Headless: true, Home: "/home/dev"},
			fs:    fakeFS{remoteCLI: now, "/run/vscode.sock": now},
			progs: map[string]string{"code": "/usr/bin/code", "nvim": "/bin/nvim"},
			want:  Editor{Kind: Code, Cmd: remoteCLI, Name: "code", Remote: true},
		},
		{
			name: "no code on PATH (zellij pane from an older shell): found under the server dir", setting: "auto",
			env: Env{VSCodeIPC: "/run/vscode.sock", Headless: true, Home: "/home/dev"},
			fs: fakeFS{
				"/home/dev/.vscode-server/cli/servers/Stable-old/server/bin/remote-cli/code": now.Add(-48 * time.Hour),
				remoteCLI:          now,
				"/run/vscode.sock": now,
			},
			progs: map[string]string{"nvim": "/bin/nvim"},
			want:  Editor{Kind: Code, Cmd: remoteCLI, Name: "code", Remote: true},
		},
		{
			name: "older server layout", setting: "code",
			env:   Env{VSCodeIPC: "/run/vscode.sock", Home: "/home/dev"},
			fs:    fakeFS{"/home/dev/.vscode-server/bin/abc123/bin/remote-cli/code": now, "/run/vscode.sock": now},
			progs: map[string]string{},
			want:  Editor{Kind: Code, Cmd: "/home/dev/.vscode-server/bin/abc123/bin/remote-cli/code", Name: "code", Remote: true},
		},
		{
			name: "stale IPC socket is replaced by the newest live one", setting: "auto",
			env: Env{TermProgram: "vscode", VSCodeIPC: "/run/user/1000/vscode-ipc-dead.sock", RuntimeDir: "/run/user/1000", Headless: true, Home: "/home/dev"},
			fs: fakeFS{
				"/run/user/1000/vscode-ipc-old.sock":  now.Add(-time.Hour),
				"/run/user/1000/vscode-ipc-live.sock": now,
			},
			progs: map[string]string{"code": remoteCLI},
			want:  Editor{Kind: Code, Cmd: remoteCLI, Name: "code", Remote: true, IPC: "/run/user/1000/vscode-ipc-live.sock"},
		},
		{
			name: "stale socket and none live: keep the environment", setting: "auto",
			env:   Env{TermProgram: "vscode", VSCodeIPC: "/run/dead.sock", RuntimeDir: "/run/user/1000", Home: "/home/dev"},
			fs:    fakeFS{},
			progs: map[string]string{"code": remoteCLI},
			want:  Editor{Kind: Code, Cmd: remoteCLI, Name: "code", Remote: true},
		},
		{
			name: "desktop VS Code terminal: its own code, not remote", setting: "auto",
			env:   Env{TermProgram: "vscode"},
			fs:    fakeFS{},
			progs: map[string]string{"code": "/usr/bin/code", "nvim": "/bin/nvim"},
			want:  Editor{Kind: Code, Cmd: "/usr/bin/code", Name: "code"},
		},
		{
			name: "web tty has no client: code is not an editor even when installed", setting: "code",
			env:   Env{Headless: true, Home: "/home/dev"},
			fs:    fakeFS{remoteCLI: now},
			progs: map[string]string{"code": "/usr/bin/code", "nvim": "/bin/nvim"},
			want:  Editor{Kind: Term, Cmd: "/bin/nvim", Name: "nvim"},
		},
		{
			name: "auto in a web tty is nvim", setting: "auto",
			env:   Env{Headless: true},
			fs:    fakeFS{},
			progs: map[string]string{"code": "/usr/bin/code", "nvim": "/bin/nvim"},
			want:  Editor{Kind: Term, Cmd: "/bin/nvim", Name: "nvim"},
		},
		{
			name: "a desktop session outside VS Code keeps honouring code", setting: "code",
			env:   Env{},
			fs:    fakeFS{},
			progs: map[string]string{"code": "/usr/bin/code"},
			want:  Editor{Kind: Code, Cmd: "/usr/bin/code", Name: "code"},
		},
		{
			name: "code-server terminal: its `code-server` remote CLI on PATH, no `code`", setting: "auto",
			env:   Env{TermProgram: "vscode", VSCodeIPC: "/tmp/vscode-ipc-cs.sock", Headless: true, Home: "/home/dev"},
			fs:    fakeFS{"/tmp/vscode-ipc-cs.sock": now},
			progs: map[string]string{"code-server": codeServerCLI, "nvim": "/bin/nvim"},
			want:  Editor{Kind: Code, Cmd: codeServerCLI, Name: "code-server", Remote: true},
		},
		{
			name: "code-server's CLI beats a stale VS Code server left under $HOME", setting: "code",
			env:   Env{TermProgram: "vscode", VSCodeIPC: "/tmp/vscode-ipc-cs.sock", Headless: true, Home: "/home/dev"},
			fs:    fakeFS{remoteCLI: now.Add(-72 * time.Hour), "/tmp/vscode-ipc-cs.sock": now},
			progs: map[string]string{"code-server": codeServerCLI},
			want:  Editor{Kind: Code, Cmd: codeServerCLI, Name: "code-server", Remote: true},
		},
		{
			name: "the `code-server` on PATH is the server itself: its install's remote CLI is used", setting: "auto",
			env: Env{TermProgram: "vscode", VSCodeIPC: "/tmp/vscode-ipc-cs.sock", Headless: true, Home: "/home/dev"},
			fs: fakeFS{
				"/tmp/code-server/lib/vscode/bin/remote-cli/code-server": now,
				"/tmp/vscode-ipc-cs.sock":                                now,
			},
			progs: map[string]string{"code-server": "/usr/bin/code-server", "nvim": "/bin/nvim"},
			want:  Editor{Kind: Code, Cmd: "/tmp/code-server/lib/vscode/bin/remote-cli/code-server", Name: "code-server", Remote: true},
		},
		{
			name: "zellij pane from a code-server terminal without PATH: install.sh layout under $HOME", setting: "auto",
			env: Env{VSCodeIPC: "/tmp/vscode-ipc-cs.sock", Headless: true, Home: "/home/dev"},
			fs: fakeFS{
				"/home/dev/.local/lib/code-server-4.96.4/lib/vscode/bin/remote-cli/code-server": now,
				"/tmp/vscode-ipc-cs.sock": now,
			},
			progs: map[string]string{"nvim": "/bin/nvim"},
			want:  Editor{Kind: Code, Cmd: "/home/dev/.local/lib/code-server-4.96.4/lib/vscode/bin/remote-cli/code-server", Name: "code-server", Remote: true},
		},
		{
			name: "desktop VS Code terminal with code-server installed: desktop code, not code-server", setting: "auto",
			env:   Env{TermProgram: "vscode", Home: "/home/dev"},
			fs:    fakeFS{codeServerCLI: now},
			progs: map[string]string{"code": "/usr/bin/code", "code-server": "/usr/bin/code-server"},
			want:  Editor{Kind: Code, Cmd: "/usr/bin/code", Name: "code"},
		},
		{
			name: "web tty with code-server running: its launcher through the session socket", setting: "code",
			env:   Env{Headless: true, Home: "/home/dev"},
			fs:    fakeFS{},
			progs: map[string]string{"code-server": "/usr/bin/code-server", "nvim": "/bin/nvim"},
			open:  []string{"/home/dev/.local/share/code-server/code-server-ipc.sock"},
			want:  Editor{Kind: Code, Cmd: "/usr/bin/code-server", Name: "code-server", Session: "/home/dev/.local/share/code-server/code-server-ipc.sock"},
		},
		{
			name: "web tty, code-server installed but its session socket is closed: no code", setting: "code",
			env:   Env{Headless: true, Home: "/home/dev"},
			fs:    fakeFS{},
			progs: map[string]string{"code-server": "/usr/bin/code-server", "nvim": "/bin/nvim"},
			want:  Editor{Kind: Term, Cmd: "/bin/nvim", Name: "nvim"},
		},
		{
			name: "web tty, socket open but no code-server launcher: no code", setting: "code",
			env:   Env{Headless: true, Home: "/home/dev"},
			fs:    fakeFS{codeServerCLI: now},
			progs: map[string]string{"code-server": codeServerCLI, "nvim": "/bin/nvim"},
			open:  []string{"/home/dev/.local/share/code-server/code-server-ipc.sock"},
			want:  Editor{Kind: Term, Cmd: "/bin/nvim", Name: "nvim"},
		},
		{
			name: "Coder module's code-server off PATH, socket under XDG_DATA_HOME", setting: "code",
			env:   Env{Headless: true, Home: "/home/dev", DataHome: "/data"},
			fs:    fakeFS{"/tmp/code-server/bin/code-server": now},
			progs: map[string]string{},
			open:  []string{"/data/code-server/code-server-ipc.sock"},
			want:  Editor{Kind: Code, Cmd: "/tmp/code-server/bin/code-server", Name: "code-server", Session: "/data/code-server/code-server-ipc.sock"},
		},
		{
			name: "CODE_SERVER_SESSION_SOCKET names the socket", setting: "code",
			env:   Env{Headless: true, Home: "/home/dev", CodeServerSession: "/run/cs.sock"},
			fs:    fakeFS{},
			progs: map[string]string{"code-server": "/usr/bin/code-server"},
			open:  []string{"/run/cs.sock", "/home/dev/.local/share/code-server/code-server-ipc.sock"},
			want:  Editor{Kind: Code, Cmd: "/usr/bin/code-server", Name: "code-server", Session: "/run/cs.sock"},
		},
		{
			name: "desktop session: desktop code before a running code-server", setting: "code",
			env:   Env{Home: "/home/dev"},
			fs:    fakeFS{},
			progs: map[string]string{"code": "/usr/bin/code", "code-server": "/usr/bin/code-server"},
			open:  []string{"/home/dev/.local/share/code-server/code-server-ipc.sock"},
			want:  Editor{Kind: Code, Cmd: "/usr/bin/code", Name: "code"},
		},
		{
			name: "auto in a web tty stays nvim with code-server running", setting: "auto",
			env:   Env{Headless: true, Home: "/home/dev"},
			fs:    fakeFS{},
			progs: map[string]string{"code-server": "/usr/bin/code-server", "nvim": "/bin/nvim"},
			open:  []string{"/home/dev/.local/share/code-server/code-server-ipc.sock"},
			want:  Editor{Kind: Term, Cmd: "/bin/nvim", Name: "nvim"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.env.LookPath = lookup(tt.progs)
			tt.env.Glob, tt.env.ModTime = tt.fs.glob, tt.fs.modTime
			tt.env.SocketOpen = func(p string) bool { return slices.Contains(tt.open, p) }
			got := Resolve(tt.setting, tt.env)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Resolve(%q) = %+v\nwant %+v", tt.setting, got, tt.want)
			}
		})
	}
}

func TestEditorEnviron(t *testing.T) {
	plain := Editor{Kind: Code, Cmd: remoteCLI, Remote: true}
	if plain.Environ() != nil {
		t.Error("an editor needing nothing must inherit the environment")
	}
	fixed := Editor{Kind: Code, Cmd: remoteCLI, Remote: true, IPC: "/run/live.sock"}
	env := fixed.Environ()
	if env[len(env)-1] != "VSCODE_IPC_HOOK_CLI=/run/live.sock" {
		t.Errorf("Environ must end with the repaired socket, got %v", env[len(env)-1])
	}
	if got := fixed.String(); !strings.Contains(got, "remote CLI") {
		t.Errorf("String = %q: doctor should say it is the remote CLI", got)
	}
	cmd := fixed.Command("/w/a.go", 7, false)
	if !reflect.DeepEqual(cmd.Args, []string{remoteCLI, "-g", "/w/a.go:7"}) {
		t.Errorf("Command args = %v", cmd.Args)
	}
}

func TestNoCodeHint(t *testing.T) {
	web := Env{Headless: true}.NoCodeHint("/w/x.code-workspace")
	if !strings.Contains(web, "/w/x.code-workspace") || !strings.Contains(web, "web tty") || !strings.Contains(web, "VS Code terminal") {
		t.Errorf("web tty hint = %q", web)
	}
	if h := (Env{VSCodeIPC: "/s"}).NoCodeHint("/w"); !strings.Contains(h, "remote CLI") {
		t.Errorf("in VS Code without code: %q", h)
	}
	if h := (Env{}).NoCodeHint("/w"); !strings.Contains(h, "not on PATH") {
		t.Errorf("desktop without code: %q", h)
	}
}

// code-server's launcher opens through the session socket in the existing
// browser window: never a bare `code-server <path>`, which starts a second
// server when no window takes it.
func TestCodeServerSessionArgv(t *testing.T) {
	const sock = "/home/dev/.local/share/code-server/code-server-ipc.sock"
	cs := Editor{Kind: Code, Cmd: "/usr/bin/code-server", Name: "code-server", Session: sock}
	base := []string{"/usr/bin/code-server", "--session-socket", sock, "--reuse-window"}
	if got := cs.ArgvDir("/w/x.code-workspace"); !reflect.DeepEqual(got, append(slices.Clone(base), "/w/x.code-workspace")) {
		t.Errorf("ArgvDir = %q", got)
	}
	if got := cs.ArgvWindow("/w"); !reflect.DeepEqual(got, append(slices.Clone(base), "/w")) {
		t.Errorf("ArgvWindow = %q: a server-opened browser window is a blocked pop-up, reuse the window", got)
	}
	if got := cs.Argv("/w/a.go", 7, true); !reflect.DeepEqual(got, append(slices.Clone(base), "--wait", "/w/a.go:7")) {
		t.Errorf("Argv = %q: code-server's launcher has no -g", got)
	}
	if got := (Editor{Kind: Code, Cmd: "/bin/code"}).ArgvWindow("/w"); !reflect.DeepEqual(got, []string{"/bin/code", "--new-window", "/w"}) {
		t.Errorf("VS Code ArgvWindow = %q", got)
	}
}
