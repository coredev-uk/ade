package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
	cmd.AddCommand(newConfigCheckCmd(), newConfigSchemaCmd())
	return cmd
}

// fileReport is the validation result for one file.
type fileReport struct {
	File   string         `json:"file"`
	Kind   string         `json:"kind"` // config | manifest
	Issues []config.Issue `json:"issues"`
	// Overlay is the local override merged into the file, when there is one.
	Overlay string `json:"overlay,omitempty"`
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

func newConfigSchemaCmd() *cobra.Command {
	var (
		manifest bool
		write    string
	)
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Print the JSON Schema of the user config or of a manifest",
		Long: "Prints the JSON Schema (draft 2020-12) generated from ADE's config structs, for editor\n" +
			"completion and typo detection in config.toml and *.ade.toml (taplo, VS Code Even Better\n" +
			"TOML: put `#:schema <path or URL>` on the first line). The user config schema is printed by\n" +
			"default, --manifest prints the manifest one, and --write DIR writes both as\n" +
			"config.schema.json and manifest.schema.json (what docs/schema/ holds).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if write != "" {
				if err := os.MkdirAll(write, 0o755); err != nil {
					return err
				}
				for _, k := range []config.SchemaKind{config.SchemaConfig, config.SchemaManifest} {
					doc, err := config.Schema(k)
					if err != nil {
						return err
					}
					path := filepath.Join(write, config.SchemaFile(k))
					if err := os.WriteFile(path, doc, 0o644); err != nil {
						return err
					}
					fmt.Fprintln(cmd.OutOrStdout(), "wrote", path)
				}
				return nil
			}
			kind := config.SchemaConfig
			if manifest {
				kind = config.SchemaManifest
			}
			doc, err := config.Schema(kind)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(doc)
			return err
		},
	}
	cmd.Flags().BoolVar(&manifest, "manifest", false, "print the manifest schema instead of the user config schema")
	cmd.Flags().StringVar(&write, "write", "", "write config.schema.json and manifest.schema.json into this directory")
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
			reports = append(reports, fileReport{File: label, Kind: "manifest", Issues: []config.Issue{{Severity: config.SeverityError, Msg: err.Error()}}})
			return
		}
		if m.Source == "" {
			m.Source = label
		}
		ms = append(ms, m)
	}
	if len(files) > 0 {
		for _, f := range files {
			m, err := config.ReadManifestFile(f)
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
		reports = append(reports, fileReport{m.Source, "manifest", nonNil(byFile[m.Source]), m.Overlay})
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
		return fileReport{File: file, Kind: "config", Issues: []config.Issue{{Severity: config.SeverityError, Msg: msg}}}
	}
	cfg := a.Config
	issues := cfg.Validate()
	if cfg.Missing {
		issues = append([]config.Issue{{Severity: config.SeverityWarn, Msg: "file not found; defaults in use"}}, issues...)
	}
	return fileReport{File: cfg.File, Kind: "config", Issues: nonNil(issues), Overlay: cfg.Overlay}
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
		if r.Overlay != "" {
			fmt.Fprintf(w, "  local override %s\n", r.Overlay)
		}
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
