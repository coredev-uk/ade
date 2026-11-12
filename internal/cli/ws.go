package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/workspace"
)

func init() {
	register(newWsCmd)
	register(newTaskCmd)
}

// treeFlags are the flags that steer tree resolution (PLAN 2.1).
type treeFlags struct {
	workspace string
	run       string
	with      []string
	itemTrees []string
}

func (f *treeFlags) add(c *cobra.Command) {
	c.Flags().StringVar(&f.workspace, "workspace", workspace.DefaultWorkspace, "manifest [workspaces] entry")
	c.Flags().StringArrayVar(&f.with, "with", nil, "pin a required repo to a branch or worktree: repo=branch (repeatable)")
	c.Flags().StringArrayVar(&f.itemTrees, "item-tree", nil, "the work item's own worktree in a required repo: repo=path (repeatable)")
}

func (f *treeFlags) options() (workspace.TreeOptions, error) {
	pins, err := keyValues("--with", f.with)
	if err != nil {
		return workspace.TreeOptions{}, err
	}
	items, err := keyValues("--item-tree", f.itemTrees)
	if err != nil {
		return workspace.TreeOptions{}, err
	}
	return workspace.TreeOptions{Pins: pins, ItemTrees: items}, nil
}

func keyValues(flag string, in []string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, kv := range in {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("%s %q: want repo=value", flag, kv)
		}
		out[k] = v
	}
	return out, nil
}

// worktreeArg is the worktree a command acts on: the argument, else the cwd.
func worktreeArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return "."
}

func newWsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ws",
		Short: "Workspaces: run graph, instances and tabs for a worktree",
	}
	cmd.AddCommand(newWsUpCmd(), newWsDownCmd(), newWsStatusCmd(), newWsRestartCmd(), newWsLayoutCmd(), newWsInstallCmd())
	return cmd
}

func newWsUpCmd() *cobra.Command {
	var (
		tf       treeFlags
		noZellij bool
		detach   bool
		layout   string
		session  string
	)
	cmd := &cobra.Command{
		Use:   "up [worktree]",
		Short: "Bring up a worktree's workspace (zellij tab, or the run graph in this process)",
		Long: `Without flags this opens the workspace in your host: a tab in the "ade" zellij
session whose panes run the graph (VS Code gets tasks.json and a window, a plain
terminal gets a cd hint).

--no-zellij runs the run graph in this process instead: required instances are
started in dependency order (shared with other workspaces when they resolve to
the same tree), readiness is printed, and Ctrl-C stops what this workspace
started and nothing another workspace still uses. --detach leaves the
instances running after readiness; "ade ws down" stops them later.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			trees, err := tf.options()
			if err != nil {
				return err
			}
			a := appFrom(cmd)
			if !noZellij {
				return openWorkspace(cmd, a, worktreeArg(args), tf, trees, layout, session)
			}
			return runWorkspace(cmd, a, worktreeArg(args), tf, trees, detach)
		},
	}
	tf.add(cmd)
	cmd.Flags().StringVar(&tf.run, "run", "", "run this group or node instead of the workspace's run group")
	cmd.Flags().BoolVar(&noZellij, "no-zellij", false, "supervise the run graph in this process instead of opening a tab")
	cmd.Flags().BoolVar(&detach, "detach", false, "with --no-zellij: return after readiness and leave instances running")
	cmd.Flags().StringVar(&layout, "layout", "", "code | agent | review | manual (default: the workspace's layout)")
	cmd.Flags().StringVar(&session, "session", "", "zellij session to open the tab in (default: ade; for tests)")
	_ = cmd.Flags().MarkHidden("session")
	return cmd
}

func openWorkspace(cmd *cobra.Command, a *app.App, worktree string, tf treeFlags, trees workspace.TreeOptions, layout, session string) error {
	loc, err := workspace.Locate(a.Root, worktree)
	if err != nil {
		return err
	}
	opts := []workspace.OpenOption{
		workspace.WithWorkspace(tf.workspace), workspace.WithPins(trees.Pins), workspace.WithItemTrees(trees.ItemTrees),
		cachedItems(a),
	}
	if layout != "" {
		opts = append(opts, workspace.WithLayout(layout))
	}
	if session != "" {
		opts = append(opts, workspace.WithSession(session))
	}
	res, err := workspace.Open(cmd.Context(), a, loc.Project, loc.Repo, loc.Tree, opts...)
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
	for _, msg := range res.Warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+msg)
	}
	switch {
	case res.Hint != "":
		fmt.Fprintln(w, res.Hint)
	case res.Host == workspace.HostZellij && !res.Created:
		fmt.Fprintf(w, "focused tab %s in session %s\n", res.Tab, res.Session)
	case res.Host == workspace.HostZellij:
		fmt.Fprintf(w, "opened tab %s in session %s (layout %s)\n", res.Tab, res.Session, res.Layout)
	default:
		fmt.Fprintf(w, "opened %s in %s\n", loc.Tree, res.Host)
	}
	return nil
}

// cachedItems offers the synced work items to workspace.Open, which finds the
// one owning the worktree's branch; without a cache there is simply no item.
func cachedItems(a *app.App) workspace.OpenOption {
	st, err := a.Cache()
	if err != nil {
		return workspace.WithItems(nil)
	}
	items, _ := forgesync.ItemRefs(st)
	return workspace.WithItems(items)
}

// runWorkspace is `ade ws up --no-zellij`.
func runWorkspace(cmd *cobra.Command, a *app.App, worktree string, tf treeFlags, trees workspace.TreeOptions, detach bool) error {
	ctx := cmd.Context()
	loc, err := workspace.Locate(a.Root, worktree)
	if err != nil {
		return err
	}
	mgr, err := workspace.NewManager(a)
	if err != nil {
		return err
	}
	plan, err := mgr.Plan(ctx, loc, workspace.PlanOptions{Workspace: tf.workspace, Run: tf.run, Trees: trees})
	if err != nil {
		return err
	}
	if len(plan.Instances) == 0 {
		return fmt.Errorf("workspace %q of %s runs nothing: add a run group to [workspaces.%s]", plan.Workspace, loc.Project, plan.Workspace)
	}
	return supervise(cmd, a, mgr, loc, plan, detach, runLabels{
		name: fmt.Sprintf("workspace %s/%s", loc.Project, filepath.Base(loc.Tree)),
		down: "ade ws down " + loc.Tree,
	})
}

// runLabels are what a supervised run calls itself in its output.
type runLabels struct {
	name string // "workspace demo/main", "project demo"
	down string // the command that stops a detached run
}

// supervise runs a plan in this process: the install hooks first, then the
// nodes in dependency order, held by the location's holder (the worktree of a
// workspace, "project:<name>" for a project-level run). Without detach it
// stays until interrupted and releases what it started.
func supervise(cmd *cobra.Command, a *app.App, mgr *workspace.Manager, loc workspace.Location, plan *workspace.Plan, detach bool, label runLabels) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()
	// Install hooks come before any service, as in the workspace's services tab.
	if targets := mgr.InstallTargets(loc, plan, true); len(targets) > 0 {
		out := w
		if jsonOut {
			out = cmd.ErrOrStderr()
		}
		if err := installError(installTargets(ctx, a, mgr, targets, loc.Project, false, out)); err != nil {
			return err
		}
	}
	tel := newWsTelemetry(ctx, a, loc, plan.Workspace)
	defer tel.close()
	opts := workspace.UpOptions{Holder: loc.Holder(), Prepared: auditPrepare(a, loc.Project)}
	if !detach {
		opts.HolderPID = os.Getpid()
	}
	opts.Progress = func(r workspace.Result) {
		tel.result(r)
		if !jsonOut {
			fmt.Fprintln(w, "  "+r.Describe())
		}
	}
	if !jsonOut {
		fmt.Fprintf(w, "%s: starting %d node(s)\n", label.name, len(plan.Instances))
	}
	// A signal during Up must still release what was started.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	results, err := mgr.Up(sigCtx, plan, opts)
	failed := err != nil
	for _, r := range results {
		failed = failed || r.Outcome != workspace.OutcomeReady
	}
	if jsonOut {
		if perr := printJSON(w, results); perr != nil {
			return perr
		}
	}
	releaseCtx := context.WithoutCancel(ctx)
	if failed {
		tel.record(workspace.RunFailed, results)
		if _, rerr := mgr.Release(releaseCtx, loc.Holder()); rerr != nil {
			fmt.Fprintln(os.Stderr, "ade: release:", rerr)
		}
		if err != nil {
			return err
		}
		return withCode(1, errors.New(label.name+" did not come up"))
	}
	if !jsonOut {
		fmt.Fprintln(w, servicesStrip(results))
	}
	if detach {
		tel.record(workspace.RunDetached, results)
		if !jsonOut {
			fmt.Fprintf(w, "left running; stop with: %s\n", label.down)
		}
		return nil
	}
	tel.record(workspace.RunRunning, results)
	if !jsonOut {
		fmt.Fprintln(w, "ready; Ctrl-C stops the instances this run started")
	}
	ids := make([]string, len(plan.Instances))
	for i, in := range plan.Instances {
		ids[i] = in.ID
	}
	mgr.WatchBlocked(sigCtx, ids, blockedPoll, func(r workspace.Row) {
		tel.publish(workspace.BlockedEvent(r))
		if !jsonOut {
			fmt.Fprintf(w, "  %s blocked: %s is not running\n", r.Node, r.BlockedBy)
		}
	})
	stopped, err := mgr.Release(releaseCtx, loc.Holder())
	tel.record(workspace.RunStopped, results)
	if !jsonOut {
		fmt.Fprintf(w, "stopped %d instance(s)\n", len(stopped))
	}
	return err
}

// servicesStrip is PLAN 2.1's one-line Services strip: "api.dev ● 54031 · web.dev ● 54044".
func servicesStrip(results []workspace.Result) string {
	parts := make([]string, 0, len(results))
	for _, r := range results {
		s := r.Node + " ready"
		if r.Port > 0 {
			s += fmt.Sprintf(" %d", r.Port)
		}
		if r.Shared {
			s += " (shared)"
		}
		parts = append(parts, s)
	}
	return "services: " + strings.Join(parts, " · ")
}

func newWsDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down [worktree]",
		Short: "Release a worktree's instances; stop those nobody else uses",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			loc, err := workspace.Locate(a.Root, worktreeArg(args))
			if err != nil {
				return err
			}
			mgr, err := workspace.NewManager(a)
			if err != nil {
				return err
			}
			stopped, err := mgr.Release(cmd.Context(), loc.Tree)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if jsonOut {
				if stopped == nil {
					stopped = []workspace.Stopped{}
				}
				return printJSON(w, stopped)
			}
			if len(stopped) == 0 {
				fmt.Fprintln(w, "nothing to stop (instances still used elsewhere keep running)")
			}
			for _, s := range stopped {
				fmt.Fprintf(w, "stopped %s\n", s.Node)
			}
			return nil
		},
	}
}

func newWsStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "List running instances, their ports and who holds them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, err := workspace.NewManager(appFrom(cmd))
			if err != nil {
				return err
			}
			rows, err := mgr.Status(cmd.Context())
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if jsonOut {
				if rows == nil {
					rows = []workspace.Row{}
				}
				return printJSON(w, rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(w, "no instances")
				return nil
			}
			printInstances(w, rows)
			return nil
		},
	}
}

// blockedPoll is how often a foreground workspace checks whether a requirement died.
const blockedPoll = time.Second

func printInstances(w io.Writer, rows []workspace.Row) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, r := range rows {
		state := r.State
		switch {
		case !r.Alive && r.Kind != "oneshot":
			state = "dead"
		case r.BlockedBy != "":
			state = "blocked by " + r.BlockedBy
		}
		port := "-"
		if r.Port > 0 {
			port = fmt.Sprint(r.Port)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\tsince %s\theld by %d\n", r.Node, state, port, r.Tree, r.StartedAt.Format(time.TimeOnly), len(r.Holders))
	}
	tw.Flush()
}

func newWsRestartCmd() *cobra.Command {
	var tf treeFlags
	cmd := &cobra.Command{
		Use:   "restart <node> [worktree]",
		Short: "Restart a node and everything that requires it",
		Long: `Stops the node's instance and every instance of the workspace that requires it
(dependents first), then starts them again in dependency order and waits until
each is ready. Other instances keep running, and everyone holding the restarted
instances still holds them. Use it on a node the services strip shows blocked:
naming a blocked node's blocker restarts the whole subtree.

Dependents that belong to another workspace's plan are not restarted from here.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			trees, err := tf.options()
			if err != nil {
				return err
			}
			a := appFrom(cmd)
			loc, err := workspace.Locate(a.Root, worktreeArg(args[1:]))
			if err != nil {
				return err
			}
			mgr, err := workspace.NewManager(a)
			if err != nil {
				return err
			}
			plan, err := mgr.Plan(cmd.Context(), loc, workspace.PlanOptions{Workspace: tf.workspace, Trees: trees})
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			opts := workspace.UpOptions{Holder: loc.Tree, Prepared: auditPrepare(a, loc.Project)}
			if !jsonOut {
				opts.Progress = func(r workspace.Result) { fmt.Fprintln(w, "  "+r.Describe()) }
			}
			results, err := mgr.Restart(cmd.Context(), plan, args[0], opts)
			if jsonOut {
				if results == nil {
					results = []workspace.Result{}
				}
				if perr := printJSON(w, results); perr != nil {
					return perr
				}
			}
			if err != nil {
				return err
			}
			for _, r := range results {
				if r.Outcome != workspace.OutcomeReady {
					return withCode(1, fmt.Errorf("%s did not come back: %s", r.Node, r.Detail))
				}
			}
			return nil
		},
	}
	tf.add(cmd)
	return cmd
}

func newWsLayoutCmd() *cobra.Command {
	var (
		tf       treeFlags
		layout   string
		services bool
	)
	cmd := &cobra.Command{
		Use:   "layout [worktree]",
		Short: "Print the zellij layout (KDL) a worktree's tab would use",
		Long: `Prints the layout of the worktree's workspace tab. --services prints the layout
of its background services tab instead (nothing when the workspace opens none:
services = "manual", or no install hook and no service).`,
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
			opts := []workspace.OpenOption{workspace.WithWorkspace(tf.workspace), workspace.WithPins(trees.Pins), workspace.WithItemTrees(trees.ItemTrees), cachedItems(a)}
			if layout != "" {
				opts = append(opts, workspace.WithLayout(layout))
			}
			render := workspace.RenderLayout
			if services {
				render = func(ctx context.Context, a *app.App, project, repo, worktree string, opts ...workspace.OpenOption) (string, error) {
					kdl, ok, err := workspace.RenderServicesLayout(ctx, a, project, repo, worktree, opts...)
					if err == nil && !ok {
						err = errors.New("this workspace opens no services tab")
					}
					return kdl, err
				}
			}
			kdl, err := render(cmd.Context(), a, loc.Project, loc.Repo, loc.Tree, opts...)
			if err != nil {
				return err
			}
			_, err = io.WriteString(cmd.OutOrStdout(), kdl)
			return err
		},
	}
	tf.add(cmd)
	cmd.Flags().StringVar(&layout, "layout", "", "code | agent | review | manual (default: the workspace's layout)")
	cmd.Flags().BoolVar(&services, "services", false, "print the background services tab's layout instead")
	return cmd
}

// --- ade task run ----------------------------------------------------------

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "Run-graph tasks"}
	cmd.AddCommand(newTaskRunCmd())
	return cmd
}

func newTaskRunCmd() *cobra.Command {
	var (
		tf       treeFlags
		worktree string
		project  string
		noFollow bool
		gates    = taskGates{timeout: workspace.DefaultWaitTimeout}
	)
	cmd := &cobra.Command{
		Use:   "run <node>",
		Short: "Run one node with its requirements and follow its output (panes and VS Code tasks)",
		Long: `Brings up <node> (<repo>.<task> or services.<name>) and everything it requires,
sharing instances with other workspaces, prints "ade: ready <node>" once it is
ready, then follows its output. When this command ends (Ctrl-C, or the node
exits) it releases its hold; instances nobody else uses stop.

--no-follow returns after readiness. A service started that way stays up until
"ade ws down"; a oneshot has simply finished.

--after-install waits until the install hooks of the worktrees the node runs in
have succeeded (ade ws install). --wait-requires starts only the node itself:
it waits until each service it requires is running and ready, started by its
own pane or another workspace. The services tab's panes use both. A node that
is already running for another workspace is not started twice: the pane says
so, keeps holding it and follows its log.

A wait says every 30 seconds that it is still waiting, and what for. It ends
with an error when the install or the required service failed (the pane that
ran it recorded why), and after --wait-timeout (default 10m).

--project <name> runs the node for the project instead of a worktree: each repo
resolves to its baseline tree (or a --with pin) and the instance is held by
"project:<name>". The standalone services tab (ade services open) uses it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if gates.timeout <= 0 {
				return fmt.Errorf("--wait-timeout %s: want a positive duration", gates.timeout)
			}
			trees, err := tf.options()
			if err != nil {
				return err
			}
			if project != "" && cmd.Flags().Changed("worktree") {
				return errors.New("--project and --worktree are exclusive")
			}
			return taskRun(cmd, appFrom(cmd), args[0], worktree, project, tf, trees, noFollow, gates)
		},
	}
	tf.add(cmd)
	cmd.Flags().StringVar(&worktree, "worktree", ".", "the workspace's worktree")
	cmd.Flags().StringVar(&project, "project", "", "run for this project instead of a worktree (repos resolve to their baselines)")
	cmd.Flags().BoolVar(&noFollow, "no-follow", false, "return after readiness instead of following the output")
	cmd.Flags().BoolVar(&gates.afterInstall, "after-install", false, "wait for the install hooks of the node's worktrees first")
	cmd.Flags().BoolVar(&gates.waitRequires, "wait-requires", false, "wait for required services started elsewhere instead of starting them")
	cmd.Flags().DurationVar(&gates.timeout, "wait-timeout", gates.timeout, "with --after-install and --wait-requires: give up a wait after this long")
	return cmd
}

// taskGates are the waits `ade task run` can put before the start.
type taskGates struct {
	afterInstall, waitRequires bool
	timeout                    time.Duration // of each wait
}

func taskRun(cmd *cobra.Command, a *app.App, node, worktree, project string, tf treeFlags, trees workspace.TreeOptions, noFollow bool, gates taskGates) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var (
		loc workspace.Location
		err error
	)
	if project != "" {
		loc, err = workspace.LocateProject(a.Root, project)
	} else {
		loc, err = workspace.Locate(a.Root, worktree)
	}
	if err != nil {
		return err
	}
	mgr, err := workspace.NewManager(a)
	if err != nil {
		return err
	}
	plan, err := mgr.Plan(ctx, loc, workspace.PlanOptions{Workspace: tf.workspace, Nodes: []string{node}, Trees: trees})
	if err != nil {
		return err
	}
	self, _ := plan.Instance(node)
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, workspace.StartMarker(node))
	if gates.afterInstall {
		targets := mgr.InstallTargets(loc, plan, false)
		err := mgr.WaitInstalled(ctx, targets, workspace.InstallWait{Timeout: gates.timeout, Waiting: func(pending []workspace.InstallTarget, elapsed time.Duration) {
			for _, t := range pending {
				if elapsed == 0 {
					fmt.Fprintf(w, "ade: waiting for the install of %s (%s); if nothing is installing it, run: %s\n", t.Repo, t.Tree, installHint(loc))
				} else {
					fmt.Fprintf(w, "ade: still waiting for the install of %s (%s) %s\n", t.Repo, t.Tree, elapsed.Round(time.Second))
				}
			}
		}})
		if err != nil {
			return ignoreInterrupt(ctx, err)
		}
	}

	tel := newWsTelemetry(ctx, a, loc, tf.workspace)
	defer tel.close()
	opts := workspace.UpOptions{Holder: loc.Holder() + "#task:" + node, Prepared: auditPrepare(a, loc.Project), Progress: func(r workspace.Result) {
		tel.result(r)
		fmt.Fprintln(w, "  "+r.Describe())
	}}
	if !noFollow || self.Node.Oneshot() {
		opts.HolderPID = os.Getpid() // released when this command ends, even by a crash
	}
	var results []workspace.Result
	if gates.waitRequires {
		wait := workspace.WaitOptions{Timeout: gates.timeout, Waiting: func(req, probe string, elapsed time.Duration) {
			if elapsed == 0 {
				fmt.Fprintf(w, "ade: waiting for %s (%s) to be ready; it starts in its own pane\n", req, probe)
			} else {
				fmt.Fprintf(w, "ade: still waiting for %s (%s) %s\n", req, probe, elapsed.Round(time.Second))
			}
		}}
		results, err = mgr.UpWaiting(ctx, plan, opts, wait)
	} else {
		results, err = mgr.Up(ctx, plan, opts)
	}
	if len(results) == 0 {
		return ignoreInterrupt(ctx, err)
	}
	last := results[len(results)-1] // the plan is in start order: the requested node is last
	if err != nil || last.Outcome != workspace.OutcomeReady {
		tel.record(workspace.RunFailed, results)
		mgr.Release(context.WithoutCancel(ctx), opts.Holder) //nolint:errcheck // the failure below is the news
		if err != nil {
			return err
		}
		return withCode(1, fmt.Errorf("%s: %s", node, last.Detail))
	}
	if last.Shared {
		fmt.Fprintf(w, "ade: %s already running (pid %d), started for another workspace or pane; this pane holds it and shows its log\n", node, last.PID)
	}
	fmt.Fprintf(w, "%s (port %d)\n", workspace.ReadyMarker(node), last.Port)
	if noFollow || self.Node.Oneshot() {
		if self.Node.Oneshot() {
			mgr.Release(context.WithoutCancel(ctx), opts.Holder) //nolint:errcheck
			tel.record(workspace.RunStopped, results)
		} else {
			tel.record(workspace.RunDetached, results)
		}
		return nil
	}
	tel.record(workspace.RunRunning, results)
	fmt.Fprintf(w, "ade: %s runs in this pane · Ctrl-C stops it · Enter starts it again once it has exited\n", node)
	exited := func() bool { return !mgr.Running(self.ID) }
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go mgr.WatchBlocked(watchCtx, []string{self.ID}, blockedPoll, func(r workspace.Row) {
		tel.publish(workspace.BlockedEvent(r))
		fmt.Fprintf(w, "ade: %s blocked: %s is not running\n", r.Node, r.BlockedBy)
	})
	followErr := workspace.Follow(ctx, self.Log, w, exited)
	relCtx := context.WithoutCancel(ctx)
	_, relErr := mgr.Release(relCtx, opts.Holder)
	tel.record(workspace.RunStopped, results)
	switch {
	case followErr != nil:
		return followErr
	case ctx.Err() != nil:
		return relErr // interrupted by the user: a normal way to stop
	case exited():
		tel.publish(workspace.ExitEvent(loc.Project, node, self.Log))
		return withCode(1, fmt.Errorf("%s exited", node))
	}
	return relErr
}

// ignoreInterrupt turns the cancellation a user's Ctrl-C causes into a clean
// exit; any other error stays.
func ignoreInterrupt(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return nil
	}
	return err
}

// installHint is the command that runs the install hooks for what loc runs.
func installHint(loc workspace.Location) string {
	if loc.Tree == "" {
		return "ade services install --project " + loc.Project
	}
	return "ade ws install " + loc.Tree
}

// auditPrepare records each prepare command of a node as git.hook with
// hook=prepare, like install commands.
func auditPrepare(a *app.App, project string) func(in *workspace.Instance, command string, err error) {
	return func(in *workspace.Instance, command string, err error) {
		e := audit.Entry{
			Action: "git.hook", Target: in.Dir, Project: project, Outcome: "ok",
			Params: map[string]any{"hook": "prepare", "node": in.Node.ID, "cmd": command},
		}
		if err != nil {
			e.Outcome, e.Error = "error", err.Error()
		}
		_ = a.Audit.Append(e) // an unwritable audit log must not stop a start
	}
}
