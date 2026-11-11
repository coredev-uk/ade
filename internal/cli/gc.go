package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/project"
)

func init() { register(newGCCmd) }

// gcRow is one worktree in `ade gc --json`.
type gcRow struct {
	project.Tree
	Action string `json:"action"` // remove | keep
	// Removed and BranchKept report what a real run did.
	Removed    bool   `json:"removed,omitempty"`
	BranchKept bool   `json:"branch_kept,omitempty"`
	Error      string `json:"error,omitempty"`
}

func newGCCmd() *cobra.Command {
	var (
		dryRun, yes bool
		only        string
	)
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Remove worktrees whose merge request is merged or closed",
		Long: `Looks at every feature worktree of every cloned project and removes the ones whose
merge request is merged or closed, but only when the tree is clean and everything
committed is pushed. Each worktree that stays says why. The baseline tree and
worktrees whose branch has an open merge request or none are never touched.

--dry-run only reports. Without --yes it asks first (and refuses to run unattended).
Exits 2 when a removal failed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			ps := project.New(a, "user")
			trees, err := ps.Trees(cmd.Context(), only, forgeLookup(a))
			if err != nil {
				return err
			}
			rows := make([]gcRow, len(trees))
			var doomed []int
			for i, t := range trees {
				rows[i] = gcRow{Tree: t, Action: "keep"}
				if t.Removable() {
					rows[i].Action = "remove"
					doomed = append(doomed, i)
				}
			}
			w := cmd.OutOrStdout()
			if !jsonOut {
				printGC(w, rows)
			}
			if !dryRun && len(doomed) > 0 {
				ok, err := confirmGC(cmd, len(doomed), yes)
				if err != nil {
					return err
				}
				if ok {
					failed := removeAll(cmd.Context(), ps, rows, doomed)
					if !jsonOut {
						printRemoved(w, rows, doomed)
					}
					if jsonOut {
						if err := printJSON(w, rows); err != nil {
							return err
						}
					}
					if failed > 0 {
						return withCode(2, fmt.Errorf("%d of %d worktrees could not be removed", failed, len(doomed)))
					}
					return nil
				}
			}
			if jsonOut {
				return printJSON(w, rows)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be removed and why the rest stays; change nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "remove without asking")
	cmd.Flags().StringVar(&only, "project", "", "only this project")
	return cmd
}

// openGCForge builds forge clients for gc; tests swap it for the fake server.
var openGCForge = func(a *app.App) func(context.Context, string, config.Forge) (forge.Forge, error) {
	return forgesync.New(a).Open
}

// forgeLookup asks the forge each project's manifest names for the MRs of a
// branch. Clients are opened once per forge.
func forgeLookup(a *app.App) project.MRLookup {
	open := openGCForge(a)
	clients := map[string]forge.Forge{}
	return func(ctx context.Context, m *config.Manifest, repoPath, branch string) ([]forge.MR, error) {
		f, ok := clients[m.Forge]
		if !ok {
			fc, defined := a.Config.Forges[m.Forge]
			if !defined {
				return nil, fmt.Errorf("forge %q is not defined in the user config", m.Forge)
			}
			var err error
			if f, err = open(ctx, m.Forge, fc); err != nil {
				return nil, err
			}
			clients[m.Forge] = f
		}
		return f.ListMRs(ctx, forge.MRQuery{Project: repoPath, State: "all", SourceBranch: branch})
	}
}

func printGC(w io.Writer, rows []gcRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "no feature worktrees")
		return
	}
	removable := 0
	for _, r := range rows {
		if r.Removable() {
			removable++
			fmt.Fprintf(w, "remove  %s  (%s)\n", r.Label(), r.MR)
			continue
		}
		fmt.Fprintf(w, "keep    %s  %s\n", r.Label(), strings.Join(r.Keep, "; "))
	}
	fmt.Fprintf(w, "%d of %d worktrees can be removed\n", removable, len(rows))
}

// confirmGC asks before removing; an unattended run needs --yes.
func confirmGC(cmd *cobra.Command, n int, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	return askYes(cmd, fmt.Sprintf("remove %d worktrees?", n))
}

// askYes asks a yes/no question on the terminal. Without one (a pipe, a
// script, /dev/null, --json) it fails instead of guessing: destructive
// commands need --yes to run unattended.
func askYes(cmd *cobra.Command, question string) (bool, error) {
	in, ok := cmd.InOrStdin().(*os.File)
	if jsonOut || !ok || !isTerminal(in) || isDevNull(in) {
		return false, errors.New("not removing anything without a terminal to ask on: pass --yes to remove, or --dry-run to only look")
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}

// isDevNull: /dev/null is a character device, so isTerminal alone says yes.
func isDevNull(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err == nil && os.SameFile(fi, null)
}

// removeAll removes rows[idx...] and records the outcome on each row; it
// returns how many failed.
func removeAll(ctx context.Context, ps *project.Service, rows []gcRow, idx []int) (failed int) {
	for _, i := range idx {
		kept, err := ps.RemoveTree(ctx, rows[i].Tree)
		if err != nil {
			rows[i].Error = err.Error()
			failed++
			continue
		}
		rows[i].Removed, rows[i].BranchKept = true, kept
	}
	return failed
}

func printRemoved(w io.Writer, rows []gcRow, idx []int) {
	for _, i := range idx {
		r := rows[i]
		switch {
		case r.Error != "":
			fmt.Fprintf(w, "failed  %s  %s\n", r.Label(), r.Error)
		case r.BranchKept:
			fmt.Fprintf(w, "removed %s (branch %s kept: git does not consider it merged)\n", r.Label(), r.Branch)
		default:
			fmt.Fprintf(w, "removed %s\n", r.Label())
		}
	}
}
