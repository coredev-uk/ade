package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/bootstrap"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/onboard"
	"github.com/coredev-uk/ade/internal/startwork"
)

// bootstrapFlags are the answers of `ade project bootstrap`.
type bootstrapFlags struct {
	forge, group, tracker, name, out, match string
	yes, force, publish, dryRun             bool
}

const (
	bootstrapTimeout = 3 * time.Minute
	defaultManifest  = ".ade.toml"
)

func newProjectBootstrapCmd() *cobra.Command {
	var f bootstrapFlags
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Generate a team manifest (.ade.toml) from an existing forge group",
		Long: `Inspects a GitLab group (or a GitHub organisation or user) and writes the
manifest a team lead would otherwise write by hand: the tracker, the repos with
their app values, label patterns (type, app, workflow, weight), the workflow
stage map, iterations or milestones, the weight source, and run tasks found in
each repo's root files (go.mod, package.json, Makefile, Procfile, flake.nix).
It states only what it detects; the rest stays a comment.

Everything is read through the forge API: nothing is cloned. The result is
validated with the same checks as every manifest before anything is written,
and an existing file is never replaced without --force.

  ade project bootstrap --forge work --group acme --yes
  ade project bootstrap --forge work --group acme --tracker planning --out acme.ade.toml
  ade project bootstrap --forge work --group acme --publish --dry-run

--publish opens a merge request on the tracker repo adding .ade.toml (branch
` + bootstrap.PublishBranch + `); running it again updates that same request. This is a forge write: use
--dry-run to see it first.

--forge names an entry of your user config [forges] (it is the manifest's forge
key, so teammates must use the same name); it may be left out when the config
has exactly one. In a terminal the command asks about what it cannot settle;
with --yes it never prompts and fails naming the flag that settles an ambiguity.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runBootstrap(cmd, f) },
	}
	fl := cmd.Flags()
	fl.StringVar(&f.forge, "forge", "", "forge entry of your user config (default: the only one)")
	fl.StringVar(&f.group, "group", "", "GitLab group, or GitHub organisation or user, to inspect")
	fl.StringVar(&f.tracker, "tracker", "", "tracker repository: full path or unique name (default: detected)")
	fl.StringVar(&f.name, "name", "", "project name (default: the group's name)")
	fl.StringVar(&f.out, "out", defaultManifest, `file to write; "-" prints to stdout`)
	fl.StringVar(&f.match, "match", "", "only include repositories whose name or path matches this glob")
	fl.BoolVarP(&f.yes, "yes", "y", false, "never prompt: use flags and detected defaults")
	fl.BoolVar(&f.force, "force", false, "replace an existing --out file (and an existing manifest when publishing)")
	fl.BoolVar(&f.publish, "publish", false, "open a merge request on the tracker repo adding "+defaultManifest+" (forge write)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "write nothing, local or forge; print the manifest and what --publish would do")
	return cmd
}

// bootstrapRepo is one repo of the --json report.
type bootstrapRepo struct {
	Key       string   `json:"key"`
	Path      string   `json:"path"`
	Apps      []string `json:"apps"`
	Tasks     []string `json:"tasks,omitempty"`
	Templates []string `json:"templates,omitempty"`
}

// bootstrapResult is the --json output; human progress goes to stderr then.
type bootstrapResult struct {
	Manifest  string               `json:"manifest"`
	Out       string               `json:"out,omitempty"`
	Written   bool                 `json:"written"`
	DryRun    bool                 `json:"dry_run,omitempty"`
	Name      string               `json:"name"`
	Forge     string               `json:"forge"`
	Tracker   string               `json:"tracker"`
	Repos     []bootstrapRepo      `json:"repos"`
	Skipped   []string             `json:"skipped,omitempty"`
	Notes     []string             `json:"notes,omitempty"`
	Warnings  []config.Issue       `json:"warnings,omitempty"`
	Published *bootstrap.Published `json:"published,omitempty"`
}

func runBootstrap(cmd *cobra.Command, f bootstrapFlags) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), bootstrapTimeout)
	defer cancel()
	a := appFrom(cmd)
	say := cmd.OutOrStdout()
	if jsonOut {
		say = cmd.ErrOrStderr()
	}
	var pr *prompter
	if !f.yes && readerIsTerminal(cmd.InOrStdin()) {
		pr = &prompter{in: bufio.NewReader(cmd.InOrStdin()), out: say}
	}

	forgeName, err := pickBootstrapForge(pr, a.Config, f.forge)
	if err != nil {
		return err
	}
	if f.group == "" {
		if pr == nil {
			return errors.New("--group is required: the GitLab group, or GitHub organisation or user, to inspect")
		}
		if f.group, err = pr.ask("Group to inspect", ""); err != nil || f.group == "" {
			return errors.Join(err, errors.New("no group given"))
		}
	}
	if f.name == "" && pr != nil {
		if f.name, err = pr.ask("Project name", filepath.Base(f.group)); err != nil {
			return err
		}
	}
	toStdout := f.out == "-"
	// Fail fast, before talking to the forge. With --publish an existing file may be the very
	// manifest this run produces (a re-run), so that case is judged once it is generated.
	if !toStdout && !f.dryRun && !f.force && !f.publish {
		if _, err := os.Stat(f.out); err == nil {
			return fmt.Errorf("%s already exists: use --force to replace it, or --out to write elsewhere", f.out)
		}
	}

	fcfg := a.Config.Forges[forgeName]
	bf, err := openBootstrapForge(ctx, forgeName, fcfg)
	if err != nil {
		return err
	}
	opts := bootstrap.Options{ForgeName: forgeName, Group: f.group, Tracker: f.tracker, Name: f.name, Match: f.match}
	if pr != nil {
		opts.Chooser = promptChooser{pr}
	}
	fmt.Fprintf(say, "inspecting %s on forge %q\n", f.group, forgeName)
	draft, err := bootstrap.Discover(ctx, bf, opts)
	if err != nil {
		return err
	}
	issues, err := draft.Check(a.Config)
	if err != nil {
		return fmt.Errorf("the generated manifest does not parse (a bug): %w", err)
	}
	var warnings []config.Issue
	for _, is := range issues {
		if is.Severity == config.SeverityError {
			return fmt.Errorf("the generated manifest is invalid, so nothing was written: %s (use --name to fix a bad project name)", is)
		}
		warnings = append(warnings, is)
	}

	res := bootstrapResult{
		Manifest: draft.TOML, DryRun: f.dryRun, Name: draft.Manifest.Name, Forge: forgeName,
		Tracker: draft.Manifest.Tracker.Project, Skipped: draft.Skipped, Notes: draft.Notes, Warnings: warnings,
		Repos: []bootstrapRepo{},
	}
	for _, r := range draft.Repos {
		br := bootstrapRepo{Key: r.Key, Path: r.Path, Apps: r.Apps, Templates: r.Templates}
		for _, t := range r.Detected.Tasks {
			br.Tasks = append(br.Tasks, t.Name)
		}
		res.Repos = append(res.Repos, br)
	}
	printBootstrapSummary(say, res)

	switch {
	case toStdout || f.dryRun:
		if !jsonOut {
			fmt.Fprintln(cmd.OutOrStdout(), "\n"+draft.TOML)
		}
	default:
		old, rerr := os.ReadFile(f.out)
		switch {
		case rerr == nil && string(old) == draft.TOML:
			res.Out = f.out
			fmt.Fprintf(say, "\n%s is already this manifest\n", f.out)
		case rerr == nil && !f.force:
			return fmt.Errorf("%s already exists with other content: use --force to replace it, or --out to write elsewhere", f.out)
		default:
			if pr != nil {
				fmt.Fprintln(say, "\n"+draft.TOML)
				if err := confirmOrCancel(pr, "Write "+f.out+"?"); err != nil {
					return err
				}
			}
			if err := writeManifest(f.out, draft.TOML, f.force); err != nil {
				return err
			}
			res.Out, res.Written = f.out, true
			fmt.Fprintf(say, "\nwrote %s\n", f.out)
		}
	}

	if f.publish {
		p, err := publishManifest(ctx, a, bf, fcfg, draft, f, pr, say)
		if err != nil {
			return err
		}
		res.Published = &p
	}

	if jsonOut {
		return printJSON(cmd.OutOrStdout(), res)
	}
	printBootstrapNext(say, fcfg, draft, res, f)
	return nil
}

// confirmOrCancel asks a yes/no question; no is a cancellation.
func confirmOrCancel(pr *prompter, question string) error {
	ok, err := pr.confirm(question)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("cancelled: nothing more was written")
	}
	return nil
}

// publishManifest opens (or finds) the merge request. In a terminal the plan is shown
// and confirmed first, like every forge write; --dry-run only shows it, and leaves one
// audit entry saying so.
func publishManifest(ctx context.Context, a *app.App, bf bootstrap.Forge, fcfg config.Forge, d *bootstrap.Draft, f bootstrapFlags, pr *prompter, say io.Writer) (bootstrap.Published, error) {
	project, src := d.Tracker.Path, gitSource(fcfg, d.Tracker.Path)
	po := bootstrap.PublishOptions{DryRun: f.dryRun, Force: f.force, Audit: bootstrapAudit(a, project)}
	if pr != nil && !f.dryRun {
		plan := po
		plan.DryRun = true
		pp, err := bootstrap.Publish(ctx, bf, project, []byte(d.TOML), src, plan)
		if err != nil {
			return pp, err
		}
		printPublish(say, pp)
		if pp.Nothing {
			return pp, nil
		}
		if err := confirmOrCancel(pr, "Go ahead? This writes to the forge."); err != nil {
			return pp, err
		}
	}
	p, err := bootstrap.Publish(ctx, bf, project, []byte(d.TOML), src, po)
	if f.dryRun && err == nil {
		_ = a.Audit.Append(audit.Entry{
			Action: "forge.mr.create", Target: project + ":" + p.Branch, Project: project,
			DryRun: true, Outcome: "skipped", Params: map[string]any{"path": p.Path, "base": p.Base},
		})
	}
	if err != nil {
		return p, err
	}
	printPublish(say, p)
	return p, nil
}

// pickBootstrapForge resolves --forge against the user config: the named
// entry, or the only one, or (in a terminal) the one the person picks.
func pickBootstrapForge(pr *prompter, cfg *config.Config, name string) (string, error) {
	names := make([]string, 0, len(cfg.Forges))
	for n := range cfg.Forges {
		names = append(names, n)
	}
	slices.Sort(names)
	switch {
	case name != "":
		if _, ok := cfg.Forges[name]; !ok {
			return "", fmt.Errorf("forge %q is not in your user config (have: %s); `ade init` adds one", name, orNone(names))
		}
		return name, nil
	case len(names) == 1:
		return names[0], nil
	case len(names) == 0:
		return "", errors.New("no forge in your user config: run `ade init` first")
	case pr == nil:
		return "", fmt.Errorf("several forges in your user config: pass --forge (have: %s)", strings.Join(names, ", "))
	}
	return pr.choose("Forge", names, names[0])
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// openBootstrapForge opens the real driver and checks it can bootstrap.
func openBootstrapForge(ctx context.Context, name string, f config.Forge) (bootstrap.Forge, error) {
	fg, err := startwork.OpenForge(ctx, name, f)
	if err != nil {
		return nil, err
	}
	bf, ok := fg.(bootstrap.Forge)
	if !ok {
		return nil, fmt.Errorf("forge %s (%s) cannot be inspected for a manifest", name, f.Kind)
	}
	return bf, nil
}

// gitSource is the git+<url>#<path> source a teammate registers once the
// manifest is merged into the tracker repo.
func gitSource(f config.Forge, tracker string) string {
	base := strings.TrimRight(f.URL, "/")
	if base == "" {
		base = onboard.PublicURL(f.Kind)
	}
	return "git+" + base + "/" + tracker + ".git#" + bootstrap.ManifestPath
}

// bootstrapAudit records each forge write of --publish.
func bootstrapAudit(a *app.App, project string) func(action, target string, params map[string]any, url string, err error) {
	return func(action, target string, params map[string]any, url string, err error) {
		e := audit.Entry{Action: action, Target: target, Project: project, Params: params, URL: url, Outcome: "ok"}
		if err != nil {
			e.Outcome, e.Error = "error", err.Error()
		}
		// An unwritable audit log must not undo a write that already happened.
		_ = a.Audit.Append(e)
	}
}

// writeManifest writes the file atomically; without force it refuses to
// replace a file that appeared meanwhile.
func writeManifest(out, text string, force bool) error {
	dir := filepath.Dir(out)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if !force {
		f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%s already exists: use --force to replace it", out)
			}
			return err
		}
		_, werr := f.WriteString(text)
		return errors.Join(werr, f.Close())
	}
	tmp, err := os.CreateTemp(dir, ".ade-manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after a successful rename
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), out)
}

func printBootstrapSummary(w io.Writer, r bootstrapResult) {
	fmt.Fprintf(w, "\ntracker   %s\n", r.Tracker)
	for _, repo := range r.Repos {
		tasks := ""
		if len(repo.Tasks) > 0 {
			tasks = "  tasks: " + strings.Join(repo.Tasks, ", ")
		}
		fmt.Fprintf(w, "repo      %-10s %s  app: %s%s\n", repo.Key, repo.Path, strings.Join(repo.Apps, ", "), tasks)
	}
	for _, s := range r.Skipped {
		fmt.Fprintf(w, "skipped   %s\n", s)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "note      %s\n", n)
	}
	for _, is := range r.Warnings {
		fmt.Fprintf(w, "warning   %s\n", is)
	}
}

func printPublish(w io.Writer, p bootstrap.Published) {
	head := "publish"
	if p.DryRun {
		head = "publish (dry run: nothing is written)"
	}
	fmt.Fprintf(w, "\n%s %s\n", head, p.Project)
	for _, s := range p.Steps {
		fmt.Fprintf(w, "  %s\n", s)
	}
	if p.Nothing {
		return
	}
	fmt.Fprintf(w, "  title: %s\n", p.Title)
	if p.MR != nil {
		fmt.Fprintf(w, "  merge request: %s  %s\n", p.MR.Ref, p.MR.WebURL)
	}
	if p.DryRun {
		fmt.Fprintf(w, "  description:\n")
		for line := range strings.SplitSeq(strings.TrimRight(p.Body, "\n"), "\n") {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
}

func printBootstrapNext(w io.Writer, fcfg config.Forge, d *bootstrap.Draft, r bootstrapResult, f bootstrapFlags) {
	fmt.Fprintln(w, "\nnext steps")
	n := 0
	step := func(format string, args ...any) {
		n++
		fmt.Fprintf(w, "  %d. %s\n", n, fmt.Sprintf(format, args...))
	}
	if r.Out != "" {
		step("read the comments in %s and settle the suggestions", r.Out)
		step("try it on this machine:   ade init --project %s   (or: ade project add %s)", r.Out, r.Out)
	}
	switch {
	case r.Published != nil && r.Published.MR != nil:
		step("merge %s so teammates can use it", r.Published.MR.Ref)
	case f.publish:
	default:
		step("share it with the team: add %s to %s, or let ADE open the merge request:", bootstrap.ManifestPath, d.Tracker.Path)
		fmt.Fprintf(w, "       ade project bootstrap --forge %s --group %s%s --publish\n", r.Forge, f.group, repeatedFlags(f))
	}
	step("teammates, once it is on %s:", d.Tracker.Path)
	fmt.Fprintf(w, "       ade init --yes --forge %s --token-env %s --project '%s'\n",
		fcfg.Kind, onboard.DefaultTokenEnv(fcfg.Kind), gitSource(fcfg, d.Tracker.Path))
}

// repeatedFlags renders the flags that must be given again for the same result.
func repeatedFlags(f bootstrapFlags) string {
	var s string
	for _, p := range [][2]string{{"--tracker", f.tracker}, {"--match", f.match}, {"--name", f.name}} {
		if p[1] != "" {
			s += " " + p[0] + " " + shellQuote(p[1])
		}
	}
	return s
}

func shellQuote(s string) string {
	if strings.ContainsAny(s, " \t*?[]{}$'\"\\") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

// promptChooser asks bootstrap's questions with numbered options.
type promptChooser struct{ p *prompter }

func (c promptChooser) Choose(question string, options []string, def string) (string, error) {
	fmt.Fprintf(c.p.out, "\n%s\n", question)
	defIdx := slices.Index(options, def)
	for i, o := range options {
		fmt.Fprintf(c.p.out, "  %d) %s\n", i+1, o)
	}
	for {
		s, err := c.p.ask("Choose", strconv.Itoa(defIdx+1))
		if err != nil {
			return "", err
		}
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(options) {
			return options[n-1], nil
		}
		if i := slices.Index(options, s); i >= 0 {
			return options[i], nil
		}
		fmt.Fprintf(c.p.out, "  enter a number from 1 to %d\n", len(options))
	}
}
