// Package cli defines the ade command tree. Each command lives in its own file
// and registers itself from init() via register.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
)

// Version is set at build time (-ldflags "-X .../cli.Version=...").
var Version = "dev"

var builders []func() *cobra.Command

// register adds a top-level subcommand builder.
func register(b func() *cobra.Command) { builders = append(builders, b) }

// rootRun is what bare `ade` does; launch.go sets it (attach/create the zellij
// session). Without it, bare `ade` prints help.
var rootRun func(cmd *cobra.Command, args []string) error

type ctxKey struct{}

// appFrom returns the App loaded by the root's PersistentPreRunE.
func appFrom(cmd *cobra.Command) *app.App {
	a, _ := cmd.Context().Value(ctxKey{}).(*app.App)
	return a
}

// jsonFlag is the shared --json flag value.
var jsonOut bool

// printJSON writes v as indented JSON to w.
func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// exitError carries a specific process exit code.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// withCode wraps err so Execute exits with code.
func withCode(code int, err error) error { return &exitError{code: code, err: err} }

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "ade",
		Short:         "Agentic development environment: your forge workflow in the terminal",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.Load("user")
			if err != nil {
				return err
			}
			cmd.SetContext(context.WithValue(cmd.Context(), ctxKey{}, a))
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if rootRun == nil {
				return cmd.Help()
			}
			return rootRun(cmd, args)
		},
	}
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	for _, b := range builders {
		root.AddCommand(b())
	}
	return root
}

// Execute runs the CLI and returns the process exit code. Every command that
// got as far as loading the app leaves one `cli.<path>` audit line.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd, err := newRoot().ExecuteContextC(ctx)
	if a := appFrom(cmd); a != nil {
		action := "cli.ade"
		if sub := strings.Fields(cmd.CommandPath())[1:]; len(sub) > 0 {
			action = "cli." + strings.Join(sub, ".")
		}
		e := audit.Entry{Action: action, Params: map[string]any{"args": os.Args[1:]}}
		if err != nil {
			e.Outcome, e.Error = "error", err.Error()
		}
		if aerr := a.Audit.Append(e); aerr != nil {
			fmt.Fprintln(os.Stderr, "ade: audit:", aerr)
		}
		a.Close() //nolint:errcheck
	}
	if err == nil {
		return 0
	}
	fmt.Fprintln(os.Stderr, "ade:", err)
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return 1
}
