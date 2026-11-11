package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() { register(newVersionCmd) }

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the ADE version",
		Args:  cobra.NoArgs,
		// Printing the version must work with a broken or missing config.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), struct {
					Version string `json:"version"`
				}{Version})
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "ade %s\n", Version)
			return err
		},
	}
}
