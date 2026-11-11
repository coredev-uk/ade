package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/notify"
)

func init() { register(newNotifyCmd) }

func newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "notify", Short: "Notification tools"}
	cmd.AddCommand(newNotifyTestCmd())
	return cmd
}

// newNotifyTestCmd pushes one event through every channel so terminal OSC
// support (zellij, VS Code, ...) can be checked by eye.
func newNotifyTestCmd() *cobra.Command {
	var kind, sev, title string
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Send a test notification through all channels",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			// Force every channel on for this kind and ignore quiet hours:
			// the point is to see whether the terminal reacts.
			a.Config.Notify.Toast = []string{"*"}
			a.Config.Notify.Terminal = []string{"*"}
			a.Config.Notify.Desktop = []string{"*"}
			a.Config.Notify.Quiet = ""

			out := cmd.OutOrStdout()
			ch := notify.Channels{
				Toast:   func(e events.Event) { fmt.Fprintf(out, "toast:    %s\n", e.Title) },
				Desktop: true,
			}
			if isTTY(os.Stdout) {
				ch.Terminal = os.Stdout
			}
			stop, err := notify.Attach(context.Background(), a, ch)
			if err != nil {
				return err
			}
			e := events.New(kind, events.ParseSeverity(strings.ToLower(sev)), title)
			e.Detail = "ade notify test"
			a.Bus.Publish(e)
			stop()
			if ch.Terminal == nil {
				fmt.Fprintln(out, "terminal: skipped (stdout is not a terminal)")
			} else {
				fmt.Fprintln(out, "terminal: OSC 777 + OSC 9 + BEL sent")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "notify.test", "event kind")
	cmd.Flags().StringVar(&sev, "severity", "info", "info | success | warn | error")
	cmd.Flags().StringVar(&title, "title", "ade notification test", "event title")
	return cmd
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
