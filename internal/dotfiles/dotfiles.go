// Package dotfiles applies a project's team dotfiles, the repository its
// members use with `coder dotfiles`: it is cloned under ADE's data directory,
// its install script is run, or its dotfiles are symlinked into $HOME. The
// steps follow `coder dotfiles` (cli/dotfiles.go in github.com/coder/coder),
// except that SSH host keys stay checked.
package dotfiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/paths"
)

// Methods a Result reports: the last thing done to the dotfiles.
const (
	MethodClone   = "clone"
	MethodPull    = "pull"
	MethodScript  = "script"
	MethodSymlink = "symlink"
	MethodSkipped = "skipped"
)

// Statuses of a Result.
const (
	StatusOK             = "ok"
	StatusFailed         = "failed"
	StatusDryRun         = "dry-run"
	StatusAppliedByCoder = "applied by coder"
)

// installScripts is the search order of `coder dotfiles`: the first file that
// exists wins.
var installScripts = []string{
	"install.sh", "install", "bootstrap.sh", "bootstrap", "setup.sh", "setup",
	"script/install.sh", "script/install", "script/bootstrap.sh", "script/bootstrap",
	"script/setup.sh", "script/setup",
}

// InstallScripts returns the install script names in search order.
func InstallScripts() []string { return append([]string(nil), installScripts...) }

// Spec is the dotfiles of one project after the user's overrides.
type Spec struct {
	Project string
	Forge   string // the manifest's forge: its token is tried first for Repo
	Repo    string
	Branch  string
	Install string
	Symlink bool
}

// Resolve returns the dotfiles to apply for a project, or false when its
// manifest has none or the user turned project dotfiles off.
func Resolve(cfg *config.Config, project string, m *config.Manifest) (Spec, bool) {
	if m == nil || !m.Dotfiles.Configured() || !cfg.Dotfiles.On() {
		return Spec{}, false
	}
	s := Spec{
		Project: project, Forge: m.Forge,
		Repo: m.Dotfiles.Repo, Branch: m.Dotfiles.Branch,
		Install: m.Dotfiles.Install, Symlink: m.Dotfiles.SymlinkFallback(),
	}
	if cfg.Dotfiles.Repo != "" {
		s.Repo, s.Branch = cfg.Dotfiles.Repo, cfg.Dotfiles.Branch
	} else if cfg.Dotfiles.Branch != "" {
		s.Branch = cfg.Dotfiles.Branch
	}
	return s, true
}

// Result is what applying one project's dotfiles did.
type Result struct {
	Project string `json:"project"`
	Repo    string `json:"repo"`
	Dir     string `json:"dir"`
	Method  string `json:"method"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
	Script  string `json:"script,omitempty"`
	Commit  string `json:"commit,omitempty"`
	// Detail says what a dry run would do, or what a run found.
	Detail string `json:"detail,omitempty"`
}

// Failed reports whether the dotfiles could not be applied.
func (r Result) Failed() bool { return r.Status == StatusFailed }

// Applier applies dotfiles. Git returns the client authorised for a URL.
type Applier struct {
	DataDir string // ADE data directory: clones live in <DataDir>/dotfiles/<project>
	Home    string // where dotfiles are symlinked
	Root    string // projects root, handed to install scripts as ADE_ROOT
	DryRun  bool   // report what would happen, write nothing
	// DropEnv names environment variables the install script does not get:
	// the forge token variables, which the script has no use for.
	DropEnv []string
	Getenv  func(string) string
	Git     func(ctx context.Context, forge, rawURL string) (git.Client, error)
	Now     func() time.Time
	Out     io.Writer // install script output; nil discards it
}

// Dir is where a project's dotfiles are cloned.
func Dir(dataDir, project string) string { return filepath.Join(dataDir, "dotfiles", project) }

func (a Applier) getenv(k string) string {
	if a.Getenv == nil {
		return os.Getenv(k)
	}
	return a.Getenv(k)
}

func (a Applier) now() time.Time {
	if a.Now == nil {
		return time.Now()
	}
	return a.Now()
}

// CoderDir is where `coder dotfiles` clones the repo.
func CoderDir(home string) string { return filepath.Join(home, ".config", "coderv2", "dotfiles") }

// CoderApplied reports whether this is a Coder workspace whose own dotfiles
// step already applied repo: its clone's origin is the same URL. Without an
// absolute home the clone cannot be found.
func CoderApplied(ctx context.Context, getenv func(string) string, home, repo string) bool {
	if getenv("CODER") != "true" || !filepath.IsAbs(home) {
		return false
	}
	origin, err := (git.Client{}).RemoteURL(ctx, CoderDir(home))
	return err == nil && sameURL(origin, repo)
}

// sameURL reports whether a and b name the same repository: the scheme (ssh
// or https), user, port, case of the host, a trailing .git and slashes do not
// matter.
func sameURL(a, b string) bool { return repoKey(a) == repoKey(b) }

// repoKey reduces a git URL (https://, ssh://, scp-like user@host:path or a
// local path) to host/path.
func repoKey(raw string) string {
	raw = strings.TrimSpace(raw)
	host, path := "", raw
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		host, path = strings.ToLower(u.Hostname()), u.Path
	} else if before, after, ok := strings.Cut(raw, ":"); ok && !strings.Contains(before, "/") {
		// scp-like: [user@]host:path
		if _, h, found := strings.Cut(before, "@"); found {
			before = h
		}
		host, path = strings.ToLower(before), after
	}
	path = strings.TrimRight(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimRight(path, "/")
	if host == "" {
		return path
	}
	return host + "/" + strings.TrimLeft(path, "/")
}

// Apply clones or updates the project's dotfiles and runs the install script
// or symlinks them. A failure is in the Result, never panicked or returned.
func (a Applier) Apply(ctx context.Context, s Spec) Result {
	res := Result{Project: s.Project, Repo: s.Repo, Dir: Dir(a.DataDir, s.Project)}
	fail := func(err error) Result {
		res.Status, res.Error = StatusFailed, err.Error()
		return res
	}

	if CoderApplied(ctx, a.getenv, a.Home, s.Repo) {
		res.Method, res.Status = MethodSkipped, StatusAppliedByCoder
		return res
	}
	client, err := a.Git(ctx, s.Forge, s.Repo)
	if err != nil {
		return fail(err)
	}

	origin, cloned := "", false
	if fi, err := os.Stat(res.Dir); err == nil && fi.IsDir() {
		cloned = true
		origin, _ = client.RemoteURL(ctx, res.Dir) // not a repository: an empty origin, replaced below
	}
	switch {
	case cloned && sameURL(origin, s.Repo):
		res.Method = MethodPull
	case cloned:
		res.Method = MethodClone
		res.Detail = "the repo changed: the old clone is moved to " + a.backupDir(res.Dir)
	default:
		res.Method = MethodClone
	}

	if a.DryRun {
		return a.plan(res, s, cloned && res.Method == MethodPull)
	}

	switch res.Method {
	case MethodPull:
		err = a.pull(ctx, client, res.Dir, s.Branch)
	default:
		if cloned {
			err = os.Rename(res.Dir, a.backupDir(res.Dir))
		}
		if err == nil {
			err = a.clone(ctx, client, s, res.Dir)
		}
	}
	if err != nil {
		return fail(err)
	}
	res.Commit, _ = client.Run(ctx, res.Dir, "rev-parse", "HEAD")
	res.Commit = strings.TrimSpace(res.Commit)

	script, err := findScript(res.Dir, s.Install)
	switch {
	case err != nil:
		return fail(err)
	case script != "":
		res.Script = script
		if err := a.runScript(ctx, s, res.Dir, script); err != nil {
			res.Method = MethodScript
			return fail(err)
		}
		res.Method = MethodScript
	case s.Symlink:
		n, err := a.symlinkAll(res.Dir)
		if err != nil {
			res.Method = MethodSymlink
			return fail(err)
		}
		if n > 0 {
			res.Method = MethodSymlink
		}
	}
	res.Status = StatusOK
	if err := writeState(a.DataDir, s.Project, State{
		Repo: s.Repo, Branch: s.Branch, Commit: res.Commit, Method: res.Method, AppliedAt: a.now().UTC(),
	}); err != nil {
		return fail(fmt.Errorf("record state: %w", err))
	}
	return res
}

// plan fills in a dry run: what Apply would do, judged from the clone that is
// already there (a fresh clone cannot be inspected without making it).
func (a Applier) plan(res Result, s Spec, existing bool) Result {
	res.Status = StatusDryRun
	var steps []string
	switch {
	case res.Method == MethodPull:
		steps = append(steps, "pull")
	case res.Detail != "":
		steps = append(steps, "move the old clone to "+a.backupDir(res.Dir)+", clone "+s.Repo)
	default:
		steps = append(steps, "clone "+s.Repo)
	}
	switch {
	case existing:
		script, err := findScript(res.Dir, s.Install)
		switch {
		case err != nil:
			steps = append(steps, err.Error())
		case script != "":
			res.Script = script
			steps = append(steps, "run "+script)
		case s.Symlink:
			steps = append(steps, "symlink its dotfiles into "+a.Home)
		}
	case s.Install != "":
		steps = append(steps, "run "+s.Install)
	default:
		steps = append(steps, "then run its install script, or symlink its dotfiles into "+a.Home)
	}
	res.Detail = "would " + strings.Join(steps, ", ")
	return res
}

func (a Applier) backupDir(dir string) string {
	return dir + ".backup-" + a.now().Format("20060102T150405")
}

func (a Applier) clone(ctx context.Context, c git.Client, s Spec, dir string) error {
	if err := paths.EnsureDir(filepath.Dir(dir)); err != nil {
		return err
	}
	args := []string{"clone", "--quiet"}
	if s.Branch != "" {
		args = append(args, "--branch", s.Branch)
	}
	_, err := c.Run(ctx, "", append(args, "--", s.Repo, dir)...)
	return err
}

// pull brings the clone to origin's branch: the configured one, else origin's
// default. The clone is ADE-managed, so when it cannot fast-forward (the
// remote was force-pushed, a script changed it) it is reset to origin.
func (a Applier) pull(ctx context.Context, c git.Client, dir, branch string) error {
	if _, err := c.Run(ctx, dir, "fetch", "--quiet", "origin"); err != nil {
		return err
	}
	cur, err := c.Run(ctx, dir, "branch", "--show-current")
	if err != nil {
		return err
	}
	cur = strings.TrimSpace(cur)
	if branch == "" {
		branch = defaultBranch(ctx, c, dir, cur)
	}
	if branch == "" {
		return errors.New("the clone is not on a branch and origin names no default branch: set dotfiles.branch")
	}
	remote := "origin/" + branch
	if cur != branch {
		_, err := c.Run(ctx, dir, "checkout", "--force", "-B", branch, remote)
		return err
	}
	if _, err := c.Run(ctx, dir, "merge", "--ff-only", "--quiet", remote); err != nil {
		_, err = c.Run(ctx, dir, "reset", "--hard", "--quiet", remote)
		return err
	}
	return nil
}

// defaultBranch is origin's default branch, or fallback when origin does not
// say.
func defaultBranch(ctx context.Context, c git.Client, dir, fallback string) string {
	if _, err := c.Run(ctx, dir, "remote", "set-head", "origin", "--auto"); err != nil {
		return fallback
	}
	ref, err := c.Run(ctx, dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if name := strings.TrimPrefix(strings.TrimSpace(ref), "origin/"); err == nil && name != "" {
		return name
	}
	return fallback
}

// findScript returns the install script (relative to dir) to run: install if
// set, else the first of installScripts that exists. "" means none.
func findScript(dir, install string) (string, error) {
	if install != "" {
		if !filepath.IsLocal(install) {
			return "", fmt.Errorf("install script %s must be a path inside the dotfiles repo", install)
		}
		if fi, err := os.Stat(filepath.Join(dir, install)); err != nil || fi.IsDir() {
			return "", fmt.Errorf("install script %s is not in the dotfiles repo", install)
		}
		return install, nil
	}
	for _, name := range installScripts {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && !fi.IsDir() {
			return name, nil
		}
	}
	return "", nil
}

// scriptWaitDelay is how long output is read after the script itself has
// exited, for a daemon it started that keeps stdout open.
var scriptWaitDelay = 5 * time.Second

// scriptEnv is the environment of the install script: ours, without DropEnv.
func (a Applier) scriptEnv() []string {
	env := os.Environ()
	if len(a.DropEnv) == 0 {
		return env
	}
	return slices.DeleteFunc(env, func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(a.DropEnv, name)
	})
}

// runScript runs the script in the clone: directly when it is executable,
// through sh when not.
func (a Applier) runScript(ctx context.Context, s Spec, dir, script string) error {
	path := filepath.Join(dir, script)
	cmd := exec.CommandContext(ctx, path)
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o111 == 0 {
		cmd = exec.CommandContext(ctx, "sh", path)
	}
	cmd.Dir = dir
	cmd.Env = append(a.scriptEnv(), "ADE_PROJECT="+s.Project, "ADE_ROOT="+a.Root, "ADE_DOTFILES_DIR="+dir)
	// Its own process group, killed whole on timeout or cancel, so a script's
	// background children cannot outlive it holding the output pipes open.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = scriptWaitDelay
	var tail bytes.Buffer
	var w io.Writer = &tail
	if a.Out != nil {
		w = io.MultiWriter(a.Out, &tail)
	}
	cmd.Stdout, cmd.Stderr = w, w
	// A script that exits 0 but leaves a daemon holding the pipes is a success.
	if err := cmd.Run(); err != nil && (!errors.Is(err, exec.ErrWaitDelay) || ctx.Err() != nil) {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		msg := strings.TrimSpace(tail.String())
		if len(msg) > 300 {
			msg = "…" + msg[len(msg)-300:]
		}
		if msg == "" {
			return fmt.Errorf("running %s: %w", script, err)
		}
		return fmt.Errorf("running %s: %w: %s", script, err, msg)
	}
	return nil
}

// isDotfile follows `coder dotfiles`: every top-level dot entry except the
// repo's own git files, with .gitconfig the one .git* file that is linked.
func isDotfile(name string) bool {
	return strings.HasPrefix(name, ".") && (!strings.HasPrefix(name, ".git") || name == ".gitconfig")
}

// symlinkAll links each dotfile of dir into Home and returns how many it
// linked or relinked. An existing file or directory is moved to <name>.bak
// first (<name>.bak.1, ... when that is taken, so no backup is overwritten),
// an existing symlink is replaced, a link already in place is left.
func (a Applier) symlinkAll(dir string) (int, error) {
	if !filepath.IsAbs(a.Home) {
		return 0, fmt.Errorf("cannot symlink dotfiles: the home directory is %q, not an absolute path (is HOME set?)", a.Home)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var errs []error
	n := 0
	for _, e := range entries {
		if !isDotfile(e.Name()) {
			continue
		}
		from, to := filepath.Join(dir, e.Name()), filepath.Join(a.Home, e.Name())
		fi, err := os.Lstat(to)
		switch {
		case err == nil && fi.Mode()&fs.ModeSymlink != 0:
			if cur, _ := os.Readlink(to); cur == from {
				continue
			}
			err = os.Remove(to)
		case err == nil:
			var backup string
			if backup, err = freeBackup(to); err == nil {
				err = os.Rename(to, backup)
			}
		case errors.Is(err, fs.ErrNotExist):
			err = nil
		}
		if err == nil {
			err = os.Symlink(from, to)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// freeBackup returns the first of path.bak, path.bak.1, ... that does not exist.
func freeBackup(path string) (string, error) {
	for i := 0; ; i++ {
		name := path + ".bak"
		if i > 0 {
			name += "." + strconv.Itoa(i)
		}
		if _, err := os.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			return name, nil
		} else if err != nil {
			return "", err
		}
	}
}

// State is the record of the last successful apply of a project's dotfiles.
type State struct {
	Repo      string    `json:"repo"`
	Branch    string    `json:"branch,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	Method    string    `json:"method"`
	AppliedAt time.Time `json:"applied_at"`
}

// stateFile lives beside the clones, in a directory no project name can take
// (project names do not start with a dot), so moving a clone aside keeps it.
func stateFile(dataDir, project string) string {
	return filepath.Join(dataDir, "dotfiles", ".state", project+".json")
}

func writeState(dataDir, project string, st State) error {
	path := stateFile(dataDir, project)
	if err := paths.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ReadState returns the last apply of a project's dotfiles, if there was one.
func ReadState(dataDir, project string) (State, bool) {
	data, err := os.ReadFile(stateFile(dataDir, project))
	if err != nil {
		return State{}, false
	}
	var st State
	if json.Unmarshal(data, &st) != nil {
		return State{}, false
	}
	return st, true
}
