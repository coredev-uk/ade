package cli

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/project"
)

func init() { register(newProjectCmd) }

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Add, list, inspect, and remove projects",
	}
	cmd.AddCommand(newProjectAddCmd(), newProjectListCmd(), newProjectStatusCmd(), newProjectBootstrapCmd(), newProjectRemoveCmd())
	return cmd
}

func newProjectAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <source>",
		Short: "Register a project manifest and clone its repos",
		Long: `Registers the manifest in your config (a local path, an https URL, or
git+<url>#<path-in-repo>), then clones every repo as a bare clone with a baseline
worktree and runs post_clone hooks. Re-running it fills in whatever is missing.

Exits 2 when any repo failed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			source, err := project.NormalizeSource(args[0])
			if err != nil {
				return err
			}
			stop, err := notify.Attach(cmd.Context(), a, notify.Channels{})
			if err != nil {
				return err
			}
			defer stop()

			rep, err := project.New(a, "user").Add(cmd.Context(), source)
			if err != nil {
				return err
			}
			return finishReport(cmd.OutOrStdout(), rep)
		},
	}
}

func newProjectListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			infos, err := project.New(appFrom(cmd), "user").List()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if jsonOut {
				if infos == nil {
					infos = []project.Info{}
				}
				return printJSON(w, infos)
			}
			if len(infos) == 0 {
				fmt.Fprintln(w, "no projects registered; try `ade project add <source>`")
				return nil
			}
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			for _, i := range infos {
				name, updated := i.Name, "never updated"
				if name == "" {
					name = "(not cloned)"
				}
				if !i.LastUpdate.IsZero() {
					updated = "updated " + i.LastUpdate.Local().Format(time.DateTime)
				}
				fmt.Fprintf(tw, "%s\t%d repos\t%s\t%s\n", name, i.Repos, updated, i.Source)
			}
			return tw.Flush()
		},
	}
}

func newProjectStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [name]",
		Short: "Show repos, worktrees, and their sync state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			st, err := project.New(appFrom(cmd), "user").Status(cmd.Context(), name)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if jsonOut {
				if st == nil {
					st = []project.ProjectStatus{}
				}
				return printJSON(w, st)
			}
			if len(st) == 0 {
				fmt.Fprintln(w, "no projects cloned yet")
				return nil
			}
			for _, p := range st {
				printProjectStatus(w, p)
			}
			return nil
		},
	}
}

func printProjectStatus(w io.Writer, p project.ProjectStatus) {
	fmt.Fprintf(w, "%s  %s\n", p.Name, p.Dir)
	for _, r := range p.Repos {
		switch {
		case !r.Present:
			fmt.Fprintf(w, "  %s  missing (run `ade update`)\n", r.Name)
			continue
		case !r.InManifest:
			fmt.Fprintf(w, "  %s  not in manifest anymore (kept on disk)\n", r.Name)
		default:
			fmt.Fprintf(w, "  %s\n", r.Name)
		}
		if r.Error != "" {
			fmt.Fprintf(w, "    error: %s\n", r.Error)
		}
		if r.Baseline != nil {
			fmt.Fprintf(w, "    %s\n", treeLine(*r.Baseline))
		}
		for _, t := range r.Worktrees {
			fmt.Fprintf(w, "    %s\n", treeLine(t))
		}
	}
}

func treeLine(t project.TreeStatus) string {
	state := "clean"
	if t.Dirty {
		state = "dirty"
	}
	sync := "no upstream"
	if t.HasUpstream {
		sync = fmt.Sprintf("↑%d ↓%d", t.Ahead, t.Behind)
	}
	return fmt.Sprintf("%s  %s  %s  %s", t.Name, t.Branch, state, sync)
}
