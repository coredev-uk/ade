// Package devshell decides how a repo's dev shell is entered, so the tools a
// repo declares (direnv's .envrc, a flake's devShell) are the ones its panes,
// services and tasks run with. The user's CDE has no globally installed
// toolchain: a Go or Node version comes from the repo.
//
// Detection order for devshell = "auto": a worktree with an .envrc and direnv
// on PATH runs through `direnv exec <dir>`; else one with a flake.nix that
// has a devShell and nix on PATH runs through `nix develop <dir> -c`; else
// nothing is wrapped. "direnv", "nix" and "none" force one choice.
//
// A project may also declare packages (`[project] packages`): ADE generates
// <root>/<project>/.ade/shell, which runs its arguments inside `nix shell`, and
// every shell is composed inside it, so a repo's own dev shell wins on conflicts.
package devshell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
)

// Mode values of `repos.<r>.devshell`.
const (
	ModeAuto   = "auto"
	ModeDirenv = "direnv"
	ModeNix    = "nix"
	ModeNone   = "none"
)

// ValidMode reports whether m is a devshell setting ("" means auto).
func ValidMode(m string) bool {
	switch m {
	case "", ModeAuto, ModeDirenv, ModeNix, ModeNone:
		return true
	}
	return false
}

// Kind is how a directory's shell is entered.
type Kind string

const (
	None   Kind = "none"
	Direnv Kind = "direnv"
	Nix    Kind = "nix"
)

// Shell is the dev shell of one directory. The zero value wraps nothing.
type Shell struct {
	Kind Kind
	Dir  string
	// Project is the project shell wrapper (ProjectScriptPath) every command
	// runs through first; empty when the project declares no packages.
	Project string
	// Note explains a forced mode that could not be honoured (for example
	// devshell = "nix" without nix on PATH); empty otherwise.
	Note string
}

// Finder is where detection looks: PATH and the file system. The zero value
// uses the real ones; tests replace them.
type Finder struct {
	LookPath func(string) (string, error)
	ReadFile func(string) ([]byte, error)
}

func (f Finder) look(name string) bool {
	l := f.LookPath
	if l == nil {
		l = exec.LookPath
	}
	_, err := l(name)
	return err == nil
}

func (f Finder) read(path string) ([]byte, error) {
	if f.ReadFile != nil {
		return f.ReadFile(path)
	}
	return os.ReadFile(path)
}

func (f Finder) hasEnvrc(dir string) bool {
	_, err := f.read(filepath.Join(dir, ".envrc"))
	return err == nil
}

// hasFlakeShell reports whether dir has a flake.nix that names a devShell
// output. It is a text check, not an evaluation: evaluating the flake would
// build things just to decide how to start a pane.
func (f Finder) hasFlakeShell(dir string) bool {
	b, err := f.read(filepath.Join(dir, "flake.nix"))
	return err == nil && strings.Contains(string(b), "devShell")
}

// Detect resolves the dev shell of dir under mode.
func Detect(dir, mode string) Shell { return Finder{}.Detect(dir, mode) }

// Detect resolves the dev shell of dir under mode ("" is auto).
func (f Finder) Detect(dir, mode string) Shell {
	none := Shell{Kind: None, Dir: dir}
	switch mode {
	case ModeNone:
		return none
	case ModeDirenv:
		if !f.look("direnv") {
			none.Note = `devshell = "direnv" but direnv is not on PATH`
			return none
		}
		if !f.hasEnvrc(dir) {
			none.Note = `devshell = "direnv" but the worktree has no .envrc`
			return none
		}
		return Shell{Kind: Direnv, Dir: dir}
	case ModeNix:
		if !f.look("nix") {
			none.Note = `devshell = "nix" but nix is not on PATH`
			return none
		}
		if !f.hasFlakeShell(dir) {
			none.Note = `devshell = "nix" but the worktree has no flake.nix with a devShell`
			return none
		}
		return Shell{Kind: Nix, Dir: dir}
	}
	if f.hasEnvrc(dir) && f.look("direnv") {
		return Shell{Kind: Direnv, Dir: dir}
	}
	if f.hasFlakeShell(dir) && f.look("nix") {
		return Shell{Kind: Nix, Dir: dir}
	}
	return none
}

// Active reports whether the shell wraps anything.
func (s Shell) Active() bool { return s.Kind == Direnv || s.Kind == Nix || s.Project != "" }

// Wrap returns argv run inside the dev shell: `direnv exec <dir> argv...` or
// `nix develop <dir> -c argv...`, behind the project shell when there is one
// (`.ade/shell direnv exec <dir> argv...`). A directory without either gets
// argv back.
func (s Shell) Wrap(argv []string) []string {
	switch s.Kind {
	case Direnv:
		argv = append([]string{"direnv", "exec", s.Dir}, argv...)
	case Nix:
		argv = append([]string{"nix", "develop", s.Dir, "-c"}, argv...)
	}
	if s.Project != "" {
		return append([]string{s.Project}, argv...)
	}
	return argv
}

// WarmArgv is the command that realises the shell without running anything in
// it (loads direnv's environment, builds the flake's devShell), so the first
// pane does not wait for it. Nil when nothing is wrapped.
func (s Shell) WarmArgv() []string {
	if !s.Active() {
		return nil
	}
	return s.Wrap([]string{"true"})
}

// String is the dev shell for messages: "direnv", "nix" or "none", with
// "+project" when the project shell wraps it.
func (s Shell) String() string {
	k := string(s.Kind)
	if s.Kind == "" {
		k = string(None)
	}
	if s.Project != "" {
		k += "+project"
	}
	return k
}

// ProjectShellRel is the project shell wrapper, relative to the project directory.
const ProjectShellRel = ".ade/shell"

// ProjectShellPath is where the project shell wrapper of projectDir lives.
func ProjectShellPath(projectDir string) string {
	return filepath.Join(projectDir, filepath.FromSlash(ProjectShellRel))
}

// WithProject composes the project shell around s: the wrapper script runs
// first and the repo's own shell inside it, so a repo's tools win over the
// project's. Nothing is added when the project declares no packages, when
// the script has not been generated yet, or when nix is not on PATH (the
// returned shell then carries a Note).
func (f Finder) WithProject(s Shell, projectDir string, setup config.ProjectSetup) Shell {
	if len(setup.Packages) == 0 {
		return s
	}
	script := ProjectShellPath(projectDir)
	if _, err := f.read(script); err != nil {
		return s
	}
	if !f.look("nix") {
		s.Note = joinNote(s.Note, "project packages need nix, which is not on PATH")
		return s
	}
	s.Project = script
	return s
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// ProjectShellMarker is the second line of the generated wrapper; ADE only
// rewrites or removes a script that carries it.
const ProjectShellMarker = "# Generated by ADE from the manifest's [project] table; edits are overwritten by `ade update`."

// ProjectScript is the wrapper: with no arguments it starts $SHELL (else
// /bin/sh), otherwise it runs its arguments, both inside `nix shell` with
// the packages of nixpkgs (empty: the registry's "nixpkgs") on PATH.
func ProjectScript(nixpkgs string, packages []string) string {
	if nixpkgs == "" {
		nixpkgs = "nixpkgs"
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n" + ProjectShellMarker + "\n")
	b.WriteString("# Usage: .ade/shell [command [args...]]   (no command: an interactive $SHELL)\n")
	b.WriteString("if [ \"$#\" -eq 0 ]; then\n  set -- \"${SHELL:-/bin/sh}\"\nfi\n")
	b.WriteString("exec nix --extra-experimental-features 'nix-command flakes' shell")
	for _, p := range packages {
		b.WriteString(" " + shellQuote(nixpkgs+"#"+p))
	}
	b.WriteString(" --command \"$@\"\n")
	return b.String()
}

// shellQuote single-quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// WarmProjectArgv realises the project shell without running anything in it,
// so the first pane does not wait for the packages to download.
func WarmProjectArgv(projectDir string) []string {
	return []string{ProjectShellPath(projectDir), "true"}
}
