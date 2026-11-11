package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
			"`ade agent address <tracker-ref>` takes the MR's unresolved threads, edits code and drafts\n" +
			"replies into the review drafts: nothing is posted until you triage and submit them.\n\n" +
			"Every run has a hard cost cap (budget.agent_run_usd, lowered with --max-usd): the agent\n" +
			"process is killed when its reported cost passes it. The transcript lives only in the run's\n" +
			"directory in the item record.",
	}
	cmd.AddCommand(
		newAgentPlanCmd(), newAgentApproveCmd(), newAgentRunCmd(), newAgentAddressCmd(),
		newAgentDiffCmd(), newAgentReviewCmd(), newAgentPushCmd(),
		newAgentListCmd(), newAgentShowCmd(), newAgentCancelCmd(),
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
		fmt.Fprintf(w, "review with: ade agent diff %s · ade agent review %s · then ade agent push %s\n", r.ID, r.ID, r.ID)
		if r.Drafts > 0 {
			fmt.Fprintf(w, "%d reply drafts are pending: ade review list\n", r.Drafts)
		}
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
	return &cobra.Command{
		Use:   "run <run>",
		Short: "Implement the approved plan in the item's worktree",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAgentSession(cmd)
			if err != nil {
				return err
			}
			defer s.stop()
			r, err := s.m.Implement(cmd.Context(), args[0])
			return printRun(cmd, s, r, err)
		},
	}
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
			lg := agent.LazygitCmd(r.Worktree, os.Getenv("ZELLIJ") != "")
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
					if !r.State.Terminal() {
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
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t$%.4f/$%.2f\t%s\n", r.ID, r.Item(), r.Kind, r.State, r.CostUSD, r.CapUSD, r.Elapsed(now).Round(time.Second))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include finished (pushed, failed, cancelled) runs")
	return cmd
}
