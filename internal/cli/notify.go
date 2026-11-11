package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/notify"
)

func init() { register(newNotifyCmd) }

func newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "notify", Short: "Notification tools"}
	cmd.AddCommand(newNotifySendCmd())
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
