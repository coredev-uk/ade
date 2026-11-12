package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/notify"
)

func init() { register(newNotifyCmd) }

func newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "notify", Short: "Notification tools"}
	cmd.AddCommand(newNotifySendCmd(), newNotifyTestCmd())
	return cmd
}

// newNotifyTestCmd runs the [notify.external] command once with a sample
// event, bypassing its kind filter and dedupe window, so a new command can be
// tried without waiting for a budget warning.
func newNotifyTestCmd() *cobra.Command {
	var sev string
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run the external notification command once with a sample event",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			x := appFrom(cmd).Config.Notify.External
			if strings.TrimSpace(x.Command) == "" {
				return errors.New("notify.external.command is not set in the user config")
			}
			e := events.New("notify.test", events.ParseSeverity(strings.ToLower(sev)), "ade external notification test")
			e.Detail = "sent by `ade notify test`"
			start := time.Now()
			err := notify.Run(cmd.Context(), x, e)
			took := time.Since(start)
			if jsonOut {
				out := map[string]any{"ok": err == nil, "command": x.Command, "took_ms": took.Milliseconds()}
				if err != nil {
					out["error"] = err.Error()
				}
				if perr := printJSON(cmd.OutOrStdout(), out); perr != nil {
					return perr
				}
			} else if err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "ran %q: ok (%s)\n", x.Command, took.Round(time.Millisecond))
			}
			if err != nil {
				return withCode(1, fmt.Errorf("external notification command failed: %w", err))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sev, "severity", "warn", "info | success | warn | error")
	return cmd
}

// newNotifySendCmd records one notification in the inbox. A running dashboard
// picks it up within seconds and shows it as a pop-up, so scripts, VS Code
// tasks and agents in other panes can raise notifications inside ADE.
func newNotifySendCmd() *cobra.Command {
	var kind, sev, title, subject, detail, project string
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Record a notification; the dashboard shows it as a pop-up",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			stop, err := notify.Attach(context.Background(), a, notify.Channels{})
			if err != nil {
				return err
			}
			e := events.New(kind, events.ParseSeverity(strings.ToLower(sev)), title)
			e.Subject, e.Detail, e.Project = subject, detail, project
			a.Bus.Publish(e)
			stop()
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), map[string]string{"id": e.ID, "kind": e.Kind})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recorded %s (%s)\n", e.ID, e.Kind)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "notify.test", "dotted event kind, e.g. task.done")
	cmd.Flags().StringVar(&sev, "severity", "info", "info | success | warn | error")
	cmd.Flags().StringVar(&title, "title", "ade notification test", "one-line title")
	cmd.Flags().StringVar(&subject, "subject", "", "what it is about (a ref like group/proj#12, or repo/tree)")
	cmd.Flags().StringVar(&detail, "detail", "", "multi-line detail")
	cmd.Flags().StringVar(&project, "project", "", "ADE project name")
	return cmd
}
