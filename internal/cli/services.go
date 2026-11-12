package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/workspace"
)

func init() { register(newServicesCmd) }

// servicesFlags are shared by the project-level commands.
type servicesFlags struct {
	project string
	with    []string
}

func (f *servicesFlags) bind(c *cobra.Command, withPins bool) {
	c.Flags().StringVar(&f.project, "project", "", "ADE project (default: the one cwd is in, else the only one)")
	if withPins {
		c.Flags().StringArrayVar(&f.with, "with", nil, "pin a repo to a branch or worktree instead of its baseline: repo=branch (repeatable)")
	}
}

func (f *servicesFlags) trees() (workspace.TreeOptions, error) {
	pins, err := keyValues("--with", f.with)
	return workspace.TreeOptions{Pins: pins}, err
}

// projectName is the project the command acts on: --project, else the project
// cwd is inside, else the only one.
func (f *servicesFlags) projectName(a *app.App) (string, error) {
	if f.project != "" {
		return f.project, nil
	}
	if name, ok := cwdProject(a); ok {
		return name, nil
	}
	names, err := workspace.ProjectNames(a.Root)
	if err != nil {
		return "", err
	}
	switch len(names) {
	case 0:
		return "", fmt.Errorf("no projects under %s (run `ade project add`)", a.Root)
	case 1:
		return names[0], nil
	}
	return "", fmt.Errorf("several projects (%s): pass --project <name>", strings.Join(names, ", "))
}

func cwdProject(a *app.App) (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	return workspace.ProjectOf(a.Root, cwd)
}

func newServicesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "services",
		Short: "A project's services, outside any worktree: up, down, status, install, open",
		Long: `Runs and inspects the services of a project without a worktree tab.

A node is named as in the manifest (api.dev, services.db), by the bare name of a
service (db) or by a group, which stands for its members. Without names, up,
install and open take every [services.*] node; down and status take every node
that has an instance. Repos resolve to their baseline trees, or to a --with
pin. Instances are the ones workspaces use: a service a worktree's tab already
runs is shared, ` + "`ade ws status`" + ` lists them all, and a project-level
down leaves what a workspace still holds running (--force stops it too).`,
	}
	cmd.AddCommand(newServicesStatusCmd(), newServicesUpCmd(), newServicesDownCmd(), newServicesInstallCmd(), newServicesOpenCmd())
	return cmd
}

// --- status ----------------------------------------------------------------

// projectServices is the status of one project's services.
type projectServices struct {
	Project  string                    `json:"project"`
	Services []workspace.ServiceStatus `json:"services"`
}

func newServicesStatusCmd() *cobra.Command {
	var (
		sf    servicesFlags
		watch bool
	)
	cmd := &cobra.Command{
		Use:   "status [names...]",
		Short: "Every node of the project with its instances in all worktrees",
		Long: `Lists the project's services, and every node that runs, with each instance:
its state, port, the worktree it runs in and who holds it. A declared service
that runs nowhere shows as stopped. Without --project it reports the project
cwd is in, or every project when cwd is in none. --watch refreshes on a
terminal until interrupted.`,
		RunE: func(cmd *cobra.Command, names []string) error {
			a := appFrom(cmd)
			mgr, err := workspace.NewManager(a)
			if err != nil {
				return err
			}
			projects, err := statusProjects(a, sf)
			if err != nil {
				return err
			}
			render := func() error {
				out := make([]projectServices, 0, len(projects))
				for _, name := range projects {
					loc, err := workspace.LocateProject(a.Root, name)
					if err != nil {
						return err
					}
					var nodes []string
					if len(names) > 0 {
						if nodes, err = workspace.ResolveNodes(loc.Manifest, names); err != nil {
							return err
						}
					}
					svc, err := mgr.ProjectStatus(cmd.Context(), loc, nodes)
					if err != nil {
						return err
					}
					out = append(out, projectServices{Project: name, Services: svc})
				}
				if jsonOut {
					return printJSON(cmd.OutOrStdout(), out)
				}
				printProjectServices(cmd.OutOrStdout(), a.Root, out)
				return nil
			}
			f, tty := cmd.OutOrStdout().(*os.File)
			if !watch || jsonOut || !tty || !isTerminal(f) {
				return render()
			}
			for {
				fmt.Fprint(f, "\x1b[H\x1b[2J")
				if err := render(); err != nil {
					return err
				}
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(2 * time.Second):
				}
			}
		},
	}
	sf.bind(cmd, false)
	cmd.Flags().BoolVar(&watch, "watch", false, "refresh every 2 seconds on a terminal")
	return cmd
}

// statusProjects is the projects status covers: the one named, the one cwd is
// in, else every project under the root.
func statusProjects(a *app.App, sf servicesFlags) ([]string, error) {
	if sf.project != "" {
		return []string{sf.project}, nil
	}
	if name, ok := cwdProject(a); ok {
		return []string{name}, nil
	}
	names, err := workspace.ProjectNames(a.Root)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no projects under %s (run `ade project add`)", a.Root)
	}
	return names, nil
}

func printProjectServices(w io.Writer, root string, all []projectServices) {
	for i, p := range all {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, p.Project)
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, s := range p.Services {
			if len(s.Instances) == 0 {
				fmt.Fprintf(tw, "  %s\t%s\t-\t\t\n", s.Node, s.State)
				continue
			}
			for _, r := range s.Instances {
				port := "-"
				if r.Port > 0 {
					port = fmt.Sprint(r.Port)
				}
				state := workspace.RowState(r)
				if r.BlockedBy != "" {
					state = "blocked by " + r.BlockedBy
				}
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", s.Node, state, port, treeLabel(root, p.Project, r.Tree), "held by "+holderLabels(root, p.Project, r.Holders))
			}
		}
		tw.Flush()
	}
}

// treeLabel names a worktree by its path below the project: "web/main".
func treeLabel(root, project, tree string) string {
	if rel, err := filepath.Rel(filepath.Join(root, project), tree); err == nil && filepath.IsLocal(rel) {
		return rel
	}
	return tree
}

// holderLabels lists who holds an instance: worktrees by their path below the
// project, "project" for a project-level run, "-" for nobody.
func holderLabels(root, project string, holders []string) string {
	var out []string
	for _, h := range holders {
		base, _, _ := strings.Cut(h, "#")
		label := treeLabel(root, project, base)
		if strings.HasPrefix(base, workspace.ProjectHolderPrefix) {
			label = "project"
		}
		if !slices.Contains(out, label) {
			out = append(out, label)
		}
	}
	if len(out) == 0 {
		return "-"
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}

// --- up --------------------------------------------------------------------

func newServicesUpCmd() *cobra.Command {
	var (
		sf     servicesFlags
		detach bool
	)
	cmd := &cobra.Command{
		Use:   "up [names...]",
		Short: "Start services (and what they require) for the project, outside any worktree",
		Long: `Runs the install hooks of the trees involved, then starts the named nodes and
everything they require in dependency order, waiting for each probe, over the
project's baseline trees. It stays until interrupted (Ctrl-C stops what it
started and nothing another workspace still holds); --detach returns after
readiness and leaves them running until "ade services down". A node a
worktree's workspace already runs is shared, not started twice.`,
		RunE: func(cmd *cobra.Command, names []string) error {
			a := appFrom(cmd)
			mgr, loc, plan, err := servicesPlan(cmd, a, sf, names)
			if err != nil {
				return err
			}
			return supervise(cmd, a, mgr, loc, plan, detach, runLabels{
				name: "project " + loc.Project,
				down: "ade services down --project " + loc.Project,
			})
		},
	}
	sf.bind(cmd, true)
	cmd.Flags().BoolVar(&detach, "detach", false, "return after readiness and leave the services running")
	return cmd
}

// servicesPlan resolves the names to nodes and plans them over the project.
func servicesPlan(cmd *cobra.Command, a *app.App, sf servicesFlags, names []string) (*workspace.Manager, workspace.Location, *workspace.Plan, error) {
	name, err := sf.projectName(a)
	if err != nil {
		return nil, workspace.Location{}, nil, err
	}
	loc, err := workspace.LocateProject(a.Root, name)
	if err != nil {
		return nil, loc, nil, err
	}
	nodes, err := workspace.ResolveNodes(loc.Manifest, names)
	if err != nil {
		return nil, loc, nil, err
	}
	trees, err := sf.trees()
	if err != nil {
		return nil, loc, nil, err
	}
	mgr, err := workspace.NewManager(a)
	if err != nil {
		return nil, loc, nil, err
	}
	plan, err := mgr.Plan(cmd.Context(), loc, workspace.PlanOptions{Nodes: nodes, Trees: trees})
	return mgr, loc, plan, err
}

// --- down ------------------------------------------------------------------

func newServicesDownCmd() *cobra.Command {
	var (
		sf    servicesFlags
		force bool
	)
	cmd := &cobra.Command{
		Use:   "down [names...]",
		Short: "Stop the project's services that no workspace still uses",
		Long: `Releases what "ade services up" holds and stops the instances nobody else holds,
dependents first. Without names it covers every instance of the project, in any
worktree; with names, those nodes' instances. An instance a worktree's workspace
still holds keeps running and is listed as still held; --force drops those
holds too and also stops keep = true services, so a workspace's pane for it
shows the service exited.`,
		RunE: func(cmd *cobra.Command, names []string) error {
			a := appFrom(cmd)
			name, err := sf.projectName(a)
			if err != nil {
				return err
			}
			loc, err := workspace.LocateProject(a.Root, name)
			if err != nil {
				return err
			}
			var nodes []string
			if len(names) > 0 {
				if nodes, err = workspace.ResolveNodes(loc.Manifest, names); err != nil {
					return err
				}
			}
			mgr, err := workspace.NewManager(a)
			if err != nil {
				return err
			}
			res, err := mgr.Down(cmd.Context(), name, workspace.DownOptions{Holder: loc.Holder(), Nodes: nodes, Force: force})
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, res)
			}
			for _, s := range res.Stopped {
				fmt.Fprintf(w, "stopped %s (%s)\n", s.Node, treeLabel(loc.Root, name, s.Tree))
			}
			for _, h := range res.StillHeld {
				fmt.Fprintf(w, "still running: %s (%s), held by %s\n", h.Node, treeLabel(loc.Root, name, h.Tree), holderLabels(loc.Root, name, h.Holders))
			}
			switch {
			case len(res.Stopped) == 0 && len(res.StillHeld) == 0:
				fmt.Fprintln(w, "nothing running")
			case len(res.StillHeld) > 0:
				fmt.Fprintln(w, "stop those too with --force, or close the workspaces that hold them")
			}
			return nil
		},
	}
	sf.bind(cmd, false)
	cmd.Flags().BoolVar(&force, "force", false, "also stop instances a workspace still holds, and keep = true ones")
	return cmd
}

// --- install ---------------------------------------------------------------

func newServicesInstallCmd() *cobra.Command {
	var (
		sf    servicesFlags
		force bool
	)
	cmd := &cobra.Command{
		Use:   "install [names...]",
		Short: "Run the install hooks of the trees the project's services run from",
		Long: `Runs [repos.<r>.hooks] install in every tree the named nodes run from (the
baselines, or --with pins), as "ade ws install" does for a worktree: a tree
whose commands already succeeded is skipped, --force runs them again. The
standalone services tab runs this first; its service panes wait for it.`,
		RunE: func(cmd *cobra.Command, names []string) error {
			a := appFrom(cmd)
			mgr, loc, plan, err := servicesPlan(cmd, a, sf, names)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				out = cmd.ErrOrStderr() // stdout carries the JSON result
			}
			results := installTargets(cmd.Context(), a, mgr, mgr.InstallTargets(loc, plan, true), loc.Project, force, out)
			if jsonOut {
				if perr := printJSON(cmd.OutOrStdout(), results); perr != nil {
					return perr
				}
			} else if len(results) == 0 {
				fmt.Fprintln(out, "no install hooks for these services")
			}
			return installError(results)
		},
	}
	sf.bind(cmd, true)
	cmd.Flags().BoolVar(&force, "force", false, "run the commands again even if they already succeeded")
	return cmd
}

// --- open ------------------------------------------------------------------

func newServicesOpenCmd() *cobra.Command {
	var (
		sf      servicesFlags
		session string
	)
	cmd := &cobra.Command{
		Use:   "open [names...]",
		Short: "Open the project's own services tab in the zellij session",
		Long: `Opens (or focuses) a tab named "<project> · services" in the ade zellij session:
the install step, then one pane per node, started at once, each waiting for the
install and for what it requires, as in a workspace's services tab but over the
project's baseline trees. Outside zellij nothing opens; the output says to run
"ade services up".`,
		RunE: func(cmd *cobra.Command, names []string) error {
			a := appFrom(cmd)
			name, err := sf.projectName(a)
			if err != nil {
				return err
			}
			loc, err := workspace.LocateProject(a.Root, name)
			if err != nil {
				return err
			}
			nodes, err := workspace.ResolveNodes(loc.Manifest, names)
			if err != nil {
				return err
			}
			trees, err := sf.trees()
			if err != nil {
				return err
			}
			res, err := workspace.OpenProjectServices(cmd.Context(), a, name, nodes, workspace.ProjectTabOptions{Trees: trees, Session: session})
			w := cmd.OutOrStdout()
			if jsonOut {
				if perr := printJSON(w, res); perr != nil {
					return perr
				}
				return err
			}
			if err != nil {
				return err
			}
			switch {
			case res.Hint != "":
				fmt.Fprintln(w, "no zellij here; run: "+res.Hint)
			case !res.Created:
				fmt.Fprintf(w, "focused tab %s in session %s\n", res.Tab, res.Session)
			default:
				fmt.Fprintf(w, "opened tab %s in session %s\n", res.Tab, res.Session)
			}
			return nil
		},
	}
	sf.bind(cmd, true)
	cmd.Flags().StringVar(&session, "session", "", "zellij session to open the tab in (default: ade; for tests)")
	_ = cmd.Flags().MarkHidden("session")
	return cmd
}
