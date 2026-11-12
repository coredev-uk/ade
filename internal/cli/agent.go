package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/agent"
	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/records"
)

func init() { register(newAgentCmd) }

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Plan, run and review coding-agent work on a started item (omp, claude, codex)",
		Long: "An agent works in the item's start-work worktree (ade start). The flow is:\n\n" +
			"  ade agent plan <tracker-ref>   read-only plan from the issue, comments and AGENTS.md\n" +
			"  ade agent approve <run>        your yes to the plan (edit plan.md first if you like)\n" +
			"  ade agent run <run>            implement the approved plan in the worktree\n" +
			"  ade agent diff <run>           what changed (--patch for the full diff)\n" +
			"  ade agent review <run>         lazygit on the worktree\n" +
			"  ade agent push <run>           commit (synthetic author) and push the branch\n\n" +
			"`ade agent run <run> --ai-branch` (or --background, detached) implements on a separate\n" +
			"<branch>-ai branch and worktree instead, so your own worktree stays yours; then\n" +
			"`ade agent accept <run>` fast-forwards the item's branch to the result (refusing if it\n" +
			"moved on) and `ade agent discard <run>` throws the -ai branch away. Neither pushes.\n\n" +
			"`ade agent address <tracker-ref>` takes the MR's unresolved threads, edits code and drafts\n" +
			"replies into the review drafts: nothing is posted until you triage and submit them.\n\n" +
			"Every run has a hard cost cap (budget.agent_run_usd, lowered with --max-usd): the agent\n" +
			"process is killed when its reported cost passes it. The transcript lives only in the run's\n" +
			"directory in the item record.",
	}
	cmd.AddCommand(
		newAgentPlanCmd(), newAgentApproveCmd(), newAgentRunCmd(), newAgentAddressCmd(),
		newAgentDiffCmd(), newAgentReviewCmd(), newAgentPushCmd(),
		newAgentListCmd(), newAgentShowCmd(), newAgentCancelCmd(), newAgentAcceptCmd(), newAgentDiscardCmd(),
	)
	return cmd
}

// agentSession is the manager and the inbox hook of one command.
type agentSession struct {
	m    *agent.Manager
	stop func()
}

func newAgentSession(cmd *cobra.Command) (*agentSession, error) {
	a := appFrom(cmd)
	m, err := agent.ForApp(a)
	if err != nil {
		return nil, err
	}
	// Events reach the inbox: the TUI shows them from there, whichever
	// process raised them.
	stop, err := notify.Attach(cmd.Context(), a, notify.Channels{})
	if err != nil {
		return nil, err
	}
	return &agentSession{m: m, stop: stop}, nil
}

type agentStartFlags struct {
	repo, driver, profile string
	maxUSD                float64
	yes, dryRun           bool
	noNotes               bool
}

func (f *agentStartFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.repo, "repo", "", "repo of the item to work in (needed when the app labels route to several)")
	fl.StringVar(&f.driver, "driver", "", "agent CLI: omp | claude | codex (default: the profile's agent)")
	fl.StringVar(&f.profile, "profile", "", "AI profile (default: the active one)")
	fl.Float64Var(&f.maxUSD, "max-usd", 0, "lower the run's cost cap below budget.agent_run_usd")
	fl.BoolVar(&f.yes, "yes", false, "skip the pre-flight confirmation")
	fl.BoolVar(&f.dryRun, "dry-run", false, "show the target and the pre-flight; start nothing, spend nothing")
	fl.BoolVar(&f.noNotes, "no-notes", false, "leave your notes on the item out of the prompts (they are included by default)")
}

func (f agentStartFlags) request(kind agent.Kind, t agent.Target, c agent.Context) agent.Request {
	return agent.Request{Kind: kind, Target: t, Context: c, Profile: f.profile, Driver: f.driver, MaxUSD: f.maxUSD, Assume: f.yes, DryRun: f.dryRun}
}

func newAgentPlanCmd() *cobra.Command {
	var f agentStartFlags
	cmd := &cobra.Command{
		Use:   "plan <tracker-ref>",
		Short: "Ask an agent for a read-only plan (needs the item's worktree: ade start)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentStart(cmd, args[0], f, agent.KindPlan)
		},
	}
	f.bind(cmd)
	return cmd
}

func newAgentAddressCmd() *cobra.Command {
	var f agentStartFlags
	cmd := &cobra.Command{
		Use:   "address <tracker-ref>",
		Short: "Edit code for the MR's unresolved threads and draft replies (pending review drafts)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentStart(cmd, args[0], f, agent.KindAddress)
		},
	}
	f.bind(cmd)
	return cmd
}

func runAgentStart(cmd *cobra.Command, refArg string, f agentStartFlags, kind agent.Kind) error {
	ctx := cmd.Context()
	t, fg, err := agent.ResolveTarget(ctx, appFrom(cmd), nil, refArg, f.repo)
	if err != nil {
		return err
	}
	c, err := agent.Gather(ctx, fg, t)
	if err != nil {
		return err
	}
	if !f.noNotes {
		if err := c.AddNotes(records.New(appFrom(cmd).RecordsDir), t); err != nil {
			return err
		}
	}
	s, err := newAgentSession(cmd)
	if err != nil {
		return err
	}
	defer s.stop()
	req := f.request(kind, t, c)
	if !f.yes {
		req.Confirm = confirmPreflight(cmd)
	}
	r, pf, err := s.m.New(ctx, req)
	w := cmd.OutOrStdout()
	if err != nil {
		return err
	}
	if f.dryRun {
		if jsonOut {
			return printJSON(w, map[string]any{"target": t, "preflight": pf, "dry_run": true, "includes_notes": c.Notes != ""})
		}
		inc := ""
		if c.Notes != "" {
			inc = " · includes your notes"
		}
		fmt.Fprintf(w, "dry run: %s in %s (%s)\n  estimate of the first call $%.4f (priced: %v)%s; nothing was started\n", kind, t.Worktree, t.Branch, pf.EstimateUSD, pf.Priced, inc)
		return nil
	}
	if kind == agent.KindPlan {
		r, err = s.m.Plan(ctx, r.ID)
	} else {
		r, err = s.m.Address(ctx, r.ID)
	}
	return printRun(cmd, s, r, err)
}

// confirmPreflight asks on the terminal when the pre-flight wants a yes.
func confirmPreflight(cmd *cobra.Command) func(ai.Preflight) (bool, error) {
	return func(pf ai.Preflight) (bool, error) {
		if jsonOut || !isTerminal(os.Stdin) {
			return false, &ai.ErrNeedsConfirm{Preflight: pf}
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: continue? [y/N] ", strings.Join(pf.Confirm, "; "))
		var ans string
		fmt.Fscanln(os.Stdin, &ans) //nolint:errcheck // an empty answer is a no
		return strings.HasPrefix(strings.ToLower(ans), "y"), nil
	}
}

// printRun reports a run after a step. err is the step's error: the run is
// printed anyway, so the user sees its state and spend.
func printRun(cmd *cobra.Command, s *agentSession, r agent.Run, stepErr error) error {
	w := cmd.OutOrStdout()
	if jsonOut {
		if err := printJSON(w, r); err != nil {
			return err
		}
		return stepErr
	}
	fmt.Fprintf(w, "run %s · %s · %s · $%.4f of $%.2f\n", r.ID, r.Item(), r.State, r.CostUSD, r.CapUSD)
	if r.State == agent.StateAwaiting {
		if plan, err := s.m.PlanText(r); err == nil {
			fmt.Fprintf(w, "\n%s\nplan: %s\napprove with: ade agent approve %s\n", strings.TrimRight(plan, "\n"), filepath.Join(s.m.Store.Dir(r), agent.FilePlan), r.ID)
		}
	}
	if r.State == agent.StateReview {
		switch {
		case r.Isolated():
			fmt.Fprintf(w, "the changes are on %s in %s\nreview with: ade agent diff %s · ade agent review %s · then ade agent accept %s (fast-forwards %s) or ade agent discard %s\n",
				r.AIBranch, r.AIWorktree, r.ID, r.ID, r.ID, r.Branch, r.ID)
		case !r.AcceptedAt.IsZero():
			fmt.Fprintf(w, "accepted into %s in %s: nothing is pushed yet · ade agent diff %s · ade agent push %s\n", r.Branch, r.Worktree, r.ID, r.ID)
		default:
			fmt.Fprintf(w, "review with: ade agent diff %s · ade agent review %s · then ade agent push %s\n", r.ID, r.ID, r.ID)
		}
		if r.Drafts > 0 {
			fmt.Fprintf(w, "%d reply drafts are pending: ade review list\n", r.Drafts)
		}
	}
	if r.Isolated() && r.State.Terminal() {
		fmt.Fprintf(w, "the -ai branch %s is kept in %s: ade agent diff %s · ade agent discard %s\n", r.AIBranch, r.AIWorktree, r.ID, r.ID)
	}
	if r.Error != "" && stepErr == nil {
		fmt.Fprintln(w, "error:", r.Error)
	}
	return stepErr
}

func newAgentApproveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "approve <run>",
		Short: "Approve the run's plan as it is now (plan.md may be edited first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAgentSession(cmd)
			if err != nil {
				return err
			}
			defer s.stop()
			r, err := s.m.Approve(args[0])
			if err != nil {
				return err
			}
			return printRun(cmd, s, r, nil)
		},
	}
}

func newAgentRunCmd() *cobra.Command {
	var apart, background bool
	cmd := &cobra.Command{
		Use:   "run <run>",
		Short: "Implement the approved plan in the item's worktree (or, with --ai-branch, on a separate branch)",
		Long: "Implements the approved plan with the agent, in the item's worktree.\n\n" +
			"--ai-branch starts a new <branch>-ai branch and worktree at the item's current commit and\n" +
			"lets the agent work there, so your own worktree stays yours: it may be dirty and you may\n" +
			"keep committing. When the run is in review, `ade agent accept` fast-forwards the item's\n" +
			"branch to the result (and refuses if the branch has moved on since), `ade agent discard`\n" +
			"throws the -ai branch away. Nothing is pushed either way. The -ai worktree is a fresh\n" +
			"checkout: ignored files of your worktree (node_modules, .env) are not in it.\n\n" +
			"--background starts the run as a detached process (it implies --ai-branch: the agent must\n" +
			"not share the tree you keep working in) and returns at once. Its output goes to\n" +
			"background.log in the run's directory, and the inbox reports when it is ready for review.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAgentSession(cmd)
			if err != nil {
				return err
			}
			defer s.stop()
			if background {
				return startBackgroundRun(cmd, s, args[0])
			}
			implement := s.m.Implement
			if apart {
				implement = s.m.ImplementApart
			}
			r, err := implement(cmd.Context(), args[0])
			return printRun(cmd, s, r, err)
		},
	}
	cmd.Flags().BoolVar(&apart, "ai-branch", false, "work on a separate <branch>-ai branch and worktree; accept or discard it afterwards")
	cmd.Flags().BoolVar(&background, "background", false, "start the run detached and return at once (implies --ai-branch)")
	return cmd
}

func newAgentAcceptCmd() *cobra.Command {
	var msg string
	cmd := &cobra.Command{
		Use:   "accept <run>",
		Short: "Take a --ai-branch run's work into the item's branch (fast-forward only; nothing is pushed)",
		Long: "Commits what the agent left in the -ai worktree (synthetic author, as `agent push` does),\n" +
			"fast-forwards the item's branch to that commit and removes the -ai worktree and branch.\n" +
			"It refuses, changing nothing on the item's branch, when the branch has commits the run did\n" +
			"not start from: the work stays committed on the -ai branch for you to rebase or merge by\n" +
			"hand, or to discard. The run stays in review: `ade agent push` sends the branch.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAgentSession(cmd)
			if err != nil {
				return err
			}
			defer s.stop()
			r, err := s.m.Accept(cmd.Context(), args[0], agent.AcceptOpts{Message: msg})
			if err != nil {
				return err
			}
			return printRun(cmd, s, r, nil)
		},
	}
	cmd.Flags().StringVarP(&msg, "message", "m", "", "commit message (default: the issue title and the run id)")
	return cmd
}

func newAgentDiscardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "discard <run>",
		Short: "Throw a --ai-branch run's work away: removes the -ai worktree and branch",
		Long: "Removes the -ai worktree (with whatever the agent left in it) and the -ai branch, and\n" +
			"cancels a run still in review. The item's own branch and worktree are not touched. Also\n" +
			"cleans up after an --ai-branch run that failed or was cancelled.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAgentSession(cmd)
			if err != nil {
				return err
			}
			defer s.stop()
			r, err := s.m.Discard(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printRun(cmd, s, r, nil)
		},
	}
}

// adeExecutable is the binary --background starts; tests replace it.
var adeExecutable = os.Executable

// startBackgroundRun checks the run can be implemented, then starts
// `ade agent run <id> --ai-branch` in its own session with its output in the
// run's directory, and returns without waiting for it.
func startBackgroundRun(cmd *cobra.Command, s *agentSession, id string) error {
	if err := s.m.CanImplement(id); err != nil {
		return err
	}
	r, err := s.m.Store.Get(id)
	if err != nil {
		return err
	}
	exe, err := adeExecutable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(s.m.Store.Dir(r), "background.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	child := exec.Command(exe, "agent", "run", r.ID, "--ai-branch")
	child.Stdout, child.Stderr = logf, logf
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return fmt.Errorf("start %s: %w", exe, err)
	}
	pid := child.Process.Pid
	go child.Wait() //nolint:errcheck // reaps the child if this process outlives it; its state is the run's
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), map[string]any{"run": r.ID, "pid": pid, "log": logPath, "background": true, "ai_branch": r.Branch + agent.AIBranchSuffix})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "run %s implementing in the background (pid %d) on %s%s\nlog: %s\nwatch with: ade agent show %s · the inbox says when it is ready; then ade agent accept %s or ade agent discard %s\n",
		r.ID, pid, r.Branch, agent.AIBranchSuffix, logPath, r.ID, r.ID, r.ID)
	return nil
}

func newAgentDiffCmd() *cobra.Command {
	var patch bool
	cmd := &cobra.Command{
		Use:   "diff <run>",
		Short: "What the agent changed in the worktree (new files included; the index is untouched)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAgentSession(cmd)
			if err != nil {
				return err
			}
			defer s.stop()
			d, r, err := s.m.Diff(cmd.Context(), args[0], patch || jsonOut)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, d)
			}
			if d.Empty() {
				fmt.Fprintf(w, "run %s: no changes\n", r.ID)
				return nil
			}
			fmt.Fprint(w, d.Stat)
			if patch {
				fmt.Fprintln(w)
				fmt.Fprint(w, d.Patch)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&patch, "patch", false, "print the full unified diff")
	return cmd
}

func newAgentReviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "review <run>",
		Short: "Open lazygit on the run's worktree (a floating zellij pane inside zellij)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAgentSession(cmd)
			if err != nil {
				return err
			}
			defer s.stop()
			r, err := s.m.Store.Get(args[0])
			if err != nil {
				return err
			}
			lg := agent.LazygitCmd(r.WorkDir(), os.Getenv("ZELLIJ") != "")
			lg.Stdin, lg.Stdout, lg.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
			if err := lg.Run(); err != nil {
				return fmt.Errorf("%s: %w", lg.Args[0], err)
			}
			return nil
		},
	}
}

func newAgentPushCmd() *cobra.Command {
	var msg string
	cmd := &cobra.Command{
		Use:   "push <run>",
		Short: "Commit the reviewed diff (synthetic author from git config) and push the branch",
		Long: "Your approval of the diff made real: everything in the worktree is committed, as the\n" +
			"configured git user marked \"(ade agent)\", and the branch is pushed. The merge request,\n" +
			"which tracks the branch, updates by itself.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAgentSession(cmd)
			if err != nil {
				return err
			}
			defer s.stop()
			r, err := s.m.Push(cmd.Context(), args[0], agent.PushOpts{Message: msg})
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), r)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "run %s pushed %s to %s (%s)\n", r.ID, strings.Join(r.Commits, ","), r.Branch, r.MR)
			return nil
		},
	}
	cmd.Flags().StringVarP(&msg, "message", "m", "", "commit message (default: the issue title and the run id)")
	return cmd
}

func newAgentCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <run>",
		Short: "Stop the run's agent process and mark the run cancelled",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAgentSession(cmd)
			if err != nil {
				return err
			}
			defer s.stop()
			r, err := s.m.Cancel(args[0])
			if err != nil {
				return err
			}
			return printRun(cmd, s, r, nil)
		},
	}
}

func newAgentShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <run>",
		Short: "A run's state, spend and files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAgentSession(cmd)
			if err != nil {
				return err
			}
			defer s.stop()
			r, err := s.m.Store.Get(args[0])
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), r)
			}
			printRunDetail(cmd.OutOrStdout(), s.m.Store.Dir(r), r)
			return nil
		},
	}
}

func printRunDetail(w io.Writer, dir string, r agent.Run) {
	fmt.Fprintf(w, "run %s · %s · %s\n", r.ID, r.Kind, r.State)
	fmt.Fprintf(w, "  item      %s (%s)\n  worktree  %s on %s\n", r.Item(), r.Repo, r.Worktree, r.Branch)
	if r.Isolated() {
		fmt.Fprintf(w, "  -ai       %s in %s (ade agent accept | discard)\n", r.AIBranch, r.AIWorktree)
	}
	fmt.Fprintf(w, "  driver    %s · %s\n  cost      $%.4f of $%.2f cap\n  dir       %s\n", r.Driver, r.Model, r.CostUSD, r.CapUSD, dir)
	for _, c := range r.Calls {
		fmt.Fprintf(w, "  call      %-8s exit %d · $%.4f · %d turns %s\n", c.Kind, c.Exit, c.CostUSD, c.Turns, c.StoppedBy)
	}
	if r.Error != "" {
		fmt.Fprintf(w, "  error     %s\n", r.Error)
	}
}

func newAgentListCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Agent runs, newest first: state, spend, time",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := newAgentSession(cmd)
			if err != nil {
				return err
			}
			defer s.stop()
			rs, err := s.m.Store.List()
			if err != nil {
				return err
			}
			if !all {
				live := rs[:0:0]
				for _, r := range rs {
					if !r.State.Terminal() || r.Isolated() { // a finished run that still holds a -ai branch needs accept or discard
						live = append(live, r)
					}
				}
				rs = live
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), rs)
			}
			if len(rs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no agent runs (--all includes finished ones)")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "RUN\tITEM\tKIND\tSTATE\tCOST\tTIME")
			now := time.Now()
			for _, r := range rs {
				state := string(r.State)
				if r.Isolated() {
					state += " (-ai)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t$%.4f/$%.2f\t%s\n", r.ID, r.Item(), r.Kind, state, r.CostUSD, r.CapUSD, r.Elapsed(now).Round(time.Second))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include finished (pushed, failed, cancelled) runs")
	return cmd
}
