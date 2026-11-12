package editor

import (
	"os"
	"reflect"
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
			want:  Editor{Kind: Nvim, Cmd: "/bin/nvim", Name: "nvim"},
		},
		{
			name: "auto in a web tty is nvim", setting: "auto",
			env:   Env{Headless: true},
			fs:    fakeFS{},
			progs: map[string]string{"code": "/usr/bin/code", "nvim": "/bin/nvim"},
			want:  Editor{Kind: Nvim, Cmd: "/bin/nvim", Name: "nvim"},
		},
		{
			name: "a desktop session outside VS Code keeps honouring code", setting: "code",
			env:   Env{},
			fs:    fakeFS{},
			progs: map[string]string{"code": "/usr/bin/code"},
			want:  Editor{Kind: Code, Cmd: "/usr/bin/code", Name: "code"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.env.LookPath = lookup(tt.progs)
			tt.env.Glob, tt.env.ModTime = tt.fs.glob, tt.fs.modTime
			got := Resolve(tt.setting, tt.env)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Resolve(%q) = %+v\nwant %+v", tt.setting, got, tt.want)
			}
		})
	}
}

func TestEditorEnvironAndPane(t *testing.T) {
	plain := Editor{Kind: Code, Cmd: remoteCLI, Remote: true}
	if plain.Environ() != nil {
		t.Error("an editor needing nothing must inherit the environment")
	}
	fixed := Editor{Kind: Code, Cmd: remoteCLI, Remote: true, IPC: "/run/live.sock"}
	env := fixed.Environ()
	if env[len(env)-1] != "VSCODE_IPC_HOOK_CLI=/run/live.sock" {
		t.Errorf("Environ must end with the repaired socket, got %v", env[len(env)-1])
	}
	prog, args := fixed.PaneCommand("/w")
	if prog != "sh" || !strings.Contains(args[1], "VSCODE_IPC_HOOK_CLI") || args[len(args)-1] != "/run/live.sock" {
		t.Errorf("pane = %s %q: the pane's shell must export the live socket", prog, args)
	}
	if _, args := plain.PaneCommand("/w"); strings.Contains(strings.Join(args, " "), "VSCODE_IPC") {
		t.Errorf("no socket repair expected: %q", args)
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
