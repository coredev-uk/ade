// Package editor resolves which editor ADE opens files in (PLAN 6.7) and builds
// the command for it. One resolution serves every place ADE spawns an editor:
// the workspace's editor pane and the VS Code window, the MR body in the start
// dialog, the agent plan, and `ade item --edit`.
package editor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Kind is the family an editor belongs to; it decides how a line and a
// directory are passed and whether the command blocks until the file is closed.
type Kind string

const (
	// Code is VS Code's `code` launcher: `code -g file:line`, `code <dir>`.
	Code Kind = "code"
	// Nvim is neovim or ade-nvim: `nvim +line file`.
	Nvim Kind = "nvim"
	// Shell is whatever $VISUAL / $EDITOR names, run through sh so that values
	// with arguments ("emacs -nw") work. No line is passed.
	Shell Kind = "env"
)

// Setting values of host.editor.
const (
	SettingAuto    = "auto"
	SettingNvim    = "nvim"
	SettingADENvim = "ade-nvim"
	SettingCode    = "code"
	SettingEnv     = "$EDITOR"
)

// Env is the part of the process environment resolution looks at; tests build
// it by hand.
type Env struct {
	TermProgram string // TERM_PROGRAM
	VSCodeIPC   string // VSCODE_IPC_HOOK_CLI: set in terminals VS Code started
	Visual      string // VISUAL
	Editor      string // EDITOR
	// LookPath finds a program on PATH; exec.LookPath when nil.
	LookPath func(string) (string, error)
}

// FromOS reads the real environment.
func FromOS() Env {
	return Env{
		TermProgram: os.Getenv("TERM_PROGRAM"),
		VSCodeIPC:   os.Getenv("VSCODE_IPC_HOOK_CLI"),
		Visual:      os.Getenv("VISUAL"),
		Editor:      os.Getenv("EDITOR"),
	}
}

// InVSCode reports whether the process runs inside a VS Code terminal, also
// from a zellij pane started there (zellij passes the environment on).
func (e Env) InVSCode() bool { return e.TermProgram == "vscode" || e.VSCodeIPC != "" }

func (e Env) look(name string) string {
	f := e.LookPath
	if f == nil {
		f = exec.LookPath
	}
	p, err := f(name)
	if err != nil {
		return ""
	}
	return p
}

func (e Env) fromEnv() string {
	if e.Visual != "" {
		return e.Visual
	}
	return e.Editor
}

// Editor is a resolved editor. The zero value is not usable; use Resolve.
type Editor struct {
	Kind Kind
	// Cmd is the program path (Code, Nvim) or the shell command text (Shell).
	Cmd string
	// Name is how the resolution reads in messages: code, ade-nvim, nvim, $EDITOR, vi.
	Name string
	// AppName is NVIM_APPNAME to run neovim with (ade-nvim started as plain
	// nvim when the ade-nvim wrapper is not on PATH); empty for everything else.
	AppName string
}

// Resolve applies host.editor (setting) to the environment:
//
//   - "code": the `code` launcher
//   - "ade-nvim": the ade-nvim wrapper, else nvim under NVIM_APPNAME=ade-nvim
//   - "nvim": nvim
//   - "$EDITOR": $VISUAL, else $EDITOR
//   - "auto" (or empty): `code` inside a VS Code terminal, else nvim, else
//     $VISUAL / $EDITOR, else vi
//
// An explicit choice whose program is missing falls back the way "auto" does
// outside VS Code, so a stale setting never leaves ADE without an editor.
func Resolve(setting string, env Env) Editor {
	switch setting {
	case SettingCode:
		if p := env.look("code"); p != "" {
			return Editor{Kind: Code, Cmd: p, Name: "code"}
		}
	case SettingADENvim:
		if p := env.look("ade-nvim"); p != "" {
			return Editor{Kind: Nvim, Cmd: p, Name: "ade-nvim"}
		}
		if p := env.look("nvim"); p != "" {
			return Editor{Kind: Nvim, Cmd: p, Name: "ade-nvim", AppName: "ade-nvim"}
		}
	case SettingNvim:
		if p := env.look("nvim"); p != "" {
			return Editor{Kind: Nvim, Cmd: p, Name: "nvim"}
		}
	case SettingEnv:
		if c := env.fromEnv(); c != "" {
			return Editor{Kind: Shell, Cmd: c, Name: "$EDITOR"}
		}
	default: // auto, or a value validation would have rejected
		if env.InVSCode() {
			if p := env.look("code"); p != "" {
				return Editor{Kind: Code, Cmd: p, Name: "code"}
			}
		}
	}
	if p := env.look("nvim"); p != "" {
		return Editor{Kind: Nvim, Cmd: p, Name: "nvim"}
	}
	if c := env.fromEnv(); c != "" {
		return Editor{Kind: Shell, Cmd: c, Name: "$EDITOR"}
	}
	return Editor{Kind: Shell, Cmd: "vi", Name: "vi"}
}

// Argv is the command line that opens path, at line when line > 0 and the
// editor can. With wait the command returns only once the file is closed
// (needed when ADE reads the file back); terminal editors always do.
func (e Editor) Argv(path string, line int, wait bool) []string {
	switch e.Kind {
	case Code:
		argv := []string{e.Cmd}
		if wait {
			argv = append(argv, "--wait")
		}
		if line > 0 {
			return append(argv, "-g", path+":"+strconv.Itoa(line))
		}
		return append(argv, path)
	case Nvim:
		if line > 0 {
			return []string{e.Cmd, "+" + strconv.Itoa(line), path}
		}
		return []string{e.Cmd, path}
	}
	return []string{"sh", "-c", e.Cmd + ` "$1"`, "sh", path}
}

// ArgvDir is the command line that opens directory dir as a workspace.
func (e Editor) ArgvDir(dir string) []string {
	if e.Kind == Code {
		return []string{e.Cmd, dir}
	}
	return e.Argv(dir, 0, false)
}

// Command returns the exec.Cmd for Argv, with NVIM_APPNAME set when needed.
func (e Editor) Command(path string, line int, wait bool) *exec.Cmd {
	argv := e.Argv(path, line, wait)
	cmd := exec.Command(argv[0], argv[1:]...)
	if e.AppName != "" {
		cmd.Env = append(os.Environ(), "NVIM_APPNAME="+e.AppName)
	}
	return cmd
}

// PaneCommand is the program line a zellij editor pane runs in dir. A GUI
// launcher returns at once, which would close the pane, so the pane opens
// the window and stays a shell.
func (e Editor) PaneCommand(dir string) (prog string, args []string) {
	if e.Kind == Code {
		return "sh", []string{"-c", `"$1" "$2"; exec "${SHELL:-sh}"`, "sh", e.Cmd, dir}
	}
	if e.Kind == Shell {
		return "sh", []string{"-c", e.Cmd + ` "$1"`, "sh", dir}
	}
	if e.AppName != "" {
		return "env", []string{"NVIM_APPNAME=" + e.AppName, e.Cmd}
	}
	return e.Cmd, nil
}

// String is the editor for display, e.g. in doctor: name and program.
func (e Editor) String() string {
	if e.Cmd == "" {
		return e.Name
	}
	if e.Kind == Shell {
		return e.Name + " (" + strings.TrimSpace(e.Cmd) + ")"
	}
	return e.Name + " (" + filepath.Clean(e.Cmd) + ")"
}
