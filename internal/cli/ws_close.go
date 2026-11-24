package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/workspace"
)

func newWsCloseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "close [worktree]",
		Short: "Stop a worktree's workspace and close its tabs",
		Long: `Stops the workspace and closes its zellij tabs, so it can be run from the
workspace tab's own shell. First the worktree's instances are released as
"ade ws down" does: the tasks and services this workspace holds stop, and an
instance another workspace still holds keeps running. Then, in every session
that still runs, the dashboard's home tab is focused (created when it is
missing, so the session keeps a tab) and the tabs ADE opened for the worktree
close, its "· services" tab included. The tab you run it in closes last.

Only tabs ADE opened or focused for the worktree close; a tab opened by an
older ADE is closed by hand once. Outside zellij it only stops the instances.
The worktree defaults to the current directory. Audited as workspace.close.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			w := cmd.OutOrStdout()
			printed := false
			res, err := workspace.Close(cmd.Context(), a, worktreeArg(args), func(res workspace.CloseResult) {
				printed = true
				printClose(w, res)
			})
			if err != nil {
				return err
			}
			if !printed {
				printClose(w, res)
			}
			return nil
		},
	}
}

func printClose(w io.Writer, res workspace.CloseResult) {
	if jsonOut {
		_ = printJSON(w, res)
		return
	}
	for _, msg := range res.Warnings {
		fmt.Fprintln(w, "warning: "+msg)
	}
	for _, s := range res.Stopped {
		fmt.Fprintf(w, "stopped %s\n", s.Node)
	}
	if len(res.Stopped) == 0 {
		fmt.Fprintln(w, "nothing to stop (instances still used elsewhere keep running)")
	}
	if len(res.Tabs) == 0 {
		fmt.Fprintln(w, "no open tab of this workspace")
		return
	}
	fmt.Fprintf(w, "closing %s\n", strings.Join(res.Tabs, ", "))
}
