package cli

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/workspace"
)

func init() {
	register(newStartCmd)
	mrExtraCmds = append(mrExtraCmds, newMRCreateCmd)
}

// mrExtraCmds are subcommands other files add to `ade mr` (see newMRCmd).
var mrExtraCmds []func() *cobra.Command

// startFlags are the choices both `ade start` and `ade mr create` take.
type startFlags struct {
	repos       []string
	branches    []string
	mode        string
	child       string
	template    string
	strip       []string
	bodyFile    string
	moveLabel   bool
	noLabel     bool
	noWorkspace bool
	layout      string
	dryRun      bool
	session     string // zellij session for the tab; verify scripts only
	onlyChild   bool   // `ade issue child`: just the child issue and its links
}

func (f *startFlags) bind(cmd *cobra.Command, withMode bool) {
	fl := cmd.Flags()
	fl.StringSliceVar(&f.repos, "repo", nil, "manifest repo to work in (repeatable); default: the repos the app labels route to")
	if withMode {
		fl.StringArrayVar(&f.branches, "branch", nil, "work on this branch in a repo instead of the derived name: repo=branch (repeatable); an existing branch and its MR are reused, and the repo is added to the work")
		fl.StringVar(&f.mode, "mode", "", "draft (child, branch, draft MR) | branch (no MR yet) | browse (no forge writes); default from conventions.mr.create")
		fl.BoolVar(&f.moveLabel, "move-label", false, "confirm the workflow label move when labels.workflow.sync = ask")
		fl.BoolVar(&f.noLabel, "no-move-label", false, "do not move the workflow label")
		fl.BoolVar(&f.noWorkspace, "no-workspace", false, "do not open a workspace tab")
		fl.StringVar(&f.layout, "layout", "", "main pane of the workspace tab: code (editor) | agent (omp with the ADE implement skill) | manual (shell); default: host.layout, then the manifest's workspace, then code")
		fl.StringVar(&f.session, "session", "", "zellij session to open the tab in (default: ade; for tests)")
		_ = fl.MarkHidden("session")
	}
	fl.StringVar(&f.child, "child", "", "child issue: auto | always | never; default from conventions.child_issue")
	fl.StringVar(&f.template, "template", "", "MR template name (file in .gitlab/merge_request_templates, without .md)")
	fl.StringSliceVar(&f.strip, "strip-actions", nil, "remove quick actions from the MR body: names (assign_reviewer,label) or all when given without a value")
	cmd.Flags().Lookup("strip-actions").NoOptDefVal = "*"
	fl.StringVar(&f.bodyFile, "mr-body-file", "", "use this file as the MR body instead of the template (child reference: "+startwork.ChildPlaceholder+")")
}

func newStartCmd() *cobra.Command {
	var f startFlags
	cmd := &cobra.Command{
		Use:   "start <tracker-ref>",
		Short: "Start work on a tracker issue: child issue, branch, draft MR, worktree",
		Long: "Starts work on a tracker issue (PLAN 6.3). The app labels route it to repos; in each repo\n" +
			"ade creates a child issue (copying type, assignee, milestone/iteration), links it to the\n" +
			"tracker issue, moves the tracker's workflow label, creates the branch and a draft MR from\n" +
			"the repo's template, then adds the worktree and opens the workspace.\n\n" +
			"It is idempotent: an existing child, branch or MR is found and reused, so a re-run creates\n" +
			"nothing and a failed run resumes. --dry-run shows every write, and the MR body, first.\n" +
			"<tracker-ref> is group/project#N, or N when one project is registered.",
		Example: "  ade start acme/tracker#128\n  ade start 128 --repo api --mode branch\n  ade start 128 --repo api --branch web=fix-retry-cache\n  ade start 128 --dry-run --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.dryRun, _ = cmd.Flags().GetBool("dry-run")
			return runStart(cmd, args[0], f, false)
		},
	}
	f.bind(cmd, true)
	cmd.Flags().Bool("dry-run", false, "show every write and the MR body; change nothing")
	return cmd
}

func newMRCreateCmd() *cobra.Command {
	var f startFlags
	cmd := &cobra.Command{
		Use:   "create <tracker-ref> --repo <repo>",
		Short: "Open the MR for work started with --mode branch, from the repo's template",
		Long: "Creates the draft MR for a tracker issue whose branch exists (ade start --mode branch).\n" +
			"The body is the repo's MR template with Closes and the parent reference filled in;\n" +
			"quick actions in the template are listed in --dry-run and can be stripped with\n" +
			"--strip-actions. Nothing but the MR is created.",
		Example: "  ade mr create acme/tracker#128 --repo api\n  ade mr create 128 --repo api --strip-actions assign_reviewer",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.dryRun, _ = cmd.Flags().GetBool("dry-run")
			if len(f.repos) != 1 {
				return errors.New("--repo <repo> is required (exactly one)")
			}
			return runStart(cmd, args[0], f, true)
		},
	}
	f.bind(cmd, false)
	return cmd
}

func runStart(cmd *cobra.Command, arg string, f startFlags, onlyMR bool) error {
	a := appFrom(cmd)
	ctx := cmd.Context()
	r, err := startwork.Resolve(ctx, a, nil, arg)
	if err != nil {
		return err
	}
	ch, err := f.choices(r)
	if err != nil {
		return err
	}
	ch.OnlyMR, ch.OnlyChild = onlyMR, f.onlyChild
	if onlyMR {
		ch.Mode = startwork.ModeDraft
	}
	env := newStartEnv(a, f.noWorkspace || onlyMR || f.onlyChild, f.session)
	env.Forge = r.Forge
	_, res, err := env.Start(ctx, r, ch, f.dryRun)
	if !f.dryRun && res.Created() > 0 {
		// The new child and MR belong in the cache now: a running dashboard
		// or `ade item --watch` shows them without a sync.
		if _, rerr := forgesync.New(a).RefreshItem(ctx, r.Manifest, r.Forge, r.Item.Ref); rerr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "ade: cache not refreshed (the next sync will): %v\n", rerr)
		}
	}
	if jsonOut {
		if perr := printJSON(cmd.OutOrStdout(), res); perr != nil {
			return perr
		}
		return err
	}
	printStart(cmd.OutOrStdout(), r, res)
	return err
}

// choices turns the flags into startwork.Choices over the manifest defaults.
func (f startFlags) choices(r *startwork.Resolved) (startwork.Choices, error) {
	ch := startwork.DefaultChoices(r.Manifest, r.Item)
	if len(f.repos) > 0 {
		ch.Repos = f.repos
	}
	if f.mode != "" {
		m, err := startwork.ParseMode(f.mode)
		if err != nil {
			return ch, err
		}
		ch.Mode = m
	}
	if f.child != "" {
		p, err := startwork.ParseChildPolicy(f.child)
		if err != nil {
			return ch, err
		}
		ch.Child = p
	}
	if f.layout != "" {
		if !workspace.ValidLayout(f.layout) || f.layout == workspace.LayoutReview {
			return ch, fmt.Errorf("--layout %q: want code, agent or manual", f.layout)
		}
		ch.Layout = f.layout
	}
	if f.moveLabel && f.noLabel {
		return ch, errors.New("--move-label and --no-move-label contradict each other")
	}
	ch.MoveLabel, ch.NoMoveLabel = f.moveLabel, f.noLabel
	if err := f.pinBranches(r.Manifest, &ch); err != nil {
		return ch, err
	}
	ch.Template = f.template
	for _, s := range f.strip {
		if s == "all" {
			s = "*"
		}
		ch.StripActions = append(ch.StripActions, s)
	}
	if f.bodyFile != "" {
		b, err := os.ReadFile(f.bodyFile)
		if err != nil {
			return ch, err
		}
		ch.MRBody = string(b)
	}
	return ch, nil
}

// pinBranches turns --branch repo=branch into ch.Branches and adds those repos
// to the chosen ones.
func (f startFlags) pinBranches(m *config.Manifest, ch *startwork.Choices) error {
	pins, err := keyValues("--branch", f.branches)
	if err != nil || pins == nil {
		return err
	}
	if ch.Mode == startwork.ModeBrowse {
		return errors.New("--branch has no effect with --mode browse")
	}
	for _, repo := range slices.Sorted(maps.Keys(pins)) {
		_, known := m.Repos[repo]
		if !known && (repo != startwork.TrackerRepo || m.Tracker.Project == "") {
			return fmt.Errorf("--branch %s=%s: unknown repo (have: %s)", repo, pins[repo], strings.Join(m.RepoNames(), ", "))
		}
		if err := startwork.ValidBranchName(pins[repo]); err != nil {
			return fmt.Errorf("--branch %s=%s: %w", repo, pins[repo], err)
		}
		if !slices.Contains(ch.Repos, repo) {
			ch.Repos = append(ch.Repos, repo)
		}
	}
	ch.Branches = pins
	return nil
}

// newStartEnv wires start work to the real git auth and the workspace host.
func newStartEnv(a *app.App, noWorkspace bool, session string) *startwork.Env {
	svc := project.New(a, "user")
	env := &startwork.Env{App: a, Git: svc.GitClient}
	if !noWorkspace {
		env.Workspace = startwork.WorkspaceOpenerIn(a, session)
	}
	return env
}

func printStart(w io.Writer, r *startwork.Resolved, res startwork.Result) {
	head := fmt.Sprintf("%s · %s", res.Item, r.Item.Issue.Title)
	if res.DryRun {
		head += " · dry run: nothing was changed"
	}
	fmt.Fprintln(w, head)
	for _, s := range res.Steps {
		line := fmt.Sprintf("  %-13s %s", glyphFor(s.Outcome), s.Text)
		if s.Ref != "" && s.Kind != startwork.StepChild && s.Kind != startwork.StepMR {
			line += " → " + s.Ref
		}
		fmt.Fprintln(w, line)
		if s.Error != "" {
			fmt.Fprintf(w, "  %-13s %s\n", "", s.Error)
		}
		if s.URL != "" && (s.Kind == startwork.StepChild || s.Kind == startwork.StepMR) {
			fmt.Fprintf(w, "  %-13s %s %s\n", "", s.Ref, s.URL)
		}
	}
	for _, p := range res.Previews {
		fmt.Fprintf(w, "\nMR body for %s (%s):\n", p.Repo, p.Title)
		for line := range strings.SplitSeq(strings.TrimRight(p.Body, "\n"), "\n") {
			fmt.Fprintln(w, "  │ "+line)
		}
		if len(p.Actions) > 0 {
			fmt.Fprintln(w, "  quick actions the forge runs on create:")
			for _, qa := range p.Actions {
				state := "kept"
				if slicesHas(p.Stripped, "*") || slicesHas(p.Stripped, qa.Name) {
					state = "stripped"
				}
				fmt.Fprintf(w, "    %-8s %s (%s)\n", state, qa.Summary, qa.Line)
			}
		}
	}
	for _, n := range res.Notes {
		fmt.Fprintln(w, "  note:", n)
	}
	if !res.DryRun {
		var tr []string
		for _, t := range res.Targets {
			if t.Worktree != "" {
				tr = append(tr, t.Worktree)
			}
		}
		if len(tr) > 0 {
			fmt.Fprintln(w, "worktree: "+strings.Join(tr, ", "))
		}
	}
}

func glyphFor(outcome string) string {
	switch outcome {
	case startwork.OutcomeCreated:
		return "+ created"
	case startwork.OutcomeExists:
		return "= exists"
	case startwork.OutcomeFailed:
		return "! failed"
	case startwork.OutcomeSkipped:
		return "- skipped"
	default:
		return "~ " + outcome
	}
}

func slicesHas(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
