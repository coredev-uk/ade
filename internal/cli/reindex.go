package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/ledger"
	"github.com/coredev-uk/ade/internal/records"
)

func init() { register(newReindexCmd) }

func newReindexCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reindex",
		Short: "Rebuild cache.db's indexes from the audit log and item records",
		Long: "cache.db is disposable; the audit log and the item records are not. This rebuilds what\n" +
			"can be rebuilt without the network: the AI result cache (from the artifacts in the item\n" +
			"records, so paid answers are not bought twice after a cache reset) and the spend ledger\n" +
			"(from the audit log). Forge data returns with the next `ade sync`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			st, err := a.Cache()
			if err != nil {
				return err
			}
			answers, err := ai.NewCache(st, records.New(a.RecordsDir)).Reindex()
			if err != nil {
				return fmt.Errorf("reindex AI results: %w", err)
			}
			entries, err := ledger.New(st, a.Audit).Rebuild()
			if err != nil {
				return fmt.Errorf("reindex ledger: %w", err)
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), map[string]int{"ai_results": answers, "ledger_entries": entries})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "reindexed: %s, %d ledger entries\n", plural(answers, "cached AI answer"), entries)
			return nil
		},
	}
}
