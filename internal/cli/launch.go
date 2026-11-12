package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/host"
	"github.com/coredev-uk/ade/internal/host/zellij"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/tui"
	"github.com/coredev-uk/ade/internal/workspace"
)

func init() {
	rootRun = func(cmd *cobra.Command, _ []string) error { return launch(cmd, false) }
	register(func() *cobra.Command {
		var dry bool
		c := &cobra.Command{
			Use:   "launch",
			Short: "Open the dashboard host (zellij session or plain terminal)",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return launch(cmd, dry) },
		}
		c.Flags().BoolVar(&dry, "dry-run", false, "print the decision, layout and zellij argv instead of launching")
		return c
	})
	register(func() *cobra.Command {
		return &cobra.Command{
			Use:   "dash",
			Short: "Run the dashboard in this terminal (the zellij home pane command)",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return tui.Run(cmd.Context(), appFrom(cmd)) },
		}
	})
}

// launch resolves the host environment and either runs the dashboard here,
// moves a running zellij session to its dashboard tab, or replaces this
// process with zellij so it owns the TTY.
func launch(cmd *cobra.Command, dry bool) error {
	a := appFrom(cmd)
	env, err := hostEnv(cmd.Context(), a)
	if err != nil {
		return err
	}
	action, ok := host.Decide(env)
	if !ok {
		return fmt.Errorf("host.mode is %q but zellij is not on PATH", env.Mode)
	}
	if action == host.RunHere {
		if dry {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "run dashboard in this process")
			return err
		}
		return tui.Run(cmd.Context(), a)
	}

	ade, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate ade executable: %w", err)
	}
	layout := zellij.Layout(ade)
	layoutFile := filepath.Join(a.Paths.CacheDir, "layout.kdl")
	if action == host.FocusHome || action == host.OpenHome {
		return showHomeTab(cmd, action, env.CurrentSession, layout, layoutFile, dry)
	}
	var zellijConfig string
	if env.UsesUnlockFirst(os.Getenv) {
		// A zellij started inside VS Code runs ADE's unlock-first preset (PLAN 6.7).
		zellijConfig = filepath.Join(filepath.Dir(a.Config.File), zellij.ConfigFileName)
	}
	argv := host.Argv(action, env.Session, layoutFile, zellijConfig)
	if dry {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "# layout file: %s\n%s", layoutFile, layout)
		if zellijConfig != "" {
			fmt.Fprintf(out, "# zellij config: %s (unlock-first preset)\n", zellijConfig)
		}
		fmt.Fprintf(out, "# argv: %s\n", strings.Join(argv, " "))
		return nil
	}
	if zellijConfig != "" {
		if _, err := zellij.WriteUnlockFirst(filepath.Dir(zellijConfig)); err != nil {
			return fmt.Errorf("write zellij config: %w", err)
		}
	}
	focusFirst := host.FocusesHomeFirst(action, env.Session)
	if action == host.Create || focusFirst {
		if err := writeHomeLayout(a.Paths.CacheDir, layoutFile, layout); err != nil {
			return err
		}
	}
	if focusFirst {
		// The session would open on the tab focused last, often an agent tab.
		if _, err := workspace.FocusTab(cmd.Context(), host.SessionName, zellij.HomeTab, layoutFile); err != nil {
			fmt.Fprintln(os.Stderr, "ade: could not focus the home tab:", err)
		}
	}
	bin, err := exec.LookPath("zellij")
	if err != nil {
		return err
	}
	// exec skips Execute's per-command audit line, so record the launch here.
	if err := a.Audit.Append(audit.Entry{Action: "cli.launch", Target: strings.Join(argv, " "), Outcome: "ok"}); err != nil {
		fmt.Fprintln(os.Stderr, "ade: audit:", err)
	}
	// Release the audit DB etc. before exec: deferred cleanup never runs after it.
	a.Close() //nolint:errcheck
	return syscall.Exec(bin, argv, os.Environ())
}

func writeHomeLayout(cacheDir, layoutFile, layout string) error {
	if err := paths.EnsureDir(cacheDir); err != nil {
		return err
	}
	if err := os.WriteFile(layoutFile, []byte(layout), 0o600); err != nil {
		return fmt.Errorf("write layout: %w", err)
	}
	return nil
}

// showHomeTab runs inside zellij: it focuses the `home` tab of the current
// session, creating it (running `ade dash`) when it is gone, so the dashboard
// never runs a second time in the pane that typed `ade`.
func showHomeTab(cmd *cobra.Command, action host.Action, session, layout, layoutFile string, dry bool) error {
	out := cmd.OutOrStdout()
	if action == host.OpenHome {
		fmt.Fprintf(out, "zellij cannot nest sessions: opening the dashboard in the %q tab of session %q\n", zellij.HomeTab, session)
	}
	if dry {
		fmt.Fprintf(out, "# layout file: %s\n%s", layoutFile, layout)
		fmt.Fprintf(out, "# focus tab %q of session %q, or create it from the layout\n", zellij.HomeTab, session)
		return nil
	}
	if err := writeHomeLayout(filepath.Dir(layoutFile), layoutFile, layout); err != nil {
		return err
	}
	if _, err := workspace.FocusTab(cmd.Context(), session, zellij.HomeTab, layoutFile); err != nil {
		return fmt.Errorf("open the home tab: %w", err)
	}
	return nil
}

func hostEnv(ctx context.Context, a *app.App) (host.Env, error) {
	env := host.Env{
		Mode: a.Config.Host.Mode, InsideZellij: os.Getenv("ZELLIJ") != "", CurrentSession: os.Getenv("ZELLIJ_SESSION_NAME"),
		InsideVSCode: os.Getenv("TERM_PROGRAM") == "vscode" || os.Getenv("VSCODE_IPC_HOOK_CLI") != "",
		WebTTY:       host.WebTTY(os.Getenv),
	}
	if env.InsideZellij {
		return env, nil
	}
	if _, err := exec.LookPath("zellij"); err != nil {
		return env, nil
	}
	env.ZellijAvailable = true
	// A failing list (no server yet) just means no sessions.
	out, _ := exec.CommandContext(ctx, "zellij", "list-sessions", "--no-formatting").Output()
	env.Session = zellij.SessionState(string(out), host.SessionName)
	return env, nil
}
