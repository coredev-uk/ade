package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
)

func init() { register(newSyncCmd) }

// exitSyncFailed is the exit code when a project could not be synced.
const exitSyncFailed = 2

func newSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Pull assigned work, sprints and review requests from the forges",
		Long: "Syncs every registered project: your assigned items, the current and adjacent\n" +
			"sprints, merge requests and review requests. Results are cached for the\n" +
			"dashboard (see `ade items`), item records are refreshed, and changes raise\n" +
			"notifications. Exits 2 when any project failed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			stop, err := notify.Attach(cmd.Context(), a, notify.Channels{})
			if err != nil {
				return err
			}
			results, err := forgesync.New(a).Sync(cmd.Context())
			stop()
			if err != nil {
				return err
			}
			if jsonOut {
				err = printJSON(cmd.OutOrStdout(), results)
			} else {
				printSync(cmd.OutOrStdout(), results)
			}
			if err != nil {
				return err
			}
			if n := countFailed(results); n > 0 {
				return withCode(exitSyncFailed, fmt.Errorf("%d of %d projects failed to sync", n, len(results)))
			}
			return nil
		},
	}
}

func countFailed(results []forgesync.Result) int {
	n := 0
	for _, r := range results {
		if r.Err != "" {
			n++
		}
	}
	return n
}

func printSync(w io.Writer, results []forgesync.Result) {
	if len(results) == 0 {
		fmt.Fprintln(w, "no projects registered (ade project add <manifest>)")
		return
	}
	for _, r := range results {
		if r.Err != "" {
			fmt.Fprintf(w, "%s: error: %s\n", r.Project, r.Err)
			continue
		}
		fmt.Fprintf(w, "%s · %s · %s\n", r.Project, plural(len(r.Items), "item"), plural(len(r.Reviews), "review request"))
		fmt.Fprintf(w, "  %s\n", windowLine(r.Window))
		for _, warn := range r.Warnings {
			fmt.Fprintf(w, "  warning: %s\n", warn)
		}
	}
}

// windowLine renders "Sprint 42 · ends Fri 13 Nov · 3 workdays left".
func windowLine(w domain.Window) string {
	if w.Current == nil {
		if w.Next != nil {
			return "no current sprint · next: " + w.Next.Title
		}
		return "no current sprint"
	}
	parts := []string{w.Current.Title, "ends " + w.Current.Due.Format("Mon 2 Jan"), plural(w.DaysLeft, "workday") + " left"}
	if w.Next != nil {
		parts = append(parts, "next: "+w.Next.Title)
	}
	return strings.Join(parts, " · ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

var errNoCache = errors.New("nothing cached yet: run `ade sync` first")
