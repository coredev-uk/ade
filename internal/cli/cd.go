package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/workspace"
)

func init() { register(newCdCmd) }

func newCdCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "cd <item-ref|repo> [--repo <name>]",
		Short: "Print a work item's worktree path (cd \"$(ade cd 412)\")",
		Long: `Prints one absolute path and a newline, so the shell can use it:

  cd "$(ade cd 412)"
  adecd() { d=$(ade cd "$@") && cd "$d"; }

Everything else goes to stderr; on failure nothing is printed to stdout and the
exit code is non-zero. Use the function: "cd ''" succeeds, so a bare
cd "$(ade cd 412)" stays where it is on failure without an error.

<item-ref> is what "ade item" and "ade vscode workspace" take: a tracker issue,
child issue or MR ref (group/project#N, group/project!N), or a bare N with one
registered project (read from the manifest snapshot of the last "ade update").
It is resolved from the cache and the file system only (no network). The path
is the item's worktree in its primary repo (the repos the item routes to, in
order, then the others by name), or in the repo --repo names.
An item with no worktree is an error.

A name that is not a ref is a repo: "api" (when one project has it) or
"<project>/api". It prints the repo's baseline worktree, the default branch's
tree. A ref wins when a string could be both: anything with # or ! and a bare
number is a work item; a repo named by digits is reached as "<project>/<repo>".`,
		Example: "  ade cd 412\n  ade cd acme/tracker#412 --repo web\n  ade cd api\n  ade cd acme/api",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sel, err := resolveCd(cmd, args[0], repo)
			if err != nil {
				return err
			}
			return printCd(cmd.OutOrStdout(), sel)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repo whose worktree to print when the item has several (default: the primary one)")
	return cmd
}

// cdSelection is the chosen worktree and the others the item has.
type cdSelection struct {
	Project   string
	Chosen    workspace.ItemWorktree
	Worktrees []workspace.ItemWorktree
}

// cdJSON is the JSON shape of `ade cd`.
type cdJSON struct {
	Path      string                   `json:"path"`
	Project   string                   `json:"project"`
	Repo      string                   `json:"repo"`
	Branch    string                   `json:"branch"`
	Worktrees []workspace.ItemWorktree `json:"worktrees"`
}

func printCd(w io.Writer, sel cdSelection) error {
	if jsonOut {
		return printJSON(w, cdJSON{
			Path: sel.Chosen.Path, Project: sel.Project, Repo: sel.Chosen.Repo, Branch: sel.Chosen.Branch, Worktrees: sel.Worktrees,
		})
	}
	_, err := fmt.Fprintln(w, sel.Chosen.Path)
	return err
}

func resolveCd(cmd *cobra.Command, arg, repoFlag string) (cdSelection, error) {
	a := appFrom(cmd)
	if !strings.ContainsAny(arg, "#!") && !isNumber(arg) {
		if repoFlag != "" {
			return cdSelection{}, errors.New("--repo applies to a work item ref, not to a repo name")
		}
		m, repo, err := workspace.FindRepo(a.Root, arg)
		if err != nil {
			return cdSelection{}, err
		}
		base, err := workspace.RepoBaseline(cmd.Context(), a.Root, m, repo)
		if err != nil {
			return cdSelection{}, err
		}
		return cdSelection{Project: m.Name, Chosen: base, Worktrees: []workspace.ItemWorktree{base}}, nil
	}

	// ade cd never touches the network: a bare number reads the snapshot only.
	ref, err := itemRefFrom(cmd, arg, project.SnapshotManifest)
	if err != nil {
		return cdSelection{}, err
	}
	st, err := a.Cache()
	if err != nil {
		return cdSelection{}, err
	}
	items, err := forgesync.ItemRefs(st)
	if err != nil {
		return cdSelection{}, err
	}
	it, ok := workspace.FindItem(items, ref)
	if !ok {
		return cdSelection{}, fmt.Errorf("no cached work item for %s (run `ade sync`)", ref)
	}
	m, err := project.LoadManifest(filepath.Join(a.Root, it.Project))
	if err != nil {
		return cdSelection{}, fmt.Errorf("project %s: %w (run `ade project add`)", it.Project, err)
	}
	wts := workspace.ItemWorktrees(cmd.Context(), a.Root, m, it.Item)
	if len(wts) == 0 {
		return cdSelection{}, fmt.Errorf("%s has no worktree: make one with `ade start %s` (or `w` on Home)", it.Item.Ref, arg)
	}
	sel := cdSelection{Project: it.Project, Chosen: wts[0], Worktrees: wts}
	if repoFlag != "" {
		i := slices.IndexFunc(wts, func(w workspace.ItemWorktree) bool { return w.Repo == repoFlag })
		if i < 0 {
			var have []string
			for _, w := range wts {
				have = append(have, w.Repo)
			}
			return cdSelection{}, fmt.Errorf("%s has no worktree in repo %q (it has: %s)", it.Item.Ref, repoFlag, strings.Join(have, ", "))
		}
		sel.Chosen = wts[i]
	}
	return sel, nil
}
