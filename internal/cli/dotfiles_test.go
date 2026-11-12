package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/doctor"
	"github.com/coredev-uk/ade/internal/dotfiles"
)

// dotfilesRepo creates a git repo whose install script (when script is not
// empty) is install.sh, and returns its path.
func dotfilesRepo(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{".bashrc": "team\n"}
	if script != "" {
		files["install.sh"] = script
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// addDotfiles appends a [dotfiles] table to the env's manifest.
func (e *initEnv) addDotfiles(repo string, extra string) {
	e.t.Helper()
	f, err := os.OpenFile(e.manifest, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("\n[dotfiles]\nrepo = \"" + repo + "\"\n" + extra); err != nil {
		e.t.Fatal(err)
	}
}

func (e *initEnv) dotfilesCmd(args ...string) (string, error) {
	e.t.Helper()
	jsonOut = false // setup in the same test turned it on
	a, err := app.Load("user")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { a.Close() }) //nolint:errcheck
	cmd := newDotfilesCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err = cmd.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, a))
	return out.String(), err
}

func TestSetupAppliesProjectDotfiles(t *testing.T) {
	e := newInitEnv(t)
	e.setupEnvVars()
	e.addDotfiles(dotfilesRepo(t, "#!/bin/sh\necho \"$ADE_PROJECT\" > \"$HOME/dotfiles-ran\"\n"), "")

	res, progress, err := e.setup()
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, progress)
	}
	if len(res.Dotfiles) != 1 {
		t.Fatalf("dotfiles = %+v\n%s", res.Dotfiles, progress)
	}
	d := res.Dotfiles[0]
	if d.Project != "team" || d.Method != dotfiles.MethodScript || d.Status != dotfiles.StatusOK || d.Error != "" {
		t.Errorf("dotfiles result = %+v", d)
	}
	if got, _ := os.ReadFile(filepath.Join(e.home, "dotfiles-ran")); strings.TrimSpace(string(got)) != "team" {
		t.Errorf("install script marker = %q", got)
	}
	if !strings.Contains(progress, "dotfiles") {
		t.Errorf("no dotfiles progress line:\n%s", progress)
	}

	// The second run pulls and runs the script again.
	res, _, err = e.setup()
	if err != nil || len(res.Dotfiles) != 1 || res.Dotfiles[0].Status != dotfiles.StatusOK {
		t.Errorf("second run: %+v, %v", res.Dotfiles, err)
	}
}

func TestSetupDotfilesFailureIsPartialAndBlocksNothing(t *testing.T) {
	e := newInitEnv(t)
	e.setupEnvVars()
	e.addDotfiles(dotfilesRepo(t, "#!/bin/sh\necho boom >&2\nexit 2\n"), "")

	res, progress, err := e.setup()
	if exitOf(err) != exitSetupPartial || res.Status != "partial" {
		t.Fatalf("exit = %d status = %q, want partial\n%s", exitOf(err), res.Status, progress)
	}
	if len(res.Dotfiles) != 1 || res.Dotfiles[0].Status != dotfiles.StatusFailed || !strings.Contains(res.Dotfiles[0].Error, "boom") {
		t.Errorf("dotfiles = %+v", res.Dotfiles)
	}
	if len(res.Sync) != 1 || res.Sync[0].Error != "" {
		t.Errorf("projects step did not complete: %+v", res.Sync)
	}
	if res.ZellijConfig == "" || len(res.Tools) == 0 {
		t.Errorf("the machine step was blocked: zellij %q, %d tools", res.ZellijConfig, len(res.Tools))
	}
}

func TestSetupDotfilesDisabledByUserConfig(t *testing.T) {
	e := newInitEnv(t)
	e.setupEnvVars()
	e.addDotfiles(dotfilesRepo(t, "#!/bin/sh\ntouch \"$HOME/dotfiles-ran\"\n"), "")
	if _, p, err := e.setup(); err != nil {
		t.Fatalf("first run: %v\n%s", err, p)
	}
	if err := os.Remove(filepath.Join(e.home, "dotfiles-ran")); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(e.configPath(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("\n[dotfiles]\nenabled = false\n") //nolint:errcheck
	f.Close()                                        //nolint:errcheck

	res, p, err := e.setup()
	if err != nil || len(res.Dotfiles) != 0 {
		t.Fatalf("dotfiles = %+v, err %v\n%s", res.Dotfiles, err, p)
	}
	if _, err := os.Stat(filepath.Join(e.home, "dotfiles-ran")); err == nil {
		t.Error("disabled dotfiles still ran their script")
	}
}

func TestDotfilesCommandDryRunWritesNothingAndDoctorWarns(t *testing.T) {
	e := newInitEnv(t)
	e.setupEnvVars()
	repo := dotfilesRepo(t, "#!/bin/sh\ntouch \"$HOME/dotfiles-ran\"\n")
	e.addDotfiles(repo, "")
	// Project step only: a token-less run would skip it, so use the real setup
	// then forget the dotfiles state.
	if _, p, err := e.setup(); err != nil {
		t.Fatalf("setup: %v\n%s", err, p)
	}
	dataDir := filepath.Join(e.home, ".local", "share", "ade")
	if err := os.RemoveAll(filepath.Join(dataDir, "dotfiles")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(e.home, "dotfiles-ran")); err != nil {
		t.Fatal(err)
	}

	a, err := app.Load("user")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close() //nolint:errcheck
	checks := doctor.Run(context.Background(), doctor.Options{
		ConfigFile: a.Paths.ConfigFile, Config: a.Config, Root: a.Root, Records: a.RecordsDir, DataDir: a.Paths.DataDir,
		LookPath: exec.LookPath, Getenv: func(k string) string { return map[string]string{"HOME": e.home}[k] },
	})
	var got *doctor.Check
	for i, c := range checks {
		if c.Group == "dotfiles" {
			got = &checks[i]
		}
	}
	if got == nil || got.Status != doctor.Warn || !strings.Contains(got.Detail, "never applied") {
		t.Fatalf("doctor dotfiles check = %+v", got)
	}

	out, err := e.dotfilesCmd("--dry-run")
	if err != nil || !strings.Contains(out, "would clone "+repo) {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "dotfiles")); err == nil {
		t.Error("dry run created the dotfiles directory")
	}
	if _, err := os.Stat(filepath.Join(e.home, "dotfiles-ran")); err == nil {
		t.Error("dry run ran the install script")
	}
	if out, err := e.dotfilesCmd("status"); err != nil || !strings.Contains(out, "never applied") {
		t.Errorf("status before: %v\n%s", err, out)
	}

	if out, err := e.dotfilesCmd(); err != nil || !strings.Contains(out, "team: script, ran install.sh") {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(e.home, "dotfiles-ran")); err != nil {
		t.Error("install script did not run")
	}
	if out, err := e.dotfilesCmd("status"); err != nil || !strings.Contains(out, "applied ") || strings.Contains(out, "never") {
		t.Errorf("status after: %v\n%s", err, out)
	}
	if _, err := e.dotfilesCmd("--project", "nope"); err == nil {
		t.Error("an unknown project must be an error")
	}
}

func TestDotfilesStatusAgreesWithDoctorWhenTheRepoChanged(t *testing.T) {
	e := newInitEnv(t)
	e.setupEnvVars()
	e.addDotfiles(dotfilesRepo(t, "#!/bin/sh\ntrue\n"), "")
	if _, p, err := e.setup(); err != nil {
		t.Fatalf("setup: %v\n%s", err, p)
	}
	if out, err := e.dotfilesCmd("status"); err != nil || !strings.Contains(out, "applied ") || strings.Contains(out, "never") {
		t.Fatalf("status after setup: %v\n%s", err, out)
	}

	// The user points the project at another repo: what was applied is of the old one.
	other := dotfilesRepo(t, "#!/bin/sh\ntrue\n")
	f, err := os.OpenFile(e.configPath(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("\n[dotfiles]\nrepo = \"" + other + "\"\n") //nolint:errcheck
	f.Close()                                                 //nolint:errcheck
	if out, err := e.dotfilesCmd("status"); err != nil || !strings.Contains(out, "never applied") {
		t.Errorf("status after the repo changed: %v\n%s", err, out)
	}
}

func TestInstallScriptDoesNotGetForgeTokens(t *testing.T) {
	e := newInitEnv(t)
	e.setupEnvVars()
	e.addDotfiles(dotfilesRepo(t, "#!/bin/sh\nprintf '%s' \"${"+initToken+"-unset}\" > \"$HOME/seen-token\"\n"), "")
	if _, p, err := e.setup(); err != nil {
		t.Fatalf("setup: %v\n%s", err, p)
	}
	if got, _ := os.ReadFile(filepath.Join(e.home, "seen-token")); string(got) != "unset" {
		t.Errorf("script saw the forge token variable: %q", got)
	}
}
