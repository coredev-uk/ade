package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/ai"
)

// ciTriageOut is the JSON shape of `ade ai ci-triage`.
type ciTriageOut struct {
	aiOut
	// FailedJobs are the names of the failed jobs the model was shown.
	FailedJobs []string `json:"failed_jobs"`
}

func newAICITriageCmd() *cobra.Command {
	var o aiOpts
	cmd := &cobra.Command{
		Use:   "ci-triage <mr-ref>",
		Short: "Triage the failed CI jobs of an MR: a short cause and a suggested fix per job",
		Long: "Reads the failed jobs of the MR's pipeline and the end of each job's log (the same log the\n" +
			"MR page shows), and asks the model for the cause and a fix per job. Nothing is written\n" +
			"to the forge. The answer is cached by what the logs say, so a retry that fails the same\n" +
			"way is free (--refresh buys a new one). With no pipeline or no failed job nothing is\n" +
			"asked. With ai.mode = \"off\" the command refuses and nothing else changes.\n" +
			"(`ade ai triage` is something else: it triages an agent review's drafts.)",
		Example: "  ade ai ci-triage acme/api!12\n  ade ai ci-triage acme/api!12 --dry-run --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseMRRef(args[0])
			if err != nil {
				return err
			}
			e, err := openAI(cmd, ref, o.forge)
			if err != nil {
				return err
			}
			defer e.stop()
			mr, err := e.be.MR(cmd.Context(), ref)
			if err != nil {
				return fmt.Errorf("read %s: %w", ref, err)
			}
			in, err := ai.GatherCITriage(cmd.Context(), e.be, mr, nil)
			if errors.Is(err, ai.ErrNoPipeline) || errors.Is(err, ai.ErrNoFailedJobs) {
				if jsonOut {
					return printJSON(cmd.OutOrStdout(), ciTriageOut{aiOut: aiOut{Feature: "ci-triage", Ref: ref.String()}, FailedJobs: []string{}})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s; nothing to triage (no AI call made)\n", ref, err)
				return nil
			}
			if err != nil {
				return err
			}
			_, out, err := e.runJob(cmd, ai.CITriageJob(e.forgeName, in), o)
			if err != nil {
				return err
			}
			if jsonOut {
				names := make([]string, 0, len(in.Failures))
				for _, f := range in.Failures {
					names = append(names, f.Job.Name)
				}
				return printJSON(cmd.OutOrStdout(), ciTriageOut{aiOut: out, FailedJobs: names})
			}
			return emit(cmd, out)
		},
	}
	o.bind(cmd)
	return cmd
}
