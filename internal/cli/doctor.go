package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/doctor"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/project"
)

func init() { register(newDoctorCmd) }

type loadErrKey struct{}

// lenientPreRun replaces the root's PersistentPreRunE for commands whose job is
// to explain a broken setup: a config that fails to load must not stop them.
// The failure is kept in the context for loadError.
func lenientPreRun(cmd *cobra.Command, _ []string) error {
	a, err := app.Load("user")
	if err != nil {
		cmd.SetContext(context.WithValue(cmd.Context(), loadErrKey{}, err))
		return nil
	}
	cmd.SetContext(context.WithValue(cmd.Context(), ctxKey{}, a))
	return nil
}

// loadError returns why lenientPreRun could not build the App, if it could not.
func loadError(cmd *cobra.Command) error {
	err, _ := cmd.Context().Value(loadErrKey{}).(error)
	return err
}

func newDoctorCmd() *cobra.Command {
	var probe, vscode bool
	cmd := &cobra.Command{
		Use:               "doctor",
		Short:             "Check config, tools, credentials and host capabilities",
		Args:              cobra.NoArgs,
		PersistentPreRunE: lenientPreRun,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := doctorOptions(cmd)
			checks := doctor.Run(cmd.Context(), opts)
			if vscode {
				cwd, _ := os.Getwd()
				checks = append(checks, doctor.VSCode(opts, doctor.VSCodeOptions{Cwd: cwd})...)
			}
			var reports []doctor.ProbeReport
			if probe {
				reports = doctor.Probe(cmd.Context(), opts, probeTargets(cmd.Context(), appFrom(cmd)))
			}
			w := cmd.OutOrStdout()
			switch {
			case jsonOut && (probe || vscode):
				out := struct {
					Checks   []doctor.Check       `json:"checks"`
					Probe    []doctor.ProbeReport `json:"probe,omitempty"`
					Settings map[string]any       `json:"vscode_settings,omitempty"`
				}{Checks: checks, Probe: reports}
				if vscode {
					out.Settings = doctor.VSCodeSettings
				}
				if err := printJSON(w, out); err != nil {
					return err
				}
			case jsonOut:
				if err := printJSON(w, checks); err != nil {
					return err
				}
			default:
				printChecks(w, checks)
				if probe {
					printProbe(w, reports, appFrom(cmd) != nil)
				}
				if vscode {
					fmt.Fprintf(w, "\nrecommended VS Code settings (merge into settings.json; entries starting with - release a key to the terminal)\n%s", doctor.VSCodeSnippet())
				}
			}
			if doctor.HasErrors(checks) || slices.ContainsFunc(reports, doctor.ProbeReport.Failed) {
				return withCode(1, errors.New("doctor found problems"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&probe, "probe", false,
		"also call the read-only endpoints ADE depends on and report their shape (status, field presence), never values")
	cmd.Flags().BoolVar(&vscode, "vscode", false,
		"also check the VS Code integration (code launcher, tasks.json, terminal key settings) and print the recommended settings snippet")
	return cmd
}

// probeTargets resolves the tracker project of every registered manifest. A
// manifest that cannot be fetched becomes a target carrying the reason, so one
// broken source does not hide the others.
func probeTargets(ctx context.Context, a *app.App) []doctor.ProbeTarget {
	if a == nil {
		return nil
	}
	var out []doctor.ProbeTarget
	for _, ref := range a.Config.Projects {
		m, err := project.FetchManifest(ctx, a, ref)
		if err != nil {
			out = append(out, doctor.ProbeTarget{Manifest: ref.Source, Err: err.Error()})
			continue
		}
		t := doctor.ProbeTarget{Manifest: m.Name, Forge: m.Forge, Project: m.Tracker.Project}
		if t.Project == "" {
			t.Err = "the manifest has no tracker.project"
		}
		for _, name := range slices.Sorted(maps.Keys(m.Repos)) {
			t.Repos = append(t.Repos, m.Repos[name].Path)
		}
		out = append(out, t)
	}
	return out
}

func doctorOptions(cmd *cobra.Command) doctor.Options {
	if a := appFrom(cmd); a != nil {
		return doctor.Options{ConfigFile: a.Paths.ConfigFile, Config: a.Config, Root: a.Root, Records: a.RecordsDir}
	}
	o := doctor.Options{ConfigErr: loadError(cmd)}
	if p, err := paths.Default(); err == nil {
		o.ConfigFile = p.ConfigFile
	}
	return o
}

func printChecks(w io.Writer, checks []doctor.Check) {
	var counts [3]int
	group := ""
	for _, c := range checks {
		if c.Group != group {
			group = c.Group
			fmt.Fprintf(w, "\n%s\n", group)
		}
		fmt.Fprintf(w, "  %-5s %s", c.Status, c.Name)
		if c.Detail != "" {
			fmt.Fprintf(w, ": %s", c.Detail)
		}
		fmt.Fprintln(w)
		if c.Fix != "" && c.Status != doctor.OK {
			fmt.Fprintf(w, "        fix: %s\n", c.Fix)
		}
		switch c.Status {
		case doctor.OK:
			counts[0]++
		case doctor.Warn:
			counts[1]++
		default:
			counts[2]++
		}
	}
	fmt.Fprintf(w, "\n%s\n", strings.Join([]string{
		fmt.Sprintf("%d ok", counts[0]), fmt.Sprintf("%d warnings", counts[1]), fmt.Sprintf("%d errors", counts[2]),
	}, ", "))
}

// printProbe renders the shape probe: one block per tracker, one line per
// endpoint with its status and `field:shape` pairs (shapes, never values).
func printProbe(w io.Writer, reports []doctor.ProbeReport, configLoaded bool) {
	fmt.Fprintf(w, "\nprobe (shape only: status codes and field presence, no values)\n")
	if !configLoaded {
		fmt.Fprintln(w, "  skipped: the config did not load")
		return
	}
	if len(reports) == 0 {
		fmt.Fprintln(w, "  no registered projects")
		return
	}
	for _, r := range reports {
		fmt.Fprintf(w, "  %s", r.Manifest)
		if r.Forge != "" {
			fmt.Fprintf(w, " on %s", r.Forge)
		}
		if r.Project != "" {
			fmt.Fprintf(w, " (%s)", r.Project)
		}
		fmt.Fprintln(w)
		switch {
		case r.Err != "":
			fmt.Fprintf(w, "    error: %s\n", r.Err)
		case r.Skipped != "":
			fmt.Fprintf(w, "    skipped: %s\n", r.Skipped)
		}
		width := 0
		for _, e := range r.Endpoints {
			width = max(width, len(e.Name))
		}
		for _, e := range r.Endpoints {
			printEndpoint(w, e, width)
		}
		printCaps(w, r)
	}
}

func printEndpoint(w io.Writer, e doctor.Endpoint, width int) {
	status := "---"
	if e.Status != 0 {
		status = fmt.Sprint(e.Status)
	}
	fmt.Fprintf(w, "    %-*s  %s  %s\n", width, e.Name, status, e.Path)
	if len(e.Fields) > 0 {
		pairs := make([]string, len(e.Fields))
		for i, f := range e.Fields {
			pairs[i] = f.Name + ":" + f.Shape
		}
		fmt.Fprintf(w, "      fields: %s\n", strings.Join(pairs, " "))
	}
	if len(e.Headers) > 0 {
		fmt.Fprintf(w, "      headers: %s\n", strings.Join(e.Headers, " "))
	}
	for _, n := range e.Notes {
		fmt.Fprintf(w, "      note: %s\n", n)
	}
	if e.Error != "" {
		fmt.Fprintf(w, "      error: %s\n", e.Error)
	}
}

func printCaps(w io.Writer, r doctor.ProbeReport) {
	switch {
	case r.CapsError != "":
		fmt.Fprintf(w, "    capabilities: error: %s\n", r.CapsError)
	case r.Caps != nil:
		c := r.Caps
		yn := func(b bool) string {
			if b {
				return "yes"
			}
			return "no"
		}
		fmt.Fprintf(w, "    capabilities: iterations=%s cadences=%s weight=%s approval_rules=%s blocks_links=%s epics=%s\n",
			yn(c.Iterations), yn(c.Cadences), yn(c.Weight), yn(c.ApprovalRules), yn(c.BlocksLinks), yn(c.Epics))
	}
}
