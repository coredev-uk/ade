package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/workspace"
)

func init() { register(newVSCodeCmd) }

func newVSCodeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "vscode", Short: "VS Code host integration"}
	cmd.AddCommand(newVSCodeTasksCmd(), newVSCodeWorkspaceCmd())
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

func newVSCodeWorkspaceCmd() *cobra.Command {
	var (
		open    bool
		ws      string
		group   string
		project string
	)
	cmd := &cobra.Command{
		Use:   "workspace [item-ref|worktree]",
		Short: "Write a multi-root VS Code workspace for a work item or a repo group (--open starts VS Code)",
		Long: `Writes <name>.code-workspace into the item's records directory (outside every git
tree) and prints its path. The workspace has one folder per repo of the project:
the work item's own worktree where it has one, the repo's baseline (the
default-branch tree) otherwise; plus the run graph as tasks (labels "ade: <node>", dependsOn and the
API_URL wiring of "ade ws up", the item's trees pinned) and settings that keep
.bare out of the explorer and search.

<item-ref> is a tracker issue, child issue or MR ref (group/project#N, !N, or N
with one registered project), resolved from the cache. A worktree path works
too: its item is found from its branch; a baseline tree has no item, so every
folder is a baseline. Existing files are regenerated; other top-level keys
(extensions, launch), your settings and your own tasks are kept.

--group <name> names a [repo_groups] entry of the manifest. Alone (with
--project when several projects are cloned) it regenerates that group's
<workspace>.code-workspace in the project directory, the file "ade update"
keeps, and prints its path: one folder per repo of the group, each at its
baseline. Together with an item ref or worktree, the item's file holds only the
group's repos (item worktrees where it has them) and is named
<item>-<group>.code-workspace.

--open runs "code <file>" when code is on PATH; otherwise the path is printed
with a hint.`,
		Example: "  ade vscode workspace acme/tracker#15 --open\n  ade vscode workspace ~/ade/demo/api/15-rate-limit --json\n  ade vscode workspace --group frontend --open\n  ade vscode workspace acme/tracker#15 --group frontend",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			if len(args) == 0 {
				if group == "" {
					return errors.New("name a work item or worktree, or a repo group with --group")
				}
				return runGroupWorkspace(cmd, project, group, open)
			}
			if project != "" {
				return errors.New("--project only applies to --group without an item ref or worktree")
			}
			st, err := a.Cache()
			if err != nil {
				return err
			}
			items, err := forgesync.ItemRefs(st)
			if err != nil {
				return err
			}
			req, err := codeRequest(cmd, args[0], items)
			if err != nil {
				return err
			}
			req.Workspace, req.Group, req.Open = ws, group, open
			res, err := workspace.CodeWorkspaceFor(cmd.Context(), a, req)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if jsonOut {
				return printCodeJSON(w, res)
			}
			fmt.Fprintln(w, res.Path)
			for _, f := range res.Folders {
				fmt.Fprintf(w, "  %-9s %s\n", f.Source, f.Name)
			}
			if res.Hint != "" {
				fmt.Fprintln(w, res.Hint)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&open, "open", false, "open the workspace in VS Code (needs code on PATH)")
	cmd.Flags().StringVar(&ws, "workspace", "", "[workspaces.<name>] whose run group becomes the tasks (default \"default\")")
	cmd.Flags().StringVar(&group, "group", "", "[repo_groups] entry: write its workspace file, or limit an item's file to its repos")
	cmd.Flags().StringVar(&project, "project", "", "ADE project of --group (default: the only one)")
	return cmd
}

// runGroupWorkspace regenerates the workspace file of a repo group in its
// project directory and prints (or opens) it.
func runGroupWorkspace(cmd *cobra.Command, projectName, group string, open bool) error {
	a := appFrom(cmd)
	if projectName == "" {
		names, err := project.Cloned(a.Root)
		if err != nil {
			return err
		}
		switch len(names) {
		case 0:
			return errors.New("no projects cloned yet (run `ade project add`)")
		case 1:
			projectName = names[0]
		default:
			return fmt.Errorf("several projects are cloned (%s): pick one with --project", strings.Join(names, ", "))
		}
	}
	dir := filepath.Join(a.Root, projectName)
	mf, err := project.LoadManifest(dir)
	if err != nil {
		return fmt.Errorf("project %s: %w (run `ade project add`)", projectName, err)
	}
	path, folders, _, err := project.WriteGroupWorkspace(cmd.Context(), dir, mf, group)
	res := workspace.CodeOutcome{CodeResult: workspace.CodeResult{Path: path}}
	for _, f := range folders {
		res.Folders = append(res.Folders, workspace.CodeFolder{Name: f.Name, Path: f.Path, Source: workspace.FolderBaseline})
	}
	if err == nil && open {
		res.Opened, res.Hint, err = workspace.OpenCodeWorkspace(cmd.Context(), path)
	}
	// Audited like the item workspace (workspace.vscode), with the group as target.
	entry := audit.Entry{
		Action: "workspace.vscode", Target: "repo_groups." + group, Project: projectName, Outcome: "ok",
		Params: map[string]any{"file": path, "folders": len(res.Folders), "opened": res.Opened},
	}
	if err != nil {
		entry.Outcome, entry.Error = "error", err.Error()
	}
	if a.Audit != nil {
		if aerr := a.Audit.Append(entry); aerr != nil && err == nil {
			err = aerr
		}
	}
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	if jsonOut {
		return printCodeJSON(w, res)
	}
	fmt.Fprintln(w, res.Path)
	for _, f := range res.Folders {
		fmt.Fprintf(w, "  %-9s %s\n", f.Source, f.Name)
	}
	if res.Hint != "" {
		fmt.Fprintln(w, res.Hint)
	}
	return nil
}

// codeRequest resolves the argument: a ref (#, !, or a bare issue number) names
// a cached item, anything else is a worktree whose item (if any) is found from
// its branch.
func codeRequest(cmd *cobra.Command, arg string, items []workspace.ItemRef) (workspace.CodeRequest, error) {
	if strings.ContainsAny(arg, "#!") || isNumber(arg) {
		ref, err := itemRef(cmd, arg)
		if err != nil {
			return workspace.CodeRequest{}, err
		}
		it, ok := workspace.FindItem(items, ref)
		if !ok {
			return workspace.CodeRequest{}, fmt.Errorf("no cached work item for %s (run `ade sync`)", ref)
		}
		return workspace.CodeRequest{Item: &it}, nil
	}
	loc, err := workspace.Locate(appFrom(cmd).Root, arg)
	if err != nil {
		return workspace.CodeRequest{}, err
	}
	req := workspace.CodeRequest{Worktree: loc.Tree}
	if it, ok := workspace.ItemFor(cmd.Context(), items, loc); ok {
		req.Item = &it
	}
	return req, nil
}

func isNumber(s string) bool {
	s = strings.TrimPrefix(s, "#")
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// codeJSON is the JSON shape of `ade vscode workspace`.
type codeJSON struct {
	Path    string       `json:"path"`
	Folders []codeFolder `json:"folders"`
	Tasks   int          `json:"tasks"`
	Opened  bool         `json:"opened"`
	Hint    string       `json:"hint,omitempty"`
}

type codeFolder struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Source string `json:"source"`
}

func printCodeJSON(w io.Writer, res workspace.CodeOutcome) error {
	out := codeJSON{Path: res.Path, Tasks: res.Tasks, Opened: res.Opened, Hint: res.Hint, Folders: []codeFolder{}}
	for _, f := range res.Folders {
		out.Folders = append(out.Folders, codeFolder{Name: f.Name, Path: f.Path, Source: f.Source})
	}
	return printJSON(w, out)
}
