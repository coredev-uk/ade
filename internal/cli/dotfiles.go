package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/dotfiles"
	"github.com/coredev-uk/ade/internal/project"
)

func init() { register(newDotfilesCmd) }

const dotfilesTimeout = 10 * time.Minute

func newDotfilesCmd() *cobra.Command {
	var projectName string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "dotfiles",
		Short: "Apply the team dotfiles of your projects (the repo used with `coder dotfiles`)",
		Long: `Applies the [dotfiles] repository each project's manifest names, like
` + "`coder dotfiles`" + `: the repo is cloned under ADE's data directory (pulled when it
is already there; moved aside and cloned again when the URL changed), then its
install script is run (install.sh, install, bootstrap.sh, bootstrap, setup.sh,
setup, script/install.sh, ..., or the manifest's dotfiles.install). Without a
script, its dotfiles are symlinked into $HOME (an existing file becomes
<name>.bak), unless the manifest sets symlink = false.

The install script is team code and runs with your environment (ADE_PROJECT,
ADE_ROOT and ADE_DOTFILES_DIR are added). ` + "`ade setup`" + ` runs this step too;
` + "`ade project sync`" + ` never does. In a Coder workspace whose own dotfiles step
already applied the same repo, nothing is done.

--dry-run says what would happen and writes nothing.
Exits 1 when any project's dotfiles failed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			specs, err := dotfileSpecs(a, projectName)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			out := w
			if jsonOut {
				out = cmd.ErrOrStderr()
			}
			results := []dotfiles.Result{}
			for _, s := range specs {
				ctx, cancel := context.WithTimeout(cmd.Context(), dotfilesTimeout)
				r := newDotfilesApplier(a, dryRun, out).Apply(ctx, s)
				cancel()
				auditDotfiles(a, "dotfiles.apply", r, dryRun)
				results = append(results, r)
			}
			if jsonOut {
				if err := printJSON(w, results); err != nil {
					return err
				}
			} else {
				if len(results) == 0 {
					fmt.Fprintln(w, "no project has [dotfiles] (add a dotfiles table with repo = ... to the manifest)")
				}
				for _, r := range results {
					fmt.Fprintln(w, dotfilesLine(r))
				}
			}
			var failed []string
			for _, r := range results {
				if r.Failed() {
					failed = append(failed, r.Project+": "+r.Error)
				}
			}
			if len(failed) > 0 {
				return errors.New("dotfiles failed: " + strings.Join(failed, "; "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectName, "project", "", "apply only this project's dotfiles")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would happen and write nothing")
	cmd.AddCommand(newDotfilesStatusCmd())
	return cmd
}

type dotfilesStatus struct {
	Project   string    `json:"project"`
	Repo      string    `json:"repo"`
	Dir       string    `json:"dir"`
	Applied   bool      `json:"applied"`
	ByCoder   bool      `json:"applied_by_coder,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	Method    string    `json:"method,omitempty"`
	AppliedAt time.Time `json:"applied_at,omitzero"`
}

func newDotfilesStatusCmd() *cobra.Command {
	var projectName string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show which project dotfiles are configured and when they were last applied",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			specs, err := dotfileSpecs(a, projectName)
			if err != nil {
				return err
			}
			list := []dotfilesStatus{}
			for _, s := range specs {
				st := dotfilesStatus{Project: s.Project, Repo: s.Repo, Dir: dotfiles.Dir(a.Paths.DataDir, s.Project)}
				if rec, ok := dotfiles.ReadState(a.Paths.DataDir, s.Project); ok {
					st.Applied, st.Commit, st.Method, st.AppliedAt = true, rec.Commit, rec.Method, rec.AppliedAt
				} else if dotfiles.CoderApplied(cmd.Context(), os.Getenv, homeDir(), s.Repo) {
					st.Applied, st.ByCoder = true, true
				}
				list = append(list, st)
			}
			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, list)
			}
			if len(list) == 0 {
				fmt.Fprintln(w, "no project has [dotfiles]")
			}
			for _, st := range list {
				fmt.Fprintf(w, "%s  %s\n", st.Project, st.Repo)
				switch {
				case st.ByCoder:
					fmt.Fprintln(w, "  applied by coder (~/.config/coderv2/dotfiles)")
				case st.Applied:
					fmt.Fprintf(w, "  applied %s via %s at %s\n", shortCommit(st.Commit), st.Method, st.AppliedAt.Local().Format(time.DateTime))
				default:
					fmt.Fprintln(w, "  never applied: run `ade dotfiles`")
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectName, "project", "", "only this project")
	return cmd
}

func shortCommit(c string) string {
	if len(c) > 10 {
		return c[:10]
	}
	if c == "" {
		return "(no commit)"
	}
	return c
}

// dotfileSpecs lists the dotfiles of every cloned project (only name, when
// given) whose manifest has a [dotfiles] repo and which the user did not
// turn off.
func dotfileSpecs(a *app.App, only string) ([]dotfiles.Spec, error) {
	infos, err := project.New(a, "user").List()
	if err != nil {
		return nil, err
	}
	var specs []dotfiles.Spec
	found := only == ""
	for _, info := range infos {
		if info.Name == "" || (only != "" && info.Name != only) {
			continue
		}
		found = true
		m, err := project.LoadManifest(filepath.Join(a.Root, info.Name))
		if err != nil {
			return nil, fmt.Errorf("project %s: %w", info.Name, err)
		}
		if s, ok := dotfiles.Resolve(a.Config, info.Name, m); ok {
			specs = append(specs, s)
		}
	}
	if !found {
		return nil, fmt.Errorf("no cloned project %q (see `ade project list`)", only)
	}
	return specs, nil
}

func newDotfilesApplier(a *app.App, dryRun bool, out io.Writer) dotfiles.Applier {
	svc := project.New(a, "user")
	return dotfiles.Applier{
		DataDir: a.Paths.DataDir, Home: homeDir(), Root: a.Root, DryRun: dryRun, Out: out,
		Git: svc.ClientFor,
	}
}

func dotfilesLine(r dotfiles.Result) string {
	switch {
	case r.Failed():
		return fmt.Sprintf("%s: %s FAILED: %s", r.Project, r.Method, r.Error)
	case r.Status == dotfiles.StatusDryRun:
		return fmt.Sprintf("%s: %s", r.Project, r.Detail)
	case r.Status == dotfiles.StatusAppliedByCoder:
		return fmt.Sprintf("%s: applied by coder (nothing to do)", r.Project)
	case r.Script != "":
		return fmt.Sprintf("%s: %s, ran %s", r.Project, r.Method, r.Script)
	}
	return fmt.Sprintf("%s: %s", r.Project, r.Method)
}

func auditDotfiles(a *app.App, action string, r dotfiles.Result, dryRun bool) {
	e := audit.Entry{
		Action: action, Project: r.Project, Target: r.Repo, DryRun: dryRun, Outcome: "ok",
		Params: map[string]any{"repo": r.Repo, "method": r.Method},
	}
	switch {
	case r.Failed():
		e.Outcome, e.Error = "error", r.Error
	case r.Method == dotfiles.MethodSkipped:
		e.Outcome = "skipped"
	}
	_ = a.Audit.Append(e)
}

// runSetupDotfiles is the setup step that applies every project's dotfiles.
// A failure is a partial run and never stops the steps after it.
func runSetupDotfiles(ctx context.Context, say io.Writer, a *app.App, res *setupResult) {
	if a.Config.Missing {
		return
	}
	specs, err := dotfileSpecs(a, "")
	if err != nil {
		step(say, "dotfiles", "FAILED: %v", err)
		res.problem(exitSetupPartial, "dotfiles: %v", err)
		return
	}
	if len(specs) == 0 {
		step(say, "dotfiles", "none configured")
		return
	}
	for _, s := range specs {
		dctx, cancel := context.WithTimeout(ctx, dotfilesTimeout)
		r := newDotfilesApplier(a, false, say).Apply(dctx, s)
		cancel()
		auditDotfiles(a, "setup.dotfiles", r, false)
		res.Dotfiles = append(res.Dotfiles, r)
		step(say, "dotfiles", "%s", dotfilesLine(r))
		if r.Failed() {
			res.problem(exitSetupPartial, "dotfiles %s: %s", r.Project, r.Error)
		}
	}
}
