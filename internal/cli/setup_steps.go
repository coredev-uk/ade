package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/devshell"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/host/zellij"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/project"
)

// checkTemplate rejects a --config-from file that ADE would not accept.
func checkTemplate(data []byte) error {
	dir, err := os.MkdirTemp("", "ade-setup-template")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir) //nolint:errcheck
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	for _, is := range cfg.Validate() {
		if is.Severity == config.SeverityError {
			return fmt.Errorf("template is invalid: %s: %s", is.Path, is.Msg)
		}
	}
	return nil
}

// checkForgeToken resolves the forge's read token (the write token when there
// is none) and asks the forge who it is. A forge with only a read token is
// read-only and passes.
func checkForgeToken(ctx context.Context, a *app.App, name string, fg config.Forge) (string, error) {
	if _, err := fg.ReadToken(ctx); err != nil {
		return "", fmt.Errorf("no usable token: %w", err)
	}
	c, err := forgesync.New(a).Open(ctx, name, fg)
	if err != nil {
		return "", err
	}
	me, err := c.Me(ctx)
	if err != nil {
		return "", fmt.Errorf("the forge rejected the token: %w", err)
	}
	return me.Username, nil
}

// runSetupProjects is step 3: add (clone + baseline worktrees + hooks),
// update (fetch / fast-forward), first sync.
// newSources are --project manifests the config does not list yet: Add
// registers them in the config (appending, never rewriting it).
func runSetupProjects(ctx context.Context, say io.Writer, a *app.App, newSources []string, res *setupResult) {
	svc := project.New(a, "ade:setup")
	sources := make([]string, 0, len(a.Config.Projects)+len(newSources))
	for _, p := range a.Config.Projects {
		sources = append(sources, p.Source)
	}
	sources = append(sources, newSources...)
	for i, src := range sources {
		rep, err := svc.Add(ctx, src)
		if err != nil {
			step(say, "projects", "add %s: FAILED: %v", src, err)
			res.problem(exitSetupPartial, "add %s: %v", src, err)
			continue
		}
		if i >= len(sources)-len(newSources) {
			res.Registered = append(res.Registered, src)
			step(say, "projects", "registered %s in %s", src, a.Config.File)
		}
		res.Add = append(res.Add, rep)
		step(say, "projects", "%s", rep.Title())
		if rep.Failed() {
			res.problem(exitSetupPartial, "%s", rep.Title())
		}
	}

	rep, err := svc.Update(ctx)
	if err != nil {
		step(say, "update", "FAILED: %v", err)
		res.problem(exitSetupPartial, "update: %v", err)
	} else {
		res.Update = &rep
		step(say, "update", "%s", rep.Title())
		if rep.Failed() {
			res.problem(exitSetupPartial, "update: %s", rep.Title())
		}
	}

	results, err := forgesync.New(a).Sync(ctx)
	if err != nil {
		step(say, "sync", "FAILED: %v", err)
		res.problem(exitSetupPartial, "sync: %v", err)
		return
	}
	for _, r := range results {
		res.Sync = append(res.Sync, setupSync{Project: r.Project, Items: len(r.Items), Reviews: len(r.Reviews), Error: r.Err})
		if r.Err != "" {
			step(say, "sync", "%s: FAILED: %s", r.Project, r.Err)
			res.problem(exitSetupPartial, "sync %s: %s", r.Project, r.Err)
			continue
		}
		step(say, "sync", "%s: %s, %s", r.Project, plural(len(r.Items), "item"), plural(len(r.Reviews), "review"))
	}
}

// unregisteredSources returns the --project sources cfg does not list yet,
// in order and without duplicates, as they will be stored (local paths made
// absolute). A local path and a remote URL are compared as written, after
// resolving local paths the way the config does.
func unregisteredSources(cfg *config.Config, flagged []string) ([]string, error) {
	have := map[string]bool{}
	canon := func(src string) (string, error) {
		if strings.Contains(src, "://") || strings.HasPrefix(src, "git+") {
			return src, nil
		}
		return cfg.ResolvePath(src)
	}
	for _, p := range cfg.Projects {
		c, err := canon(p.Source)
		if err != nil {
			continue // reported by `project add` for that entry
		}
		have[c] = true
	}
	var out []string
	for _, src := range flagged {
		norm, err := project.NormalizeSource(src)
		if err != nil {
			return nil, fmt.Errorf("--project %s: %w", src, err)
		}
		c, err := canon(norm)
		if err != nil {
			return nil, fmt.Errorf("--project %s: %w", src, err)
		}
		if have[c] {
			continue
		}
		have[c] = true
		out = append(out, norm)
	}
	return out, nil
}

// warmShells pre-builds the dev shell of every repo's baseline worktree.
func warmShells(ctx context.Context, say io.Writer, a *app.App, res *setupResult) {
	var dirs []string
	sts, _ := project.New(a, "ade:setup").Status(ctx, "")
	for _, ps := range sts {
		for _, rs := range ps.Repos {
			if rs.Baseline != nil { // named after the repo's default branch
				dirs = append(dirs, rs.Baseline.Path)
			}
		}
	}
	if len(dirs) == 0 {
		step(say, "warm", "no repos under %s", a.Root)
		return
	}
	for _, dir := range dirs {
		w := setupWarm{Dir: dir}
		sh := devshell.Detect(dir, "")
		w.Kind = string(sh.Kind)
		argv := sh.WarmArgv()
		if argv == nil {
			w.Status = "skipped"
			res.Warm = append(res.Warm, w)
			continue
		}
		wctx, cancel := context.WithTimeout(ctx, warmTimeout)
		if sh.Kind == devshell.Direnv {
			// --warm is the request to trust the repo's .envrc; without this
			// direnv exec refuses a blocked file.
			allow := exec.CommandContext(wctx, "direnv", "allow", dir)
			allow.Dir = dir
			_ = allow.Run() // a failure shows up in the exec below
		}
		cmd := exec.CommandContext(wctx, argv[0], argv[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		cancel()
		rel, _ := filepath.Rel(a.Root, dir)
		if err != nil {
			w.Status, w.Error = "failed", strings.TrimSpace(tail(string(out), 300))
			if w.Error == "" {
				w.Error = err.Error()
			}
			step(say, "warm", "%s (%s): FAILED: %s", rel, sh.Kind, w.Error)
			res.problem(exitSetupPartial, "warm %s: %s", rel, w.Error)
		} else {
			w.Status = "warmed"
			step(say, "warm", "%s (%s): ready", rel, string(sh.Kind))
		}
		res.Warm = append(res.Warm, w)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// zellijEnv is the environment for zellij commands: ZELLIJ* variables dropped
// so a setup run from inside a session still manages other sessions.
func zellijEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "ZELLIJ") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// startSession starts the detached zellij session name with the dashboard
// layout unless it is already running. A resurrectable (exited) session is
// resurrected.
func startSession(ctx context.Context, a *app.App, name string) setupSession {
	s := setupSession{Name: name}
	fail := func(err error) setupSession {
		s.Status, s.Error = "failed", err.Error()
		return s
	}
	bin, err := exec.LookPath("zellij")
	if err != nil {
		return fail(errors.New("zellij is not on PATH"))
	}
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	list := exec.CommandContext(ctx, bin, "list-sessions", "--no-formatting")
	list.Env = zellijEnv()
	out, _ := list.Output() // no server yet: an error just means no sessions
	state := zellij.SessionState(string(out), name)
	if state == zellij.Live {
		s.Status = "running"
		return s
	}

	var argv []string
	if cfg := os.Getenv("ZELLIJ_CONFIG_FILE"); cfg == "" {
		// The session outlives this process and is attached from a VS Code
		// terminal, so it gets the unlock-first preset (PLAN 6.7).
		p, err := zellij.WriteUnlockFirst(filepath.Dir(a.Paths.ConfigFile))
		if err != nil {
			return fail(fmt.Errorf("write zellij config: %w", err))
		}
		argv = append(argv, "--config", p)
	}
	if state == zellij.Exited {
		argv = append(argv, "attach", "--create-background", "--force-run-commands", name)
		s.Status = "resurrected"
	} else {
		ade, err := os.Executable()
		if err != nil {
			return fail(fmt.Errorf("locate ade executable: %w", err))
		}
		if err := paths.EnsureDir(a.Paths.CacheDir); err != nil {
			return fail(err)
		}
		layoutFile := filepath.Join(a.Paths.CacheDir, "layout.kdl")
		if err := os.WriteFile(layoutFile, []byte(zellij.Layout(ade)), 0o600); err != nil {
			return fail(fmt.Errorf("write layout: %w", err))
		}
		argv = append(argv, "--layout", layoutFile, "attach", "--create-background", name)
		s.Status = "started"
	}
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Env = zellijEnv()
	if o, err := cmd.CombinedOutput(); err != nil {
		return fail(fmt.Errorf("zellij %s: %w: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(o))))
	}
	return s
}
