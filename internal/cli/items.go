package cli

import (
	"fmt"
	"io"
	"slices"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forgesync"
)

func init() { register(newItemsCmd) }

func newItemsCmd() *cobra.Command {
	var only string
	cmd := &cobra.Command{
		Use:   "items",
		Short: "List cached work items (no network; run `ade sync` to refresh)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := appFrom(cmd).Cache()
			if err != nil {
				return err
			}
			results, err := forgesync.Load(st)
			if err != nil {
				return err
			}
			items, err := selectItems(results, only)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), items)
			}
			printItems(cmd.OutOrStdout(), items)
			return nil
		},
	}
	cmd.Flags().StringVar(&only, "project", "", "only this ADE project")
	return cmd
}

// selectItems flattens the cached items of one project (or all).
func selectItems(results []forgesync.Result, project string) ([]domain.WorkItem, error) {
	if len(results) == 0 {
		return nil, errNoCache
	}
	if project != "" && !slices.ContainsFunc(results, func(r forgesync.Result) bool { return r.Project == project }) {
		names := make([]string, len(results))
		for i, r := range results {
			names[i] = r.Project
		}
		return nil, fmt.Errorf("no cached project %q (have: %v)", project, names)
	}
	items := []domain.WorkItem{}
	for _, r := range results {
		if project == "" || r.Project == project {
			items = append(items, r.Items...)
		}
	}
	return items, nil
}

// printItems writes one line per item: ref, stage, court · reason, weight, title.
func printItems(w io.Writer, items []domain.WorkItem) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, it := range items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", it.Ref, it.Stage, courtReason(it), weightLabel(it), it.Issue.Title)
	}
	tw.Flush()
}

func courtReason(it domain.WorkItem) string {
	if it.Reason == "" {
		return string(it.Court)
	}
	return string(it.Court) + " · " + it.Reason
}

func weightLabel(it domain.WorkItem) string {
	if it.Weight == nil {
		return "–"
	}
	return fmt.Sprintf("%dw", *it.Weight)
}
