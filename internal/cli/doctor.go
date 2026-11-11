package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/doctor"
	"github.com/coredev-uk/ade/internal/paths"
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
	return &cobra.Command{
		Use:               "doctor",
		Short:             "Check config, tools, credentials and host capabilities",
		Args:              cobra.NoArgs,
		PersistentPreRunE: lenientPreRun,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checks := doctor.Run(cmd.Context(), doctorOptions(cmd))
			w := cmd.OutOrStdout()
			if jsonOut {
				if err := printJSON(w, checks); err != nil {
					return err
				}
			} else {
				printChecks(w, checks)
			}
			if doctor.HasErrors(checks) {
				return withCode(1, errors.New("doctor found problems"))
			}
			return nil
		},
	}
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
