package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/doctor"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/onboard"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/tlsca"
)

func init() { register(newInitCmd) }

// initFlags are the scriptable answers. In a terminal, whatever is missing is
// asked for; with --yes, whatever is missing is defaulted or an error.
type initFlags struct {
	yes, force  bool
	kind        string
	host        string
	tokenEnv    string
	tokenCmd    string
	gitProtocol string
	root        string
	handle      string
	projects    []string
	caFile      string
	sshPort     int
	ai          aiFlags
}

func newInitCmd() *cobra.Command {
	var f initFlags
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up ADE on this machine: config, token check, projects, first sync",
		Long: `Walks a new teammate from zero to a populated dashboard:

  1. writes the user config (forge, token source, projects root, your handle)
  2. verifies the token against the forge
  3. registers the project manifest(s): clones every repo with its worktrees
  4. runs the first sync and ` + "`ade doctor`" + `
  5. prints what to do next

AI through a LiteLLM gateway is optional: --ai-gateway <url> with --ai-key-env
and --ai-model-explain/-classify/-draft/-review writes a gateway profile (in a
terminal the models are chosen from the gateway's /v1/models list). Classify
is the small, cheap model for label suggestions; it falls back to explain.

Run it in a terminal to be asked for each answer, or script it with --yes:

  ade init --yes --forge gitlab --host gitlab.example.com --token-env GITLAB_TOKEN \
      --project git+https://gitlab.example.com/acme/tracker.git#.ade.toml

The config lives at $ADE_CONFIG or $XDG_CONFIG_HOME/ade/config.toml (default
~/.config/ade/config.toml). An existing config is never replaced without
--force, and then it is copied to config.toml.bak-<timestamp> first. Tokens are
never written: the config stores an environment variable name or a command.

Exits 2 when a project failed to clone or sync, 1 when ` + "`ade doctor`" + ` reports errors.`,
		Args: cobra.NoArgs,
		// init creates the config, so it must not require one.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE:              func(cmd *cobra.Command, _ []string) error { return runInit(cmd, f) },
	}
	fl := cmd.Flags()
	fl.BoolVarP(&f.yes, "yes", "y", false, "never prompt: use flags and defaults")
	fl.BoolVar(&f.force, "force", false, "replace an existing config (a backup is kept beside it)")
	fl.StringVar(&f.kind, "forge", "", "forge kind: gitlab or github")
	fl.StringVar(&f.host, "host", "", "forge host or URL (default: gitlab.com / github.com)")
	fl.StringVar(&f.caFile, "ca-file", "", "PEM bundle of the certificate authority of a self-hosted forge (also: export SSL_CERT_FILE)")
	fl.IntVar(&f.sshPort, "ssh-port", 0, "ssh port of a self-hosted forge when not 22 (clone URLs become ssh://git@host:port/...)")
	fl.StringVar(&f.tokenEnv, "token-env", "", "environment variable holding the token")
	fl.StringVar(&f.tokenCmd, "token-cmd", "", "command that prints the token (e.g. \"gh auth token\")")
	fl.StringVar(&f.gitProtocol, "git-protocol", "", "clone protocol: ssh (default) or https")
	fl.StringVar(&f.root, "root", "", "projects root (default ~/ade)")
	fl.StringVar(&f.handle, "handle", "", "your forge username (default: asked from the forge)")
	bindAIFlags(cmd, &f.ai)
	fl.StringArrayVar(&f.projects, "project", nil, "project manifest: path, https URL or git+<url>#<path> (repeatable)")
	return cmd
}

// initResult is the --json output; human progress goes to stderr then.
type initResult struct {
	Config   string             `json:"config"`
	Backup   string             `json:"backup,omitempty"`
	Forge    string             `json:"forge"`
	Handle   string             `json:"handle"`
	Projects []string           `json:"projects"`
	Clone    []project.Report   `json:"clone"`
	Sync     []forgesync.Result `json:"sync"`
	Doctor   []doctor.Check     `json:"doctor"`
	AI       *initAIResult      `json:"ai,omitempty"`
	Problems []string           `json:"problems,omitempty"`
}

const initTimeout = 30 * time.Second

func runInit(cmd *cobra.Command, f initFlags) error {
	ctx := cmd.Context()
	say := cmd.OutOrStdout()
	if jsonOut {
		say = cmd.ErrOrStderr()
	}

	p, err := paths.Default()
	if err != nil {
		return err
	}
	interactive := !f.yes && readerIsTerminal(cmd.InOrStdin())
	if !f.yes && !interactive {
		return errors.New("ade init asks questions and stdin is not a terminal: run it in a terminal, or pass --yes with --forge, a token source (--token-env or --token-cmd) and --project")
	}
	if _, err := os.Stat(p.ConfigFile); err == nil && !f.force {
		return fmt.Errorf("%w: %s\nuse --force to replace it (a backup is kept), or `ade project add <manifest>` to add a project to it",
			onboard.ErrExists, p.ConfigFile)
	}

	var pr *prompter
	if interactive {
		pr = &prompter{in: bufio.NewReader(cmd.InOrStdin()), out: say}
		fmt.Fprintf(say, "ade init: config will be written to %s\n\n", p.ConfigFile)
	}
	ans, sources, err := gather(pr, f)
	if err != nil {
		return err
	}

	// Manifests name the forge key they expect; read them before the config
	// exists, through a throwaway in-memory config so private https/git
	// sources can authenticate.
	probe := config.Defaults()
	probe.Forges = map[string]config.Forge{ans.Kind: ans.Forge()}
	probeApp := &app.App{Paths: p, Config: &probe}
	var named []string
	manifests := make([]*config.Manifest, len(sources))
	for i, src := range sources {
		fctx, cancel := context.WithTimeout(ctx, initTimeout)
		m, err := project.FetchManifest(fctx, probeApp, config.ProjectRef{Source: src})
		cancel()
		if err != nil {
			return fmt.Errorf("manifest %s: %w", src, err)
		}
		manifests[i] = m
		named = append(named, m.Forge)
	}
	if ans.ForgeName, err = onboard.ForgeName(named, ans.Kind); err != nil {
		return err
	}
	if err := ans.Validate(); err != nil {
		return err
	}
	final := config.Defaults()
	final.Forges = map[string]config.Forge{ans.ForgeName: ans.Forge()}
	for i, m := range manifests {
		for _, is := range m.Validate(&final) {
			if is.Severity == config.SeverityError {
				return fmt.Errorf("manifest %s is invalid: %s: %s", sources[i], is.Path, is.Msg)
			}
		}
	}

	// A token that does not work is found out here, before any file is written.
	vctx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	probeApp.Config = &final
	fg, err := forgesync.New(probeApp).Open(vctx, ans.ForgeName, ans.Forge())
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	me, err := fg.Me(vctx)
	if err != nil {
		return fmt.Errorf("the forge rejected the token: %w", err)
	}
	fmt.Fprintf(say, "token ok: authenticated as %s on %s\n", me.Username, hostLabel(ans))
	ans.Handle = f.handle
	if ans.Handle == "" {
		ans.Handle = me.Username
		if pr != nil {
			if ans.Handle, err = pr.ask("Your handle", me.Username); err != nil {
				return err
			}
		}
	}
	if ans.AI, err = gatherAI(ctx, pr, f.ai, say); err != nil {
		return err
	}
	if pr != nil {
		ok, err := pr.confirm(fmt.Sprintf("Write %s and add %s?", p.ConfigFile, plural(len(sources), "project")))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cancelled: nothing was written")
		}
	}

	data, err := onboard.Render(ans)
	if err != nil {
		return err
	}
	backup, err := onboard.WriteConfig(p.ConfigFile, data, f.force, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(say, "wrote %s\n", p.ConfigFile)
	if backup != "" {
		fmt.Fprintf(say, "previous config saved as %s\n", backup)
	}

	a, err := app.Load("user")
	if err != nil {
		return fmt.Errorf("load the new config: %w", err)
	}
	// Execute audits the command and closes the app once it is in the context.
	cmd.SetContext(context.WithValue(ctx, ctxKey{}, a))

	res := initResult{Config: p.ConfigFile, Backup: backup, Forge: ans.ForgeName, Handle: ans.Handle, Projects: sources}
	if g := ans.AI; g != nil {
		res.AI = &initAIResult{Profile: g.ProfileName(), BaseURL: g.BaseURL, Models: g.Models}
		fmt.Fprintf(say, "AI profile %s: %s\n", g.ProfileName(), g.BaseURL)
		initAIEnvHint(say, g)
	}
	stop, err := notify.Attach(ctx, a, notify.Channels{})
	if err != nil {
		return err
	}
	exit := 0
	problem := func(code int, format string, args ...any) {
		res.Problems = append(res.Problems, fmt.Sprintf(format, args...))
		if code > exit && exit != 2 { // a failed clone or sync outranks doctor
			exit = code
		}
	}

	svc := project.New(a, "user")
	for _, src := range sources {
		fmt.Fprintf(say, "\nadding %s\n", src)
		rep, err := svc.Add(ctx, src)
		if err != nil {
			problem(2, "add %s: %v", src, err)
			fmt.Fprintf(say, "  FAILED: %v\n", err)
			continue
		}
		printRepoResults(say, rep)
		res.Clone = append(res.Clone, rep)
		if rep.Failed() {
			problem(2, "%s", rep.Title())
		}
	}

	if len(a.Config.Projects) > 0 {
		fmt.Fprintln(say, "\nfirst sync")
		results, err := forgesync.New(a).Sync(ctx)
		if err != nil {
			problem(2, "sync: %v", err)
		}
		stop()
		res.Sync = results
		printSync(say, results)
		if n := countFailed(results); n > 0 {
			problem(2, "%d of %d projects failed to sync", n, len(results))
		}
	} else {
		stop()
	}

	fmt.Fprintln(say, "\nade doctor")
	checks := doctor.Run(ctx, doctor.Options{ConfigFile: a.Paths.ConfigFile, Config: a.Config, Root: a.Root, Records: a.RecordsDir})
	res.Doctor = checks
	printChecks(say, checks)
	if doctor.HasErrors(checks) {
		problem(1, "doctor found problems")
	}

	printNextSteps(say, len(a.Config.Projects) > 0)
	if jsonOut {
		if res.Projects == nil {
			res.Projects = []string{}
		}
		if err := printJSON(cmd.OutOrStdout(), res); err != nil {
			return err
		}
	}
	if exit != 0 {
		return withCode(exit, errors.New(strings.Join(res.Problems, "; ")))
	}
	return nil
}

func hostLabel(a onboard.Answers) string {
	if a.URL == "" {
		return strings.TrimPrefix(onboard.PublicURL(a.Kind), "https://")
	}
	return a.URL
}

func printNextSteps(w io.Writer, haveProjects bool) {
	fmt.Fprintln(w, "\nnext steps")
	if !haveProjects {
		fmt.Fprintln(w, "  ade project add <manifest>   register your team's project (see docs/manifest.md)")
	}
	fmt.Fprintln(w, "  ade                          open the dashboard")
	fmt.Fprintln(w, "  ade items                    your work items (--json for scripts)")
	fmt.Fprintln(w, "  ade update                   fetch every repo and fast-forward clean trees")
	fmt.Fprintln(w, "  ade doctor                   re-check tools and credentials any time")
}

// gather resolves the answers and manifest sources: flags first, then the
// prompts when pr is set, then defaults. With pr == nil a missing required
// answer is an error naming its flag.
func gather(pr *prompter, f initFlags) (onboard.Answers, []string, error) {
	var a onboard.Answers
	var err error

	a.Kind = f.kind
	if a.Kind == "" {
		if pr == nil {
			return a, nil, errors.New("--forge is required (gitlab or github)")
		}
		if a.Kind, err = pr.choose("Forge", []string{"gitlab", "github"}, "gitlab"); err != nil {
			return a, nil, err
		}
	}
	if a.Kind != "gitlab" && a.Kind != "github" {
		return a, nil, fmt.Errorf("--forge %q: want gitlab or github", a.Kind)
	}

	host := f.host
	if host == "" && pr != nil {
		def := strings.TrimPrefix(onboard.PublicURL(a.Kind), "https://")
		if host, err = pr.ask("Host (self-hosted: gitlab.example.com)", def); err != nil {
			return a, nil, err
		}
	}
	if a.URL, err = onboard.NormalizeHost(a.Kind, host); err != nil {
		return a, nil, err
	}

	a.TokenEnv, a.TokenCmd = f.tokenEnv, f.tokenCmd
	if a.TokenEnv == "" && a.TokenCmd == "" {
		def := defaultTokenSource(a.Kind)
		src := def
		if pr != nil {
			if src, err = pr.ask("Token source: an environment variable name, or !command (the token itself is never stored)", def); err != nil {
				return a, nil, err
			}
		} else if src == "" {
			return a, nil, fmt.Errorf("no token source: pass --token-env (e.g. %s) or --token-cmd", onboard.DefaultTokenEnv(a.Kind))
		}
		if a.TokenEnv, a.TokenCmd, err = onboard.ParseTokenSource(src); err != nil {
			return a, nil, err
		}
	}

	if a.GitProtocol = f.gitProtocol; a.GitProtocol == "" {
		a.GitProtocol = "ssh"
		if pr != nil {
			if a.GitProtocol, err = pr.choose("Clone over", []string{"ssh", "https"}, "ssh"); err != nil {
				return a, nil, err
			}
		}
	}

	// A self-hosted forge may sit behind an internal CA and listen for ssh on
	// another port; neither is asked for gitlab.com or github.com.
	a.SSHPort = f.sshPort
	if a.CAFile = f.caFile; a.URL != "" {
		if a.CAFile == "" && pr != nil && f.host == "" {
			if a.CAFile, err = pr.ask("CA bundle (PEM) of the forge, blank to use the system certificates", ""); err != nil {
				return a, nil, err
			}
		}
		if a.SSHPort == 0 && pr != nil && f.host == "" && a.GitProtocol == "ssh" {
			p, err := pr.ask("SSH port of the forge", "22")
			if err != nil {
				return a, nil, err
			}
			if a.SSHPort, err = strconv.Atoi(strings.TrimSpace(p)); err != nil {
				return a, nil, fmt.Errorf("ssh port %q is not a number", p)
			}
			if a.SSHPort == 22 {
				a.SSHPort = 0
			}
		}
	}
	if a.CAFile != "" {
		p, err := paths.Expand(a.CAFile)
		if err == nil {
			p, err = filepath.Abs(p)
		}
		if err != nil {
			return a, nil, fmt.Errorf("--ca-file %q: %w", a.CAFile, err)
		}
		if _, err := tlsca.Pool(p); err != nil {
			return a, nil, err
		}
		a.CAFile = p
	}

	sources := append([]string(nil), f.projects...)
	if len(sources) == 0 && pr != nil {
		for {
			s, err := pr.ask("Project manifest (path, https URL or git+<url>#<path>; blank to finish)", "")
			if err != nil {
				return a, nil, err
			}
			if s == "" {
				break
			}
			sources = append(sources, s)
		}
	}
	for i, s := range sources {
		if sources[i], err = project.NormalizeSource(s); err != nil {
			return a, nil, err
		}
	}

	if a.Root = f.root; a.Root == "" {
		a.Root = "~/ade"
		if pr != nil {
			if a.Root, err = pr.ask("Projects root", a.Root); err != nil {
				return a, nil, err
			}
		}
	}
	return a, sources, nil
}

// defaultTokenSource prefers an already exported conventional variable, then
// `gh auth token` for GitHub; otherwise the conventional variable name is
// offered as the answer to type in, and "" when running unattended.
func defaultTokenSource(kind string) string {
	env := onboard.DefaultTokenEnv(kind)
	if strings.TrimSpace(os.Getenv(env)) != "" {
		return env
	}
	if kind == "github" {
		if _, err := exec.LookPath("gh"); err == nil {
			return "!gh auth token"
		}
	}
	return ""
}

// readerIsTerminal reports whether r is an interactive terminal.
func readerIsTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && isTerminal(f)
}

// prompter asks line-based questions; an answered default is the empty line.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func (p *prompter) line() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || s == "") {
		return "", fmt.Errorf("input ended before the questions were answered: %w", err)
	}
	return strings.TrimSpace(s), nil
}

func (p *prompter) ask(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	s, err := p.line()
	if err != nil {
		return "", err
	}
	if s == "" {
		return def, nil
	}
	return s, nil
}

func (p *prompter) choose(label string, options []string, def string) (string, error) {
	for {
		s, err := p.ask(label+" ("+strings.Join(options, "/")+")", def)
		if err != nil {
			return "", err
		}
		for _, o := range options {
			if strings.EqualFold(s, o) {
				return o, nil
			}
		}
		fmt.Fprintf(p.out, "  choose one of: %s\n", strings.Join(options, ", "))
	}
}

func (p *prompter) confirm(label string) (bool, error) {
	for {
		s, err := p.ask(label+" (y/n)", "y")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}
