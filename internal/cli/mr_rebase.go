package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/review"
)

func init() { mrExtraCmds = append(mrExtraCmds, newMRRebaseCmd) }

// rebaseResult is `ade mr rebase --json`. Local lists the git commands to run
// by hand when the forge could not do it (see review.LocalRebaseSteps).
type rebaseResult struct {
	Ref      forge.Ref `json:"ref"`
	Action   string    `json:"action"`
	DryRun   bool      `json:"dry_run,omitempty"`
	Method   string    `json:"method,omitempty"`
	UpToDate bool      `json:"up_to_date,omitempty"`
	SHA      string    `json:"sha,omitempty"`
	Local    []string  `json:"local,omitempty"`
}

func newMRRebaseCmd() *cobra.Command {
	var (
		yes    bool
		skipCI bool
	)
	cmd := &cobra.Command{
		Use:   "rebase <ref> --yes",
		Short: "Bring the MR's branch up to date with its target on the forge (refuses without --yes)",
		Long: "Asks the forge to update the MR's source branch with its target branch: GitLab rebases it\n" +
			"(PUT .../rebase; the source branch is rewritten, so a checkout of it must reset), GitHub merges\n" +
			"the target into it (update-branch, pinned to the head you saw). ADE waits until the forge is\n" +
			"done. When the forge cannot do it, in practice a conflict, the branch is untouched and the exact\n" +
			"git commands to rebase in the worktree are printed (ADE never runs them). --dry-run sends nothing.",
		Example: "  ade mr rebase acme/api!12 --dry-run\n  ade mr rebase acme/api!12 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dry, _ := cmd.Flags().GetBool("dry-run"); !yes && !dry {
				return errors.New("refusing to update the branch without --yes (GitLab rewrites it; check with --dry-run first)")
			}
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			ctx, a := cmd.Context(), appFrom(cmd)
			mr, err := c.be.MR(ctx, c.ref)
			if err != nil {
				return err
			}
			if mr.State != "opened" {
				return fmt.Errorf("%s is %s, not open", c.ref, mr.State)
			}
			res, err := c.w.Rebase(ctx, c.ref, forge.RebaseOpts{SHA: mr.HeadSHA, SkipCI: skipCI})
			if errors.Is(err, forge.ErrRebaseFailed) {
				steps := review.LocalRebaseSteps(mr, localTree(ctx, a, mr))
				if jsonOut {
					_ = printJSON(cmd.OutOrStdout(), rebaseResult{Ref: c.ref, Action: "rebase", Local: steps})
				}
				return fmt.Errorf("%w\n\nrebase it locally, then push:\n%s", err, indent(strings.Join(steps, "\n")))
			}
			if err != nil {
				return err
			}
			r := rebaseResult{Ref: c.ref, Action: "rebase", DryRun: c.dry, Method: res.Method, UpToDate: res.UpToDate, SHA: res.HeadSHA}
			var text string
			switch {
			case c.dry:
				text = fmt.Sprintf("dry-run: would update %s (%s) with %s", c.ref, mr.SourceBranch, mr.TargetBranch)
				if mr.HasConflicts {
					r.Local = review.LocalRebaseSteps(mr, localTree(ctx, a, mr))
					text += fmt.Sprintf("\n%s reports conflicts with %s, so the forge will probably refuse; by hand:\n%s",
						forgeName(c.be), mr.TargetBranch, indent(strings.Join(r.Local, "\n")))
				}
			case res.UpToDate:
				text = fmt.Sprintf("%s is already up to date with %s", c.ref, mr.TargetBranch)
			case res.Method == "merge":
				text = fmt.Sprintf("updated %s: merged %s into %s%s", c.ref, mr.TargetBranch, mr.SourceBranch, headNote(res.HeadSHA))
			default:
				text = fmt.Sprintf("rebased %s onto %s%s", c.ref, mr.TargetBranch, headNote(res.HeadSHA))
			}
			return out(cmd, r, "%s", text)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm: the forge rewrites (GitLab) or adds a merge commit to (GitHub) the source branch")
	cmd.Flags().BoolVar(&skipCI, "skip-ci", false, "GitLab: do not start a pipeline for the rebased head")
	return cmd
}

func headNote(sha string) string {
	if sha == "" {
		return ""
	}
	return fmt.Sprintf(" (head %.8s)", sha)
}

func forgeName(be review.Backend) string {
	if k := be.Kind(); k != "" {
		return strings.ToUpper(k[:1]) + k[1:]
	}
	return "the forge"
}

// localTree finds the worktree of the MR, from disk alone: the one on its
// source branch, or for a fork MR the detached mr-<iid> tree (its branch is not
// in origin). "" when there is none, or the repo is not known.
func localTree(ctx context.Context, a *app.App, mr forge.MR) string {
	for _, pr := range a.Config.Projects {
		m, err := project.SnapshotManifest(a, pr) // disk only: a hint must not wait on the network
		if err != nil {
			continue
		}
		for _, name := range slices.Sorted(mapKeys(m.Repos)) {
			if !forge.SameProject(m.Repos[name].Path, mr.Ref.Project) {
				continue
			}
			trees, err := git.Client{}.Worktrees(ctx, filepath.Join(a.Root, m.Name, name, ".bare"))
			if err != nil {
				continue
			}
			detached := project.MRTreeDir(a.Root, m.Name, name, mr.Ref.IID)
			for _, w := range trees {
				if mr.Fork && w.Path == detached || !mr.Fork && w.Branch == mr.SourceBranch {
					return w.Path
				}
			}
		}
	}
	return ""
}
