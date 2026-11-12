package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/commitprefix"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/overview"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/workspace"
)

func init() { register(newAdoptCmd) }

// adoptOut is the JSON shape of `ade adopt`.
type adoptOut struct {
	Project  string   `json:"project"`
	Repo     string   `json:"repo"`
	Branch   string   `json:"branch"`
	Worktree string   `json:"worktree"`
	Existing bool     `json:"existing"` // the branch already had a worktree, kept where it is
	DryRun   bool     `json:"dry_run,omitempty"`
	Parent   int      `json:"parent,omitempty"`
	Prefix   string   `json:"prefix,omitempty"`
	Target   string   `json:"target"`
	Steps    []string `json:"steps"`
	Notes    []string `json:"notes,omitempty"`
}

type adoptFlags struct {
	item, target, from string
	dryRun             bool
}

func newAdoptCmd() *cobra.Command {
	var f adoptFlags
	cmd := &cobra.Command{
		Use:   "adopt <project> <repo> <branch>",
		Short: "Bring an existing branch or worktree into the ADE layout (no forge writes)",
		Long: `Puts <branch> of <repo> in the project's layout, <root>/<project>/<repo>/<branch>, and records the
item's git config in it (ade.parent, ade.commitPrefix, ade.target), the way ade start does for a
branch it creates. Nothing is written to the forge: no issue, branch, merge request or label.

By default the bare clone is fetched and the branch is checked out from origin/<branch>.
--from <path> takes the branch from another local clone or worktree instead, with its unpushed
commits: they are fetched into the bare clone's local branch, which the new worktree checks out.
A branch that already has a worktree of the bare clone stays where it is; only the git config is
written (--from cannot update a checked-out branch, so it is refused there).

--item <ref> sets ade.parent and the commit prefix from that cached work item; without it the
parent is unset and there is no prefix. --target sets ade.target (default: the repo's default
branch). The repo's post_worktree hooks run in a worktree adopt creates. A branch the dashboard
cannot link to the item yet (it has no merge request, and its name does not start with a child
issue's iid and a dash) is reported: it appears under the item once a merge request exists.
--dry-run prints each step and changes nothing.`,
		Example: "  ade adopt demo api feature/export-limit --from ~/src/api --item acme/tracker#15\n  ade adopt demo api fix-headers --dry-run",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := runAdopt(cmd, f, args[0], args[1], args[2])
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), out)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.item, "item", "", "cached work item (ref) the branch belongs to: sets ade.parent and the commit prefix")
	fl.StringVar(&f.target, "target", "", "MR target branch to record as ade.target (default: the repo's default branch)")
	fl.StringVar(&f.from, "from", "", "local clone or worktree holding the branch; its commits are fetched into the bare clone")
	fl.BoolVar(&f.dryRun, "dry-run", false, "print each step and change nothing")
	return cmd
}

// adopter carries one `ade adopt` run.
type adopter struct {
	cmd    *cobra.Command
	a      *app.App
	f      adoptFlags
	out    *adoptOut
	itemID string // audit item, "" without --item
}

// step reports one step: done, or what a dry run would do.
func (ad *adopter) step(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	ad.out.Steps = append(ad.out.Steps, s)
	if jsonOut {
		return
	}
	if ad.f.dryRun {
		s = "would " + s
	}
	fmt.Fprintln(ad.cmd.OutOrStdout(), s)
}

func (ad *adopter) note(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	ad.out.Notes = append(ad.out.Notes, s)
	if !jsonOut {
		fmt.Fprintln(ad.cmd.OutOrStdout(), "note: "+s)
	}
}

func (ad *adopter) audit(action, target string, params map[string]any, err error) {
	ent := audit.Entry{Action: action, Target: target, Item: ad.itemID, Project: ad.out.Project, Params: params, Outcome: "ok"}
	if err != nil {
		ent.Outcome, ent.Error = "error", err.Error()
	}
	_ = ad.a.Audit.Append(ent) // an unwritable audit log must not undo the adoption
}

func runAdopt(cmd *cobra.Command, f adoptFlags, projectName, repo, branch string) (*adoptOut, error) {
	ctx := cmd.Context()
	a := appFrom(cmd)
	ad := &adopter{cmd: cmd, a: a, f: f, out: &adoptOut{Project: projectName, Repo: repo, Branch: branch, DryRun: f.dryRun}}

	m, err := project.LoadManifest(filepath.Join(a.Root, projectName))
	if err != nil {
		return nil, fmt.Errorf("project %q is not cloned (see `ade project list`): %w", projectName, err)
	}
	spec, ok := m.Repos[repo]
	if !ok {
		return nil, fmt.Errorf("project %s has no repo %q (have: %s)", projectName, repo, strings.Join(m.RepoNames(), ", "))
	}
	ps := project.New(a, "user")
	bare := filepath.Join(ps.RepoDir(projectName, repo), ".bare")
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		return nil, fmt.Errorf("%s is not cloned: run `ade project add %s` first", repo, projectName)
	}
	gc, err := ps.GitClient(ctx, m.Forge, bare)
	if err != nil {
		return nil, err
	}
	if _, err := gc.Run(ctx, bare, "check-ref-format", "--branch", branch); err != nil {
		return nil, fmt.Errorf("%q is not a valid branch name", branch)
	}

	var wi *domain.WorkItem
	if f.item != "" {
		if wi, err = adoptItem(cmd, projectName, f.item); err != nil {
			return nil, err
		}
		ad.itemID = wi.Ref.String()
	}

	target := f.target
	if target == "" {
		if target = spec.DefaultBranch; target == "" {
			if target, err = gc.DefaultBranch(ctx, bare); err != nil {
				return nil, err
			}
		}
	}

	if !f.dryRun {
		if err := gc.PruneWorktrees(ctx, bare); err != nil {
			return nil, err
		}
	}
	trees, err := gc.Worktrees(ctx, bare)
	if err != nil {
		return nil, err
	}
	existing := slices.IndexFunc(trees, func(w git.Worktree) bool { return w.Branch == branch })
	if existing >= 0 {
		if f.from != "" {
			return nil, fmt.Errorf("%s is checked out at %s: git cannot update it from %s; adopt without --from", branch, trees[existing].Path, f.from)
		}
		dir := trees[existing].Path
		ad.out.Worktree, ad.out.Existing = dir, true
		ad.step("keep the worktree of %s where it is: %s", branch, dir)
	} else if err := ad.addWorktree(ctx, m, gc, bare, repo, branch); err != nil {
		return nil, err
	}

	prefix := ""
	if wi != nil {
		ad.out.Parent = wi.Ref.IID
		prefix = commitprefix.ExpandPrefix(m, *wi, repo)
	}
	ad.out.Prefix, ad.out.Target = prefix, target
	ad.step("write ade.parent=%d ade.commitPrefix=%q ade.target=%s in %s", ad.out.Parent, prefix, target, ad.out.Worktree)
	if !f.dryRun {
		err := commitprefix.WriteWorktreeConfig(ctx, ad.out.Worktree, ad.out.Parent, prefix, target)
		ad.audit("git.worktree.config", ad.out.Worktree, map[string]any{"parent": ad.out.Parent, "prefix": prefix, "target": target}, err)
		if err != nil {
			return nil, fmt.Errorf("write the worktree config: %w", err)
		}
	}

	if wi != nil && !workspace.OwnsBranch(*wi, spec.Path, branch) {
		ad.note("the dashboard will not show %s under %s until a merge request from it exists (or its name starts with a child issue iid and a dash)", branch, wi.Ref)
	}
	return ad.out, nil
}

// adoptItem finds the cached work item for ref; it must belong to the project.
func adoptItem(cmd *cobra.Command, projectName, arg string) (*domain.WorkItem, error) {
	ref, err := itemRef(cmd, arg)
	if err != nil {
		return nil, err
	}
	st, err := appFrom(cmd).Cache()
	if err != nil {
		return nil, err
	}
	results, err := forgesync.Load(st)
	if err != nil {
		return nil, err
	}
	res, wi, ok := overview.Find(results, ref)
	if !ok {
		return nil, fmt.Errorf("%s is not in the cache: run `ade sync`", ref)
	}
	if res.Project != projectName {
		return nil, fmt.Errorf("%s belongs to project %s, not %s", ref, res.Project, projectName)
	}
	return &wi, nil
}

// addWorktree brings the branch into the bare clone and checks it out under
// <root>/<project>/<repo>/<branch dir>.
func (ad *adopter) addWorktree(ctx context.Context, m *config.Manifest, gc git.Client, bare, repo, branch string) error {
	dir := filepath.Join(ad.a.Root, m.Name, repo, git.BranchDir(branch))
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s exists but is not a worktree of %s", dir, branch)
	}
	ad.out.Worktree = dir
	dry := ad.f.dryRun
	hasRef := func(ref string) bool {
		_, err := gc.Run(ctx, bare, "rev-parse", "--verify", "--quiet", ref)
		return err == nil
	}

	local := hasRef("refs/heads/" + branch)
	if ad.f.from != "" {
		from, err := filepath.Abs(ad.f.from)
		if err != nil {
			return err
		}
		if _, err := (git.Client{}).Run(ctx, from, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
			return fmt.Errorf("%s has no local branch %s", from, branch)
		}
		ad.step("fetch %s from %s into the bare clone", branch, from)
		if !dry {
			refspec := "refs/heads/" + branch + ":refs/heads/" + branch
			if _, err := (git.Client{}).Run(ctx, bare, "fetch", "--quiet", from, refspec); err != nil {
				return fmt.Errorf("fetch %s from %s: %w", branch, from, err)
			}
		}
		local = true
	} else {
		ad.step("fetch origin")
		if !dry {
			if err := gc.Fetch(ctx, bare); err != nil {
				ad.audit("git.fetch", bare, nil, err)
				return err
			}
			if !local && !hasRef("refs/remotes/origin/"+branch) {
				return fmt.Errorf("branch %s is not on origin and not in the bare clone: pass --from <path> to take it from a local clone", branch)
			}
		}
	}

	if local {
		ad.step("add worktree %s on the local branch %s", dir, branch)
	} else {
		ad.step("add worktree %s tracking origin/%s", dir, branch)
	}
	if dry {
		return nil
	}
	var err error
	if local {
		_, err = gc.Run(ctx, bare, "worktree", "add", dir, branch)
		if err == nil && hasRef("refs/remotes/origin/"+branch) {
			_, err = gc.Run(ctx, bare, "branch", "--set-upstream-to=origin/"+branch, branch)
		}
	} else {
		err = gc.AddTracking(ctx, bare, dir, branch)
	}
	params := map[string]any{"branch": branch, "repo": repo, "adopt": true}
	if ad.f.from != "" {
		params["from"] = ad.f.from
	}
	ad.audit("git.worktree.add", dir, params, err)
	if err != nil {
		return err
	}
	ad.runHooks(ctx, m, repo, dir)
	return nil
}

// runHooks runs post_worktree hooks as ade start does: a failing hook is a
// warning, the tree exists and is usable.
func (ad *adopter) runHooks(ctx context.Context, m *config.Manifest, repo, dir string) {
	for _, hook := range m.Repos[repo].Hooks.PostWorktree {
		ad.step("run post_worktree hook: %s", hook)
		out, err := git.RunHook(ctx, dir, hook)
		ad.audit("git.hook", dir, map[string]any{"hook": "post_worktree", "cmd": hook}, err)
		if err == nil {
			continue
		}
		ad.note("post_worktree hook failed in %s: %s", repo, hook)
		if ad.a.Bus != nil {
			ev := events.New("git.hook.failed", events.Warn, "post_worktree hook failed in "+repo+": "+hook)
			ev.Project, ev.Subject, ev.Detail = m.Name, repo+"/"+filepath.Base(dir), strings.TrimSpace(out)
			ad.a.Bus.Publish(ev)
		}
	}
}
