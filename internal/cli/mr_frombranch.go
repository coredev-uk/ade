package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/commitprefix"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/review"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/workspace"
)

func init() { mrExtraCmds = append(mrExtraCmds, newMRFromBranchCmd) }

// mrFromBranch is `ade mr from-branch --json`.
type mrFromBranch struct {
	Project string `json:"project"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Target  string `json:"target"`
	Title   string `json:"title"`
	Draft   bool   `json:"draft"`
	// Template is the MR template the body came from, when one applied.
	Template string `json:"template,omitempty"`
	// Body is the MR description, shown in dry runs.
	Body    string                  `json:"body,omitempty"`
	Actions []startwork.QuickAction `json:"quick_actions,omitempty"`
	// Stripped names the quick actions removed from the body.
	Stripped []string   `json:"stripped,omitempty"`
	Notes    []string   `json:"notes,omitempty"`
	DryRun   bool       `json:"dry_run,omitempty"`
	MR       *forge.Ref `json:"mr,omitempty"`
	URL      string     `json:"url,omitempty"`
	// Created is false when the branch had an open MR already.
	Created bool `json:"created"`
}

func newMRFromBranchCmd() *cobra.Command {
	var (
		title, template, target, closes string
		strip                           []string
		ready, dry                      bool
		forgeFlag                       string
	)
	cmd := &cobra.Command{
		Use:   "from-branch [dir]",
		Short: "Open the MR for the branch checked out in dir (default: the current directory)",
		Long: "For work started outside ADE: the branch checked out in a worktree under the projects root\n" +
			"(dir, default `.`) gets a merge request. The repo and project come from the path, the target\n" +
			"is the repo's default branch (--target), and the body is the repo's MR template (--template,\n" +
			"else conventions.mr.template) with the quick actions you strip (--strip-actions) removed,\n" +
			"preceded by `Closes <issue>` when you pass --closes. The title is the commit subject when the\n" +
			"branch has one commit, else the branch name in words (--title). The MR is a draft unless\n" +
			"conventions.mr.draft = false or --ready; the worktree's recorded commit prefix is passed\n" +
			"to the forge for the commits it creates. The branch must be pushed already; an open MR for\n" +
			"it is reported, not duplicated. Audited as forge.mr.create; --dry-run prints the MR first.",
		Example: "  cd ~/ade/acme/api/fix-retry && ade mr from-branch\n  ade mr from-branch ~/ade/acme/api/fix-retry --closes acme/api#31 --dry-run --json",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			dry, _ = cmd.Flags().GetBool("dry-run")
			res, err := openMRFromBranch(cmd.Context(), appFrom(cmd), dir, mrFromBranchOpts{
				title: title, template: template, target: target, closes: closes, strip: strip, ready: ready,
				forge: forgeFlag, dry: dry,
			})
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, res)
			}
			printMRFromBranch(w, res)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&title, "title", "", "MR title (default: the one commit's subject, else the branch name in words)")
	fl.StringVar(&template, "template", "", "MR template name (file in .gitlab/merge_request_templates, without .md)")
	fl.StringVar(&target, "target", "", "target branch (default: the repo's default branch)")
	fl.StringVar(&closes, "closes", "", "issue the MR closes, group/project#N: adds a Closes line to the body")
	fl.StringSliceVar(&strip, "strip-actions", nil, "remove quick actions from the MR body: names (assign_reviewer,label) or all when given without a value")
	fl.Lookup("strip-actions").NoOptDefVal = "*"
	fl.BoolVar(&ready, "ready", false, "open the MR ready for review instead of as a draft")
	fl.StringVar(&forgeFlag, "forge", "", "forge (from the user config); default: the project manifest's")
	fl.Bool("dry-run", false, "show the MR (title, body, quick actions); change nothing")
	return cmd
}

type mrFromBranchOpts struct {
	title, template, target, closes, forge string
	strip                                  []string
	ready, dry                             bool
}

func openMRFromBranch(ctx context.Context, a *app.App, dir string, o mrFromBranchOpts) (mrFromBranch, error) {
	loc, err := workspace.Locate(a.Root, dir)
	if err != nil {
		return mrFromBranch{}, err
	}
	m := loc.Manifest
	repoPath := m.Repos[loc.Repo].Path
	gc := git.Client{}
	branch, err := gc.Run(ctx, loc.Tree, "branch", "--show-current")
	if err != nil {
		return mrFromBranch{}, fmt.Errorf("%s: %s", loc.Tree, git.Reason(err))
	}
	if branch = strings.TrimSpace(branch); branch == "" {
		return mrFromBranch{}, fmt.Errorf("%s has a detached HEAD: check out the branch to open the MR for", loc.Tree)
	}
	name := m.Forge
	if o.forge != "" {
		name = o.forge
	}
	fc, ok := a.Config.Forges[name]
	if !ok {
		return mrFromBranch{}, fmt.Errorf("forge %q is not defined in the user config", name)
	}
	be, err := review.Dial(ctx, name, fc)
	if err != nil {
		return mrFromBranch{}, err
	}
	fg, ok := forge.Forge(be).(startwork.Forge)
	if !ok {
		return mrFromBranch{}, fmt.Errorf("forge %s cannot create merge requests", name)
	}
	res := mrFromBranch{Project: loc.Project, Repo: loc.Repo, Branch: branch, DryRun: o.dry}

	def, err := fg.DefaultBranch(ctx, repoPath)
	if err != nil {
		return res, fmt.Errorf("default branch of %s: %w", repoPath, err)
	}
	res.Target = cmpOr(o.target, def)
	if branch == res.Target {
		return res, fmt.Errorf("%s is the target branch itself: there is nothing to open an MR for", branch)
	}
	if existing, err := openMRsOf(ctx, fg, repoPath, branch); err != nil {
		return res, err
	} else if existing != nil {
		res.MR, res.URL, res.Title, res.Draft = &existing.Ref, existing.WebURL, existing.Title, existing.Draft
		res.Notes = append(res.Notes, "the branch has an open merge request already")
		return res, nil
	}
	branches, err := fg.Branches(ctx, repoPath, branch)
	if err != nil {
		return res, fmt.Errorf("branches of %s: %w", repoPath, err)
	}
	if !slices.Contains(branches, branch) {
		return res, fmt.Errorf("%s is not on the forge yet: `git push -u origin %s` first", branch, branch)
	}

	res.Title = o.title
	if res.Title == "" {
		res.Title = mrTitleFor(ctx, gc, loc.Tree, res.Target, branch)
	}
	res.Draft = m.Conventions.MR.DraftMR() && !o.ready
	body, err := fromBranchBody(ctx, fg, m, repoPath, def, &res, o)
	if err != nil {
		return res, err
	}
	d := forge.MRDraft{
		Project: repoPath, Source: branch, Target: res.Target, Title: res.Title,
		Description: body, Draft: res.Draft, CommitPrefix: commitprefix.Read(ctx, loc.Tree),
	}
	if o.dry {
		res.Body = body
		_ = a.Audit.Append(audit.Entry{
			Actor: "user", Action: "forge.mr.create", Target: repoPath + ":" + branch, Project: loc.Project,
			Params: map[string]any{"source": branch, "target": res.Target, "draft": res.Draft, "template": res.Template},
			DryRun: true, Outcome: "skipped",
		})
		return res, nil
	}
	mr, err := fg.CreateMR(ctx, d)
	if errors.Is(err, forge.ErrExists) {
		if existing, lerr := openMRsOf(ctx, fg, repoPath, branch); lerr == nil && existing != nil {
			mr, err = *existing, nil
			res.Notes = append(res.Notes, "the branch has an open merge request already")
		}
	} else if err == nil {
		res.Created = true
	}
	e := audit.Entry{
		Actor: "user", Action: "forge.mr.create", Target: mr.Ref.String(), Project: loc.Project, URL: mr.WebURL,
		Params: map[string]any{
			"source": branch, "target": res.Target, "draft": res.Draft, "template": res.Template,
			"stripped": res.Stripped, "quick_actions": len(res.Actions), "outcome": map[bool]string{true: "created", false: "exists"}[res.Created],
		},
		Outcome: "ok",
	}
	if err != nil {
		e.Target = repoPath + ":" + branch
		e.Outcome, e.Error = "error", err.Error()
	}
	_ = a.Audit.Append(e)
	if err != nil {
		return res, fmt.Errorf("open MR for %s: %w", branch, err)
	}
	res.MR, res.URL = &mr.Ref, mr.WebURL
	return res, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// openMRsOf is the open MR with branch as its source, when there is one.
func openMRsOf(ctx context.Context, fg startwork.Forge, project, branch string) (*forge.MR, error) {
	mrs, err := fg.ListMRs(ctx, forge.MRQuery{Project: project, State: "opened", SourceBranch: branch})
	if err != nil {
		return nil, fmt.Errorf("merge requests of %s: %w", project, err)
	}
	if len(mrs) == 0 {
		return nil, nil
	}
	return &mrs[0], nil
}

// fromBranchBody renders the description: the Closes line, then the template
// without the stripped quick actions. It records the template and actions on res.
func fromBranchBody(ctx context.Context, fg startwork.Forge, m *config.Manifest, repoPath, ref string, res *mrFromBranch, o mrFromBranchOpts) (string, error) {
	pick, err := startwork.PickTemplate(ctx, fg, m, repoPath, ref, o.template)
	if err != nil {
		return "", err
	}
	res.Template = pick.Name
	res.Notes = append(res.Notes, pick.Warnings...)
	res.Actions = startwork.ParseQuickActions(pick.Body)
	for _, n := range o.strip {
		if n == "all" {
			n = "*"
		}
		res.Stripped = append(res.Stripped, n)
	}
	var head string
	if o.closes != "" {
		c, err := forge.ParseRef(o.closes)
		if err != nil {
			return "", fmt.Errorf("--closes: %w", err)
		}
		if c.Kind != forge.KindIssue {
			return "", fmt.Errorf("--closes %s: want an issue like group/project#3", o.closes)
		}
		if c.Project == repoPath {
			head = fmt.Sprintf("Closes #%d", c.IID)
		} else {
			head = "Closes " + c.String()
		}
	}
	body := head
	if tb := strings.TrimSpace(startwork.StripQuickActions(pick.Body, res.Stripped)); tb != "" {
		if body != "" {
			body += "\n\n"
		}
		body += tb
	}
	if body != "" {
		body += "\n"
	}
	return body, nil
}

// mrTitleFor is the subject of the branch's one commit, else the branch name
// in words (GitLab's own rule).
func mrTitleFor(ctx context.Context, gc git.Client, tree, target, branch string) string {
	for _, base := range []string{"origin/" + target, target} {
		out, err := gc.Run(ctx, tree, "log", "--format=%s", base+"..HEAD")
		if err != nil {
			continue
		}
		if subjects := strings.Split(strings.TrimSpace(out), "\n"); len(subjects) == 1 && subjects[0] != "" {
			return subjects[0]
		}
		break
	}
	words := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '/' {
			return ' '
		}
		return r
	}, branch)
	words = strings.TrimSpace(words)
	rs := []rune(words)
	if len(rs) > 0 {
		rs[0] = unicode.ToUpper(rs[0])
	}
	return string(rs)
}

func printMRFromBranch(w interface{ Write([]byte) (int, error) }, r mrFromBranch) {
	kind := "MR"
	if r.Draft {
		kind = "draft MR"
	}
	switch {
	case r.DryRun:
		fmt.Fprintf(w, "dry-run: would open %s %s → %s: %s\n", kind, r.Branch, r.Target, r.Title)
		for line := range strings.SplitSeq(strings.TrimRight(r.Body, "\n"), "\n") {
			fmt.Fprintln(w, "  │ "+line)
		}
		for _, qa := range r.Actions {
			state := "kept"
			if slices.Contains(r.Stripped, "*") || slices.Contains(r.Stripped, qa.Name) {
				state = "stripped"
			}
			fmt.Fprintf(w, "  quick action %-8s %s (%s)\n", state, qa.Summary, qa.Line)
		}
	case r.Created:
		fmt.Fprintf(w, "opened %s %s %s → %s: %s\n  %s\n", kind, r.MR, r.Branch, r.Target, r.Title, r.URL)
	default:
		fmt.Fprintf(w, "%s %s is open for %s: %s\n  %s\n", kind, r.MR, r.Branch, r.Title, r.URL)
	}
	for _, n := range r.Notes {
		fmt.Fprintln(w, "  note:", n)
	}
}
