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
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/doctor"
	"github.com/coredev-uk/ade/internal/dotfiles"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/host"
	"github.com/coredev-uk/ade/internal/host/zellij"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/onboard"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/project"
)

func init() { register(newSetupCmd) }

// Exit codes of `ade setup`. 0 is a complete run.
const (
	exitSetupFailed  = 1 // nothing usable: bad config, unwritable files
	exitSetupPartial = 3 // set up, but forge steps were skipped or some repos failed
)

const (
	setupEnvPrefix = "ADE_SETUP_"
	setupTimeout   = 30 * time.Second
	warmTimeout    = 20 * time.Minute
)

type setupFlags struct {
	init        initFlags // the same answers as `ade init`
	configFrom  string
	warm        bool
	session     bool
	sessionName string
}

func newSetupCmd() *cobra.Command {
	var f setupFlags
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Non-interactive, idempotent first start of a machine (Coder startup scripts, laptops)",
		Long: `Brings this machine to a working ADE without ever prompting, and is safe and
fast to run on every boot:

  1. config   no user config yet: created like ` + "`ade init --yes`" + ` from flags or
              ADE_SETUP_* variables, or copied from --config-from <path|url>.
              An existing config is validated and never rewritten; a
              --project it does not list yet is registered (appended).
  2. token    every configured forge's token (and read token) is checked. A missing or rejected
              token skips the forge steps (3) and exits 3; with a read token, a write
              token that does not resolve exits 3 but the forge steps still run.
              Setup never waits for input.
  3. projects ` + "`project add`" + ` for each configured project, then ` + "`update`" + `, then ` + "`sync`" + `.
  4. dotfiles each project's [dotfiles] repo (the one used with ` + "`coder dotfiles`" + `) is
              cloned or pulled and its install script run, or its dotfiles are
              symlinked into $HOME. Its install script is team code. A failure
              exits 3 and does not block the steps after it.
  5. machine  writes the zellij unlock-first preset, reports missing tools with the
              nix command to get them, --warm pre-builds each repo's dev shell.
  6. session  --session starts the detached zellij session so a VS Code remote
              terminal or web tty only has to attach.

Every flag can be given as an ADE_SETUP_<FLAG> variable (--token-env as
ADE_SETUP_TOKEN_ENV, --read-token-env as ADE_SETUP_READ_TOKEN_ENV, --project as ADE_SETUP_PROJECT with entries separated by
whitespace), so a Coder template sets the environment instead of a long
command line. Flags win over variables.

Two runs never overlap: a lock in the cache directory makes the second wait.

Exit codes: 0 complete, 3 partial (forge steps skipped, or a repo/project failed),
1 failure (invalid or uncreatable config). A final ` + "`ade doctor`" + ` is informational.`,
		Args: cobra.NoArgs,
		// setup creates the config, so it must not require one.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := applySetupEnv(cmd.Flags(), os.Getenv); err != nil {
				return err
			}
			return runSetup(cmd, f)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.init.kind, "forge", "", "forge kind: gitlab or github")
	fl.StringVar(&f.init.host, "host", "", "forge host or URL (default: gitlab.com / github.com)")
	fl.StringVar(&f.init.tokenEnv, "token-env", "", "environment variable holding the token")
	fl.StringVar(&f.init.tokenCmd, "token-cmd", "", "command that prints the token")
	fl.StringVar(&f.init.readTokenEnv, "read-token-env", "", "environment variable holding a read-only token for syncs, dashboards and agents (writes use --token-env/--token-cmd)")
	fl.StringVar(&f.init.readTokenCmd, "read-token-cmd", "", "command that prints a read-only token for syncs, dashboards and agents (writes use --token-env/--token-cmd)")
	fl.StringVar(&f.init.caFile, "ca-file", "", "PEM bundle of the certificate authority of a self-hosted forge")
	fl.IntVar(&f.init.sshPort, "ssh-port", 0, "ssh port of a self-hosted forge when not 22")
	fl.StringVar(&f.init.gitProtocol, "git-protocol", "", "clone protocol: ssh (default) or https")
	fl.StringVar(&f.init.root, "root", "", "projects root (default ~/ade)")
	fl.StringVar(&f.init.handle, "handle", "", "your forge username (default: asked from the forge)")
	fl.StringArrayVar(&f.init.projects, "project", nil, "project manifest: path, https URL or git+<url>#<path> (repeatable)")
	bindAIFlags(cmd, &f.init.ai)
	fl.StringVar(&f.configFrom, "config-from", "", "copy a team config template (path or https URL) instead of building one from flags")
	fl.BoolVar(&f.warm, "warm", false, "pre-build each repo's dev shell so the first work tab opens fast")
	fl.BoolVar(&f.session, "session", false, "start the detached zellij session with the dashboard if it is not running")
	fl.StringVar(&f.sessionName, "session-name", host.SessionName, "name of the zellij session --session starts")
	return cmd
}

// applySetupEnv fills every flag the command line left alone from its
// ADE_SETUP_<FLAG> variable.
func applySetupEnv(fl *pflag.FlagSet, getenv func(string) string) error {
	var err error
	fl.VisitAll(func(fg *pflag.Flag) {
		if err != nil || fg.Changed {
			return
		}
		v := strings.TrimSpace(getenv(setupEnvPrefix + strings.ToUpper(strings.ReplaceAll(fg.Name, "-", "_"))))
		if v == "" {
			return
		}
		vals := []string{v}
		if fg.Name == "project" {
			vals = onboard.SplitList(v)
		}
		for _, one := range vals {
			if e := fg.Value.Set(one); e != nil {
				err = fmt.Errorf("%s%s=%q: %w", setupEnvPrefix, strings.ToUpper(strings.ReplaceAll(fg.Name, "-", "_")), one, e)
				return
			}
		}
		fg.Changed = true
	})
	return err
}

type setupToken struct {
	Forge string `json:"forge"`
	User  string `json:"user,omitempty"`
	Error string `json:"error,omitempty"`
	// WriteError is set when a forge with a read token has a write token that
	// does not resolve: reads (and the forge steps) carry on, writes fail.
	WriteError string `json:"write_error,omitempty"`
}

type setupSync struct {
	Project string `json:"project"`
	Items   int    `json:"items"`
	Reviews int    `json:"reviews"`
	Error   string `json:"error,omitempty"`
}

type setupWarm struct {
	Dir    string `json:"dir"`
	Kind   string `json:"kind"`
	Status string `json:"status"` // warmed | skipped | failed
	Error  string `json:"error,omitempty"`
}

type setupSession struct {
	Name   string `json:"name"`
	Status string `json:"status"` // started | resurrected | running | skipped | failed
	Error  string `json:"error,omitempty"`
}

// setupResult is the --json output; human progress goes to stderr then.
type setupResult struct {
	Status        string               `json:"status"` // ok | partial | failed
	Config        string               `json:"config"`
	ConfigCreated bool                 `json:"config_created"`
	Tokens        []setupToken         `json:"tokens"`
	Registered    []string             `json:"registered,omitempty"` // --project sources added to an existing config
	Add           []project.Report     `json:"add,omitempty"`
	Update        *project.Report      `json:"update,omitempty"`
	Sync          []setupSync          `json:"sync,omitempty"`
	ZellijConfig  string               `json:"zellij_config,omitempty"`
	Tools         []onboard.ToolStatus `json:"tools"`
	InstallHint   string               `json:"install_hint,omitempty"`
	Dotfiles      []dotfiles.Result    `json:"dotfiles,omitempty"`
	Warm          []setupWarm          `json:"warm,omitempty"`
	Session       *setupSession        `json:"session,omitempty"`
	Doctor        []doctor.Check       `json:"doctor,omitempty"`
	Skipped       []string             `json:"skipped,omitempty"`
	Problems      []string             `json:"problems,omitempty"`
	DurationMS    int64                `json:"duration_ms"`
	exit          int
}

func (r *setupResult) problem(code int, format string, args ...any) {
	r.Problems = append(r.Problems, fmt.Sprintf(format, args...))
	if code == exitSetupFailed || r.exit == 0 {
		r.exit = code
	}
}

func runSetup(cmd *cobra.Command, f setupFlags) error {
	start := time.Now()
	ctx := cmd.Context()
	say := cmd.OutOrStdout()
	if jsonOut {
		say = cmd.ErrOrStderr()
	}
	p, err := paths.Default()
	if err != nil {
		return err
	}

	unlock, err := onboard.Lock(ctx, filepath.Join(p.CacheDir, "setup.lock"), func() {
		fmt.Fprintln(say, "another ade setup is running: waiting for it")
	})
	if err != nil {
		return err
	}
	defer unlock()

	res := &setupResult{Config: p.ConfigFile, Tokens: []setupToken{}}
	fmt.Fprintln(say, "ade setup")

	// 1. config
	created, err := ensureConfig(ctx, say, p, f)
	if err != nil {
		return withCode(exitSetupFailed, err)
	}
	res.ConfigCreated = created.created
	if created.problem != "" {
		res.problem(created.code, "%s", created.problem)
	}

	a, err := app.Load("user")
	if err != nil {
		return withCode(exitSetupFailed, fmt.Errorf("config %s: %w", p.ConfigFile, err))
	}
	// Execute audits the command and closes the app once it is in the context.
	cmd.SetContext(context.WithValue(ctx, ctxKey{}, a))
	step(say, "config", "%s", configStatus(a, created))
	if a.Config.Missing {
		// No config could be created (forge/token details missing, nothing to read a template from).
		res.Skipped = append(res.Skipped, "forge steps: no config")
		return finishSetup(cmd, say, a, f, res, start)
	}
	var invalid []string
	for _, is := range a.Config.Validate() {
		if is.Severity == config.SeverityError {
			invalid = append(invalid, is.Path+": "+is.Msg)
		}
	}
	if len(invalid) > 0 {
		for _, m := range invalid {
			fmt.Fprintf(say, "  invalid: %s\n", m)
		}
		_ = a.Audit.Append(audit.Entry{Action: "setup.run", Outcome: "error", Error: "invalid config: " + strings.Join(invalid, "; ")})
		return withCode(exitSetupFailed, fmt.Errorf("config %s is invalid: %s (fix it, or see `ade config check`)", p.ConfigFile, invalid[0]))
	}

	stop, err := notify.Attach(ctx, a, notify.Channels{})
	if err != nil {
		return withCode(exitSetupFailed, err)
	}
	defer stop()

	// 2. token
	tokensOK := true
	for name, fg := range a.Config.Forges {
		tctx, cancel := context.WithTimeout(ctx, setupTimeout)
		user, terr := checkForgeToken(tctx, a, name, fg)
		cancel()
		t := setupToken{Forge: name, User: user}
		if terr != nil {
			tokensOK = false
			t.Error = terr.Error()
			step(say, "token", "%s: %v", name, terr)
			res.problem(exitSetupPartial, "forge %s: %v", name, terr)
		} else {
			suffix := ""
			if fg.HasReadToken() && !fg.HasWriteToken() {
				suffix = " (read-only)"
			}
			step(say, "token", "%s: authenticated as %s%s", name, user, suffix)
			if fg.HasReadToken() && fg.HasWriteToken() {
				wctx, wcancel := context.WithTimeout(ctx, setupTimeout)
				if _, werr := fg.Token(wctx); werr != nil {
					t.WriteError = werr.Error()
					step(say, "token", "%s: write token: %v (reads work; writes fail until it resolves)", name, werr)
					res.problem(exitSetupPartial, "forge %s: write token: %v", name, werr)
				}
				wcancel()
			}
		}
		res.Tokens = append(res.Tokens, t)
	}
	if len(a.Config.Forges) == 0 {
		tokensOK = false
		res.problem(exitSetupPartial, "no forge configured in %s", p.ConfigFile)
	}

	// 3. projects, update, sync. A --project (ADE_SETUP_PROJECT) the existing
	// config lacks is registered here, as `ade project add` would.
	newSources, err := unregisteredSources(a.Config, f.init.projects)
	if err != nil {
		return withCode(exitSetupFailed, err)
	}
	switch {
	case !tokensOK:
		res.Skipped = append(res.Skipped, "projects", "update", "sync")
		msg := "skipped: token problem above (fix it and rerun ade setup)"
		if len(newSources) > 0 {
			msg += "; not registered yet: " + strings.Join(newSources, ", ")
		}
		step(say, "projects", "%s", msg)
	case len(a.Config.Projects) == 0 && len(newSources) == 0:
		step(say, "projects", "none configured (ade project add <manifest>)")
	default:
		runSetupProjects(ctx, say, a, newSources, res)
	}

	return finishSetup(cmd, say, a, f, res, start)
}

// finishSetup runs the dotfiles and machine steps, doctor, audit and exit
// mapping that every run ends with.
func finishSetup(cmd *cobra.Command, say io.Writer, a *app.App, f setupFlags, res *setupResult, start time.Time) error {
	ctx := cmd.Context()

	// 4. dotfiles: team code, so after the projects that name it and before
	// the machine tools its install script may add.
	runSetupDotfiles(ctx, say, a, res)

	// 5. machine
	if dir := filepath.Dir(a.Paths.ConfigFile); dir != "" {
		if zp, err := zellij.WriteUnlockFirst(dir); err != nil {
			res.problem(exitSetupPartial, "zellij config: %v", err)
		} else {
			res.ZellijConfig = zp
			step(say, "machine", "zellij unlock-first preset %s", zp)
		}
	}
	res.Tools = onboard.CheckTools(exec.LookPath, homeDir())
	lines, nixCmd := onboard.MissingAdvice(res.Tools)
	res.InstallHint = nixCmd
	if len(lines) == 0 {
		step(say, "machine", "tools: all present")
	}
	for _, l := range lines {
		step(say, "machine", "tools: %s", l)
	}
	if nixCmd != "" {
		fmt.Fprintf(say, "  get them with: %s\n", nixCmd)
	}
	if f.warm {
		warmShells(ctx, say, a, res)
	}

	// 6. session
	if f.session {
		s := startSession(ctx, a, f.sessionName)
		res.Session = &s
		if s.Error != "" {
			step(say, "session", "%s: %s", s.Name, s.Error)
			res.problem(exitSetupPartial, "session %s: %s", s.Name, s.Error)
		} else {
			step(say, "session", "%s %s", s.Name, s.Status)
		}
	}

	// 7. doctor: informational, never changes the exit code.
	dctx, cancel := context.WithTimeout(ctx, 2*setupTimeout)
	res.Doctor = doctor.Run(dctx, doctor.Options{ConfigFile: a.Paths.ConfigFile, Config: a.Config, Root: a.Root, Records: a.RecordsDir, DataDir: a.Paths.DataDir})
	cancel()
	nErr, nWarn := 0, 0
	for _, c := range res.Doctor {
		switch c.Status {
		case doctor.Error:
			nErr++
		case doctor.Warn:
			nWarn++
		}
	}
	step(say, "doctor", "%d errors, %d warnings (run `ade doctor` for details)", nErr, nWarn)

	res.DurationMS = time.Since(start).Milliseconds()
	switch res.exit {
	case 0:
		res.Status = "ok"
	case exitSetupPartial:
		res.Status = "partial"
	default:
		res.Status = "failed"
	}
	outcome := "ok"
	if res.exit != 0 {
		outcome = res.Status
	}
	_ = a.Audit.Append(audit.Entry{
		Action: "setup.run", Target: a.Paths.ConfigFile, Outcome: outcome, Error: strings.Join(res.Problems, "; "),
		Params: map[string]any{
			"config_created": res.ConfigCreated, "skipped": res.Skipped, "warm": f.warm, "session": f.session,
			"duration_ms": res.DurationMS,
		},
	})
	fmt.Fprintf(say, "setup %s in %s\n", res.Status, time.Since(start).Round(time.Millisecond))
	if jsonOut {
		if err := printJSON(cmd.OutOrStdout(), res); err != nil {
			return err
		}
	}
	if res.exit != 0 {
		return withCode(res.exit, errors.New(strings.Join(res.Problems, "; ")))
	}
	return nil
}

func step(w io.Writer, name, format string, args ...any) {
	fmt.Fprintf(w, "%-9s %s\n", name, fmt.Sprintf(format, args...))
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// configOutcome is what the config step did.
type configOutcome struct {
	created bool
	from    string // template source, when copied
	problem string // set when a config could not be created
	code    int
}

func configStatus(a *app.App, c configOutcome) string {
	switch {
	case a.Config.Missing:
		return "none (" + c.problem + ")"
	case c.created && c.from != "":
		return "created " + a.Paths.ConfigFile + " from " + c.from
	case c.created:
		return "created " + a.Paths.ConfigFile
	}
	return "exists " + a.Paths.ConfigFile + " (left untouched)"
}

// ensureConfig creates the user config when there is none. A config that
// already exists is never touched. A hard error (unreadable template, bad
// answers) is returned; a missing token that blocks creation is a problem the
// run goes on with.
func ensureConfig(ctx context.Context, say io.Writer, p paths.Paths, f setupFlags) (configOutcome, error) {
	if _, err := os.Stat(p.ConfigFile); err == nil {
		return configOutcome{}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return configOutcome{}, err
	}

	if f.configFrom != "" {
		data, err := onboard.FetchTemplate(ctx, f.configFrom)
		if err != nil {
			return configOutcome{}, fmt.Errorf("--config-from %s: %w", f.configFrom, err)
		}
		if err := checkTemplate(data); err != nil {
			return configOutcome{}, fmt.Errorf("--config-from %s: %w", f.configFrom, err)
		}
		if _, err := onboard.WriteConfig(p.ConfigFile, data, false, time.Now()); err != nil {
			return configOutcome{}, err
		}
		return configOutcome{created: true, from: f.configFrom}, nil
	}

	in := f.init
	in.yes = true
	ans, sources, err := gather(nil, in)
	if err != nil {
		if in.kind == "" && in.tokenEnv == "" && in.tokenCmd == "" && in.readTokenEnv == "" && in.readTokenCmd == "" && len(in.projects) == 0 {
			return configOutcome{
				problem: "no config and nothing to create one from: set ADE_SETUP_FORGE, ADE_SETUP_TOKEN_ENV and ADE_SETUP_PROJECT (or pass --config-from)",
				code:    exitSetupFailed,
			}, nil
		}
		return configOutcome{}, err
	}
	if ans.AI, err = gatherAI(ctx, nil, in.ai, say); err != nil {
		return configOutcome{}, err
	}
	probe := config.Defaults()
	probe.Forges = map[string]config.Forge{ans.Kind: ans.Forge()}
	probeApp := &app.App{Paths: p, Config: &probe}
	var named []string
	manifests := make([]*config.Manifest, len(sources))
	for i, src := range sources {
		fctx, cancel := context.WithTimeout(ctx, setupTimeout)
		m, merr := project.FetchManifest(fctx, probeApp, config.ProjectRef{Source: src})
		cancel()
		if merr != nil {
			// Without the manifest the forge key is unknown, so no config is written;
			// the next run tries again.
			return configOutcome{
				problem: fmt.Sprintf("config not created: manifest %s: %v", src, merr),
				code:    exitSetupPartial,
			}, nil
		}
		manifests[i] = m
		named = append(named, m.Forge)
	}
	if ans.ForgeName, err = onboard.ForgeName(named, ans.Kind); err != nil {
		return configOutcome{}, err
	}
	if err := ans.Validate(); err != nil {
		return configOutcome{}, err
	}
	final := config.Defaults()
	final.Forges = map[string]config.Forge{ans.ForgeName: ans.Forge()}
	for i, m := range manifests {
		for _, is := range m.Validate(&final) {
			if is.Severity == config.SeverityError {
				return configOutcome{}, fmt.Errorf("manifest %s is invalid: %s: %s", sources[i], is.Path, is.Msg)
			}
		}
	}

	// The handle comes from the forge when the token works; a bad token does
	// not stop the config being written, it is reported by the token step.
	ans.Handle = in.handle
	if ans.Handle == "" {
		tctx, cancel := context.WithTimeout(ctx, setupTimeout)
		probeApp.Config = &final
		if fg, oerr := forgesync.New(probeApp).Open(tctx, ans.ForgeName, ans.Forge()); oerr == nil {
			if me, merr := fg.Me(tctx); merr == nil {
				ans.Handle = me.Username
			}
		}
		cancel()
	}
	data, err := onboard.Render(ans)
	if err != nil {
		return configOutcome{}, err
	}
	// Registered here, as `project add` would, so a run that cannot clone yet
	// (token problem) still leaves a config the next run completes.
	var b strings.Builder
	b.Write(data)
	for _, src := range sources {
		fmt.Fprintf(&b, "\n[[projects]]\nsource = %q\n", src)
	}
	if _, err := onboard.WriteConfig(p.ConfigFile, []byte(b.String()), false, time.Now()); err != nil {
		return configOutcome{}, err
	}
	return configOutcome{created: true}, nil
}
