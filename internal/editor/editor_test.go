package editor

import (
	"errors"
	"reflect"
	"slices"
	"testing"
)

// path returns a LookPath that finds only the named programs under /bin.
func path(have ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		if slices.Contains(have, name) {
			return "/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func TestResolveMatrix(t *testing.T) {
	vs := Env{TermProgram: "vscode"}
	tests := []struct {
		name    string
		setting string
		env     Env
		have    []string
		want    string // Name
		kind    Kind
		cmd     string
	}{
		{"auto in VS Code terminal uses code", "auto", Env{TermProgram: "vscode"}, []string{"code", "nvim"}, "code", Code, "/bin/code"},
		{"auto detects VS Code by the IPC socket (zellij pane inside it)", "", Env{VSCodeIPC: "/run/vscode.sock"}, []string{"code", "nvim"}, "code", Code, "/bin/code"},
		{"auto in VS Code without code on PATH falls to nvim", "auto", vs, []string{"nvim"}, "nvim", Nvim, "/bin/nvim"},
		{"auto outside VS Code prefers nvim over $EDITOR", "auto", Env{Editor: "emacs"}, []string{"code", "nvim"}, "nvim", Nvim, "/bin/nvim"},
		{"auto without nvim uses $VISUAL first", "auto", Env{Visual: "hx", Editor: "emacs"}, nil, "$EDITOR", Shell, "hx"},
		{"auto without nvim uses $EDITOR", "auto", Env{Editor: "emacs -nw"}, nil, "$EDITOR", Shell, "emacs -nw"},
		{"auto with nothing falls back to vi", "auto", Env{}, nil, "vi", Shell, "vi"},
		{"code is honoured outside VS Code", "code", Env{}, []string{"code", "nvim"}, "code", Code, "/bin/code"},
		{"code missing falls back to nvim", "code", Env{}, []string{"nvim"}, "nvim", Nvim, "/bin/nvim"},
		{"nvim inside VS Code stays nvim", "nvim", vs, []string{"code", "nvim"}, "nvim", Nvim, "/bin/nvim"},
		{"ade-nvim wrapper", "ade-nvim", Env{}, []string{"ade-nvim", "nvim"}, "ade-nvim", Nvim, "/bin/ade-nvim"},
		{"ade-nvim without wrapper runs nvim under its app name", "ade-nvim", Env{}, []string{"nvim"}, "ade-nvim", Nvim, "/bin/nvim"},
		{"$EDITOR setting", "$EDITOR", Env{Editor: "nano"}, []string{"nvim"}, "$EDITOR", Shell, "nano"},
		{"$EDITOR setting unset falls back to nvim", "$EDITOR", Env{}, []string{"nvim"}, "nvim", Nvim, "/bin/nvim"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.env.LookPath = path(tt.have...)
			got := Resolve(tt.setting, tt.env)
			if got.Name != tt.want || got.Kind != tt.kind || got.Cmd != tt.cmd {
				t.Fatalf("Resolve(%q) = %+v, want %s/%s/%s", tt.setting, got, tt.want, tt.kind, tt.cmd)
			}
		})
	}
	nv := Resolve("ade-nvim", Env{LookPath: path("nvim")})
	if nv.AppName != "ade-nvim" {
		t.Fatalf("nvim fallback of ade-nvim must run under NVIM_APPNAME, got %+v", nv)
	}
}

func TestArgv(t *testing.T) {
	code := Editor{Kind: Code, Cmd: "/bin/code"}
	nvim := Editor{Kind: Nvim, Cmd: "/bin/nvim"}
	sh := Editor{Kind: Shell, Cmd: "emacs -nw"}
	tests := []struct {
		name string
		ed   Editor
		line int
		wait bool
		want []string
	}{
		{"code file:line", code, 12, false, []string{"/bin/code", "-g", "/w/a.go:12"}},
		{"code file", code, 0, false, []string{"/bin/code", "/w/a.go"}},
		{"code waits for the window to close", code, 0, true, []string{"/bin/code", "--wait", "/w/a.go"}},
		{"code wait + line", code, 3, true, []string{"/bin/code", "--wait", "-g", "/w/a.go:3"}},
		{"nvim +line", nvim, 12, true, []string{"/bin/nvim", "+12", "/w/a.go"}},
		{"nvim file", nvim, 0, false, []string{"/bin/nvim", "/w/a.go"}},
		{"shell editor takes the path as an argument, keeping its own flags", sh, 12, false, []string{"sh", "-c", `emacs -nw "$1"`, "sh", "/w/a.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ed.Argv("/w/a.go", tt.line, tt.wait); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Argv = %q, want %q", got, tt.want)
			}
		})
	}
	if got := code.ArgvDir("/w"); !reflect.DeepEqual(got, []string{"/bin/code", "/w"}) {
		t.Fatalf("ArgvDir(code) = %q", got)
	}
	if got := nvim.ArgvDir("/w"); !reflect.DeepEqual(got, []string{"/bin/nvim", "/w"}) {
		t.Fatalf("ArgvDir(nvim) = %q", got)
	}
}

func TestCommandSetsNvimAppName(t *testing.T) {
	cmd := Editor{Kind: Nvim, Cmd: "/bin/nvim", AppName: "ade-nvim"}.Command("/w/a.go", 0, false)
	if !slices.Contains(cmd.Env, "NVIM_APPNAME=ade-nvim") {
		t.Fatalf("env lacks NVIM_APPNAME: %v", cmd.Env)
	}
	if cmd := (Editor{Kind: Nvim, Cmd: "/bin/nvim"}).Command("/w/a.go", 0, false); cmd.Env != nil {
		t.Fatalf("plain nvim must inherit the environment untouched, got %v", cmd.Env)
	}
}

func TestPaneCommand(t *testing.T) {
	prog, args := Editor{Kind: Code, Cmd: "/bin/code"}.PaneCommand("/w")
	if prog != "sh" || args[len(args)-2] != "/bin/code" || args[len(args)-1] != "/w" {
		t.Fatalf("code pane = %s %q: it must open the window with the directory and keep a shell", prog, args)
	}
	if prog, args := (Editor{Kind: Nvim, Cmd: "/bin/nvim"}).PaneCommand("/w"); prog != "/bin/nvim" || args != nil {
		t.Fatalf("nvim pane = %s %q", prog, args)
	}
	prog, args = Editor{Kind: Nvim, Cmd: "/bin/nvim", AppName: "ade-nvim"}.PaneCommand("/w")
	if prog != "env" || !slices.Contains(args, "NVIM_APPNAME=ade-nvim") {
		t.Fatalf("ade-nvim pane = %s %q", prog, args)
	}
}
