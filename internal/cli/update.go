package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/project"
)

func init() { register(newUpdateCmd) }

func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Refresh manifests, fetch every repo, and fast-forward clean trees",
		Long: `Refetches each project manifest, clones new repos, runs 'git fetch --prune'
everywhere, then fast-forwards clean baseline and feature worktrees that are
strictly behind their upstream. Dirty, ahead, or diverged trees are never touched.

Exits 2 when any repo failed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			stop, err := notify.Attach(cmd.Context(), a, notify.Channels{})
			if err != nil {
				return err
			}
			defer stop() // flushes pending events into the inbox

			svc := project.New(a, "ade:update")
			if !jsonOut && isTerminal(os.Stderr) {
				svc.OnProgress = func(p project.Progress) {
					fmt.Fprintf(os.Stderr, "\rupdating %d/%d", p.Done, p.Total)
					if p.Done == p.Total {
						fmt.Fprint(os.Stderr, "\r\033[K")
					}
				}
			}
			rep, err := svc.Update(cmd.Context())
			if err != nil {
				return err
			}
			return finishReport(cmd.OutOrStdout(), rep)
		},
	}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// finishReport prints rep and maps a failed run to exit code 2.
func finishReport(w io.Writer, rep project.Report) error {
	if jsonOut {
		if rep.Results == nil {
			rep.Results = []project.RepoResult{}
		}
		if err := printJSON(w, struct {
			project.Report
			Title string `json:"title"`
		}{rep, rep.Title()}); err != nil {
			return err
		}
	} else {
		printRepoResults(w, rep)
	}
	if rep.Failed() {
		return withCode(2, errors.New(rep.Title()))
	}
	return nil
}

func printRepoResults(w io.Writer, rep project.Report) {
	for _, res := range rep.Results {
		name := res.Project + "/" + res.Repo
		if res.Cloned {
			fmt.Fprintf(w, "%s  cloned\n", name)
		}
		for _, t := range res.Pulled {
			fmt.Fprintf(w, "%s  fast-forwarded %s\n", name, t)
		}
		for _, s := range res.Skipped {
			fmt.Fprintf(w, "%s  skipped %s: %s\n", name, s.Tree, s.Reason)
		}
		for _, f := range res.Failures {
			fmt.Fprintf(w, "%s  FAILED %s: %s\n", name, f.Op, f.Reason)
		}
	}
	for _, msg := range rep.Invalid {
		fmt.Fprintf(w, "manifest rejected (last good copy in use): %s\n", msg)
	}
	fmt.Fprintln(w, rep.Title())
}
