package dotfiles

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/git"
)

// srcRepo creates a git repository holding files (name -> content; a name
// ending in "*" is written executable) and returns its path.
func srcRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, files)
	gitIn(t, dir, "init", "-q", "-b", "main")
	commitAll(t, dir)
	return dir
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, "*") {
			name, mode = strings.TrimSuffix(name, "*"), 0o755
		}
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
}

func commitAll(t *testing.T, dir string) {
	t.Helper()
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "c")
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type env struct {
	data, home string
	now        time.Time
	app        Applier
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{data: t.TempDir(), home: t.TempDir(), now: time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)}
	e.app = Applier{
		DataDir: e.data, Home: e.home, Root: "/projects",
		Getenv: func(string) string { return "" },
		Git:    func(context.Context, string, string) (git.Client, error) { return git.Client{}, nil },
		Now:    func() time.Time { return e.now },
	}
	return e
}

func (e *env) apply(repo string, mod ...func(*Spec)) Result {
	s := Spec{Project: "acme", Repo: repo, Symlink: true}
	for _, m := range mod {
		m(&s)
	}
	return e.app.Apply(context.Background(), s)
}

func TestFindScriptOrder(t *testing.T) {
	// Every name beats the ones after it, in the order `coder dotfiles` searches.
	for i, want := range installScripts {
		dir := t.TempDir()
		for _, later := range installScripts[i:] {
			writeFiles(t, dir, map[string]string{later: ""})
		}
		got, err := findScript(dir, "")
		if err != nil || got != want {
			t.Errorf("with %v present: got %q, %v; want %q", installScripts[i:], got, err, want)
		}
	}
	if got, _ := findScript(t.TempDir(), ""); got != "" {
		t.Errorf("empty repo: got %q", got)
	}
	if !slices.Equal(InstallScripts()[:3], []string{"install.sh", "install", "bootstrap.sh"}) {
		t.Errorf("InstallScripts = %v", InstallScripts())
	}
}

func TestFindScriptInstallOverride(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"install.sh": "", "tools/mine.sh": ""})
	if got, err := findScript(dir, "tools/mine.sh"); err != nil || got != "tools/mine.sh" {
		t.Errorf("got %q, %v", got, err)
	}
	if _, err := findScript(dir, "tools/gone.sh"); err == nil {
		t.Error("a missing override must be an error, not a fall back to install.sh")
	}
}

func TestCloneRunsScriptWithEnvironment(t *testing.T) {
	e := newEnv(t)
	repo := srcRepo(t, map[string]string{
		"install.sh*": "#!/bin/sh\nprintf '%s|%s|%s|%s' \"$ADE_PROJECT\" \"$ADE_ROOT\" \"$ADE_DOTFILES_DIR\" \"$PWD\" > \"$HOME_OUT/out\"\n",
		".bashrc":     "x",
	})
	out := t.TempDir()
	t.Setenv("HOME_OUT", out)
	res := e.apply(repo)
	dir := Dir(e.data, "acme")
	if res.Status != StatusOK || res.Method != MethodScript || res.Script != "install.sh" || res.Dir != dir {
		t.Fatalf("result = %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(out, "out"))
	if want := "acme|/projects|" + dir + "|" + dir; string(got) != want {
		t.Errorf("script saw %q, want %q", got, want)
	}
	if _, err := os.Lstat(filepath.Join(e.home, ".bashrc")); err == nil {
		t.Error("an install script replaces the symlinking")
	}
	st, ok := ReadState(e.data, "acme")
	if !ok || st.Commit == "" || st.Commit != res.Commit || st.Method != MethodScript || !st.AppliedAt.Equal(e.now) {
		t.Errorf("state = %+v, %v", st, ok)
	}
}

func TestScriptWithoutExecuteBitRunsThroughSh(t *testing.T) {
	e := newEnv(t)
	repo := srcRepo(t, map[string]string{"setup": "touch ran\n"})
	if res := e.apply(repo); res.Status != StatusOK || res.Method != MethodScript {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(Dir(e.data, "acme"), "ran")); err != nil {
		t.Error("script did not run in the clone")
	}
}

func TestFailingScriptFailsTheResult(t *testing.T) {
	e := newEnv(t)
	repo := srcRepo(t, map[string]string{"install.sh*": "#!/bin/sh\necho nope >&2\nexit 4\n"})
	res := e.apply(repo)
	if !res.Failed() || res.Method != MethodScript || !strings.Contains(res.Error, "nope") {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := ReadState(e.data, "acme"); ok {
		t.Error("a failed apply must not be recorded as applied")
	}
}

func TestPullWhenTheURLMatches(t *testing.T) {
	e := newEnv(t)
	repo := srcRepo(t, map[string]string{"install.sh*": "#!/bin/sh\ncp VERSION \"$ADE_DOTFILES_DIR/../seen\"\n", "VERSION": "one"})
	if res := e.apply(repo); res.Status != StatusOK {
		t.Fatalf("first: %+v", res)
	}
	writeFiles(t, repo, map[string]string{"VERSION": "two"})
	commitAll(t, repo)
	res := e.apply(repo)
	if res.Status != StatusOK {
		t.Fatalf("second: %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(e.data, "dotfiles", "seen"))
	if string(got) != "two" {
		t.Errorf("script ran against %q, want the pulled version", got)
	}
	if res.Commit != gitIn(t, repo, "rev-parse", "HEAD") {
		t.Errorf("commit = %q", res.Commit)
	}
	backups, _ := filepath.Glob(Dir(e.data, "acme") + ".backup-*")
	if len(backups) != 0 {
		t.Errorf("a matching URL must not back anything up: %v", backups)
	}
}

func TestPullMethodWhenNothingToInstall(t *testing.T) {
	e := newEnv(t)
	repo := srcRepo(t, map[string]string{"README": "x"})
	if res := e.apply(repo); res.Method != MethodClone || res.Status != StatusOK {
		t.Fatalf("first: %+v", res)
	}
	if res := e.apply(repo); res.Method != MethodPull || res.Status != StatusOK {
		t.Fatalf("second: %+v", res)
	}
}

func TestChangedURLBacksUpAndClonesFresh(t *testing.T) {
	e := newEnv(t)
	old := srcRepo(t, map[string]string{"OLD": "1"})
	next := srcRepo(t, map[string]string{"NEW": "1"})
	e.apply(old)
	dir := Dir(e.data, "acme")
	res := e.apply(next)
	if res.Status != StatusOK || res.Method != MethodClone {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "NEW")); err != nil {
		t.Error("the new repo was not cloned")
	}
	backup := dir + ".backup-20261112T093000"
	if _, err := os.Stat(filepath.Join(backup, "OLD")); err != nil {
		t.Errorf("old clone not kept at %s: %v", backup, err)
	}
}

func TestBranchIsCheckedOutOnCloneAndPull(t *testing.T) {
	e := newEnv(t)
	repo := srcRepo(t, map[string]string{"A": "main"})
	gitIn(t, repo, "checkout", "-q", "-b", "team")
	writeFiles(t, repo, map[string]string{"B": "team"})
	commitAll(t, repo)
	gitIn(t, repo, "checkout", "-q", "main")
	dir := Dir(e.data, "acme")

	onTeam := func(s *Spec) { s.Branch = "team" }
	if res := e.apply(repo, onTeam); res.Status != StatusOK {
		t.Fatalf("clone: %+v", res)
	}
	if got := gitIn(t, dir, "branch", "--show-current"); got != "team" {
		t.Errorf("cloned branch %q", got)
	}
	// Dropping the branch from the manifest leaves the clone where it is; naming
	// main moves it back.
	if res := e.apply(repo, func(s *Spec) { s.Branch = "main" }); res.Status != StatusOK {
		t.Fatalf("pull: %+v", res)
	}
	if got := gitIn(t, dir, "branch", "--show-current"); got != "main" {
		t.Errorf("branch after pull %q", got)
	}
}

func TestSymlinkMovesFilesToBakAndIsIdempotent(t *testing.T) {
	e := newEnv(t)
	repo := srcRepo(t, map[string]string{
		".bashrc": "team", ".config/tool/conf": "c", ".gitconfig": "g", ".gitignore": "i", ".github/x": "x", "README.md": "r",
	})
	writeFiles(t, e.home, map[string]string{".bashrc": "mine"})
	if err := os.Symlink("/elsewhere", filepath.Join(e.home, ".config")); err != nil {
		t.Fatal(err)
	}
	dir := Dir(e.data, "acme")

	res := e.apply(repo)
	if res.Status != StatusOK || res.Method != MethodSymlink {
		t.Fatalf("result = %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(e.home, ".bashrc.bak")); string(got) != "mine" {
		t.Errorf(".bashrc.bak = %q", got)
	}
	for _, name := range []string{".bashrc", ".config", ".gitconfig"} {
		if got, err := os.Readlink(filepath.Join(e.home, name)); err != nil || got != filepath.Join(dir, name) {
			t.Errorf("%s -> %q, %v", name, got, err)
		}
	}
	for _, name := range []string{".git", ".gitignore", ".github", "README.md"} {
		if _, err := os.Lstat(filepath.Join(e.home, name)); err == nil {
			t.Errorf("%s must not be linked", name)
		}
	}

	again := e.apply(repo)
	if again.Status != StatusOK || again.Method != MethodPull {
		t.Errorf("second run = %+v: links already in place are no change", again)
	}
	if got, _ := os.ReadFile(filepath.Join(e.home, ".bashrc.bak")); string(got) != "mine" {
		t.Errorf("second run touched .bashrc.bak: %q", got)
	}
}

func TestSymlinkOffLeavesHomeAlone(t *testing.T) {
	e := newEnv(t)
	repo := srcRepo(t, map[string]string{".bashrc": "team"})
	res := e.apply(repo, func(s *Spec) { s.Symlink = false })
	if res.Status != StatusOK || res.Method != MethodClone {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Lstat(filepath.Join(e.home, ".bashrc")); err == nil {
		t.Error("symlink = false must not link")
	}
}

func TestAppliedByCoderIsSkipped(t *testing.T) {
	e := newEnv(t)
	repo := srcRepo(t, map[string]string{"install.sh*": "#!/bin/sh\ntouch \"$ADE_DOTFILES_DIR.ran\"\n"})
	coder := CoderDir(e.home)
	gitIn(t, filepath.Dir(repo), "clone", "-q", repo, coder)

	e.app.Getenv = func(k string) string {
		if k == "CODER" {
			return "true"
		}
		return ""
	}
	res := e.apply(repo)
	if res.Status != StatusAppliedByCoder || res.Method != MethodSkipped {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(Dir(e.data, "acme")); err == nil {
		t.Error("nothing is cloned when Coder applied the repo")
	}

	// A different repo in Coder's clone, or outside Coder, is applied.
	other := srcRepo(t, map[string]string{"README": "x"})
	if res := e.apply(other); res.Status != StatusOK {
		t.Errorf("different repo: %+v", res)
	}
	e.app.Getenv = func(string) string { return "" }
	if res := e.apply(repo); res.Status != StatusOK {
		t.Errorf("outside coder: %+v", res)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.app.DryRun = true
	repo := srcRepo(t, map[string]string{"install.sh*": "#!/bin/sh\ntouch /nonexistent-dir/x\n", ".bashrc": "x"})
	res := e.apply(repo)
	if res.Status != StatusDryRun || !strings.Contains(res.Detail, "would clone "+repo) {
		t.Fatalf("fresh: %+v", res)
	}
	assertEmpty(t, e.data)
	assertEmpty(t, e.home)

	// With a clone in place the plan names the script, still without running it.
	e.app.DryRun = false
	if res := e.apply(repo, func(s *Spec) { s.Install = "install.sh"; s.Symlink = false }); res.Status != StatusFailed {
		t.Fatalf("setup run should fail on the bad script: %+v", res)
	}
	before := snapshotTree(t, e.data)
	e.app.DryRun = true
	plan := e.apply(repo)
	if plan.Status != StatusDryRun || plan.Method != MethodPull || plan.Script != "install.sh" || !strings.Contains(plan.Detail, "run install.sh") {
		t.Errorf("existing: %+v", plan)
	}
	other := srcRepo(t, map[string]string{"README": "x"})
	if plan := e.apply(other); !strings.Contains(plan.Detail, ".backup-") {
		t.Errorf("changed URL: %+v", plan)
	}
	if after := snapshotTree(t, e.data); !slices.Equal(before, after) {
		t.Errorf("dry run changed the data dir:\n%v\n%v", before, after)
	}
	assertEmpty(t, e.home)
}

func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("%s is not empty: %v", dir, entries)
	}
}

func snapshotTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !strings.Contains(p, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			out = append(out, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCloneFailureIsInTheResult(t *testing.T) {
	e := newEnv(t)
	res := e.apply(filepath.Join(t.TempDir(), "missing"))
	if !res.Failed() || res.Error == "" {
		t.Fatalf("result = %+v", res)
	}
}

func TestResolveAppliesUserOverrides(t *testing.T) {
	off := false
	m := &config.Manifest{Name: "acme", Forge: "work", Dotfiles: config.Dotfiles{Repo: "git@h:team/d.git", Branch: "main", Install: "go.sh"}}
	tests := []struct {
		name string
		cfg  config.DotfilesOverride
		m    *config.Manifest
		want Spec
		ok   bool
	}{
		{"manifest as is", config.DotfilesOverride{}, m, Spec{Project: "acme", Forge: "work", Repo: "git@h:team/d.git", Branch: "main", Install: "go.sh", Symlink: true}, true},
		{"repo and branch replaced", config.DotfilesOverride{Repo: "git@h:me/d.git", Branch: "dev"}, m, Spec{Project: "acme", Forge: "work", Repo: "git@h:me/d.git", Branch: "dev", Install: "go.sh", Symlink: true}, true},
		{"branch only", config.DotfilesOverride{Branch: "dev"}, m, Spec{Project: "acme", Forge: "work", Repo: "git@h:team/d.git", Branch: "dev", Install: "go.sh", Symlink: true}, true},
		{"disabled", config.DotfilesOverride{Enabled: &off}, m, Spec{}, false},
		{"manifest without dotfiles", config.DotfilesOverride{Repo: "git@h:me/d.git"}, &config.Manifest{Name: "acme"}, Spec{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Dotfiles = tt.cfg
			got, ok := Resolve(&cfg, "acme", tt.m)
			if ok != tt.ok || got != tt.want {
				t.Errorf("got %+v, %v; want %+v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
