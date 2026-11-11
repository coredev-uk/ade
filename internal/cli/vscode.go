package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/workspace"
)

func init() { register(newVSCodeCmd) }

func newVSCodeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "vscode", Short: "VS Code host integration"}
	cmd.AddCommand(newVSCodeTasksCmd())
	return cmd
}

func newVSCodeTasksCmd() *cobra.Command {
	var (
		tf     treeFlags
		stdout bool
	)
	cmd := &cobra.Command{
		Use:   "tasks [worktree]",
		Short: "Write .vscode/tasks.json for a worktree's run graph",
		Long: `Writes one VS Code task per run-graph node. dependsOn mirrors "requires",
services are background tasks whose problem matcher ends on the node's
readiness (the readiness regexp for log probes, otherwise the
"ade: ready <node>" line that "ade task run" prints), and oneshots are plain
tasks that finish on exit 0. Existing non-ADE tasks in the file are kept; ADE's
own (labels starting "ade: ") are replaced.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			trees, err := tf.options()
			if err != nil {
				return err
			}
			a := appFrom(cmd)
			loc, err := workspace.Locate(a.Root, worktreeArg(args))
			if err != nil {
				return err
			}
			mgr, err := workspace.NewManager(a)
			if err != nil {
				return err
			}
			plan, err := mgr.Plan(cmd.Context(), loc, workspace.PlanOptions{Workspace: tf.workspace, Run: tf.run, Trees: trees})
			if err != nil {
				return err
			}
			ade, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate ade executable: %w", err)
			}
			var runArgs []string
			for _, kv := range tf.with {
				runArgs = append(runArgs, "--with", kv)
			}
			for _, kv := range tf.itemTrees {
				runArgs = append(runArgs, "--item-tree", kv)
			}
			tasks := workspace.BuildTasks(plan, ade, runArgs)
			w := cmd.OutOrStdout()
			if stdout {
				data, err := workspace.MergeTasks(nil, tasks)
				if err != nil {
					return err
				}
				_, err = w.Write(data)
				return err
			}
			path, err := workspace.WriteTasks(loc.Tree, tasks)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(w, map[string]any{"path": path, "tasks": len(tasks)})
			}
			fmt.Fprintf(w, "wrote %d task(s) to %s\n", len(tasks), relTo(loc.Tree, path))
			return nil
		},
	}
	tf.add(cmd)
	cmd.Flags().StringVar(&tf.run, "run", "", "export this group or node instead of the workspace's run group")
	cmd.Flags().BoolVar(&stdout, "stdout", false, "print the generated tasks.json instead of writing it")
	return cmd
}

// relTo shows path relative to dir when it is inside it.
func relTo(dir, path string) string {
	if rel, err := filepath.Rel(dir, path); err == nil && !filepath.IsAbs(rel) && len(rel) > 0 && rel[0] != '.' {
		return rel
	}
	return path
}
