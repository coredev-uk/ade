package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/project"
)

func init() { register(newConfigCmd) }

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "config",
		Short:             "Inspect and validate configuration",
		PersistentPreRunE: lenientPreRun,
	}
	cmd.AddCommand(newConfigCheckCmd())
	return cmd
}

// fileReport is the validation result for one file.
type fileReport struct {
	File   string         `json:"file"`
	Kind   string         `json:"kind"` // config | manifest
	Issues []config.Issue `json:"issues"`
}

func newConfigCheckCmd() *cobra.Command {
	var manifests []string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate the user config and project manifests",
		Long: "Validates the user config and the manifest of every registered project.\n" +
			"With --manifest, validates only those files (no fetching). Exits 1 on any error.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reports := checkConfig(cmd, manifests)
			w := cmd.OutOrStdout()
			if jsonOut {
				if err := printJSON(w, reports); err != nil {
					return err
				}
			} else {
				printReports(w, reports)
			}
			for _, r := range reports {
				if config.HasErrors(r.Issues) {
					return withCode(1, errors.New("configuration has errors"))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&manifests, "manifest", nil, "validate this manifest file instead of the registered projects (repeatable)")
	return cmd
}

func checkConfig(cmd *cobra.Command, files []string) []fileReport {
	a := appFrom(cmd)
	var cfg *config.Config
	if a != nil {
		cfg = a.Config
	}
	var reports []fileReport
	if len(files) == 0 {
		reports = append(reports, configReport(cmd, a))
	}
	var ms []*config.Manifest
	add := func(label string, m *config.Manifest, err error) {
		if err != nil {
			reports = append(reports, fileReport{label, "manifest", []config.Issue{{Severity: config.SeverityError, Msg: err.Error()}}})
			return
		}
		if m.Source == "" {
			m.Source = label
		}
		ms = append(ms, m)
	}
	if len(files) > 0 {
		for _, f := range files {
			m, err := readManifestFile(f)
			add(f, m, err)
		}
	} else if a != nil {
		for _, ref := range cfg.Projects {
			m, err := project.FetchManifest(cmd.Context(), a, ref)
			add(ref.Source, m, err)
		}
	}
	byFile := config.ValidateAll(ms, cfg)
	seen := map[string]bool{}
	for _, m := range ms {
		if seen[m.Source] {
			continue
		}
		seen[m.Source] = true
		reports = append(reports, fileReport{m.Source, "manifest", nonNil(byFile[m.Source])})
	}
	return reports
}

func configReport(cmd *cobra.Command, a *app.App) fileReport {
	if a == nil {
		file := ""
		if o := doctorOptions(cmd); o.ConfigFile != "" {
			file = o.ConfigFile
		}
		msg := "cannot load config"
		if err := loadError(cmd); err != nil {
			msg = err.Error()
		}
		return fileReport{file, "config", []config.Issue{{Severity: config.SeverityError, Msg: msg}}}
	}
	cfg := a.Config
	issues := cfg.Validate()
	if cfg.Missing {
		issues = append([]config.Issue{{Severity: config.SeverityWarn, Msg: "file not found; defaults in use"}}, issues...)
	}
	return fileReport{cfg.File, "config", nonNil(issues)}
}

func readManifestFile(path string) (*config.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return config.ParseManifest(data, path)
}

// nonNil keeps JSON output as [] rather than null.
func nonNil(i []config.Issue) []config.Issue {
	if i == nil {
		return []config.Issue{}
	}
	return i
}

func printReports(w io.Writer, reports []fileReport) {
	for _, r := range reports {
		fmt.Fprintf(w, "%s (%s)\n", r.File, r.Kind)
		if len(r.Issues) == 0 {
			fmt.Fprintln(w, "  ok")
		}
		for _, i := range r.Issues {
			if i.Path == "" {
				fmt.Fprintf(w, "  %-5s %s\n", i.Severity, i.Msg)
			} else {
				fmt.Fprintf(w, "  %-5s %s: %s\n", i.Severity, i.Path, i.Msg)
			}
		}
	}
}
