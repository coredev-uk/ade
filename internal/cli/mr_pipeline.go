package cli

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/forge"
)

func init() { mrExtraCmds = append(mrExtraCmds, newMRPipelineCmd) }

// pipelineLogBytes is how much of a job log is read: enough for --tail lines
// of an ordinary log without pulling a whole trace through the API.
const pipelineLogBytes = 256 << 10

// pipelineView is `ade mr pipeline --json`.
type pipelineView struct {
	Ref      forge.Ref       `json:"ref"`
	Pipeline *forge.Pipeline `json:"pipeline"`
	Jobs     []forge.Job     `json:"jobs"`
	Log      *jobLogView     `json:"log,omitempty"`
}

type jobLogView struct {
	Job  forge.Job `json:"job"`
	Text string    `json:"text"`
}

func newMRPipelineCmd() *cobra.Command {
	var (
		logJob string
		tail   int
	)
	cmd := &cobra.Command{
		Use:   "pipeline <ref>",
		Short: "The MR's head pipeline and its jobs; --log <job> prints a job's log tail",
		Long: "Shows the head pipeline of the merge request (status, id, link) and every job in it,\n" +
			"failed jobs first, then pipeline order: status, stage, name, duration and job id. --log takes a job's name\n" +
			"or id and prints the last --tail lines of its log (0: all of what was read).\n" +
			"Read-only: `ade mr retry <ref>` retries the failed jobs.",
		Example: "  ade mr pipeline acme/web!12\n  ade mr pipeline acme/web!12 --log lint --tail 100\n  ade mr pipeline acme/web!12 --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openMR(cmd, args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			mr, err := c.be.MR(ctx, c.ref)
			if err != nil {
				return err
			}
			v := pipelineView{Ref: c.ref, Pipeline: mr.Pipeline, Jobs: []forge.Job{}}
			if mr.Pipeline == nil {
				if logJob != "" {
					return fmt.Errorf("%s has no pipeline: no job log to show", c.ref)
				}
				return out(cmd, v, "%s has no pipeline", c.ref)
			}
			jobs, err := c.be.Jobs(ctx, c.ref.Project, mr.Pipeline.ID)
			if err != nil {
				return fmt.Errorf("jobs of pipeline #%d: %w", mr.Pipeline.ID, err)
			}
			// The forges list the newest job first; pipeline order reads
			// better, with the failed jobs up front.
			slices.SortStableFunc(jobs, func(a, b forge.Job) int {
				return cmp.Or(cmp.Compare(failedRank(a), failedRank(b)), cmp.Compare(a.ID, b.ID))
			})
			v.Jobs = orEmpty(jobs)
			if logJob != "" {
				j, err := findJob(jobs, logJob)
				if err != nil {
					return fmt.Errorf("pipeline #%d: %w", mr.Pipeline.ID, err)
				}
				txt, err := c.be.JobLog(ctx, c.ref.Project, j.ID, pipelineLogBytes)
				if err != nil {
					return fmt.Errorf("log of job %s (#%d): %w", j.Name, j.ID, err)
				}
				v.Log = &jobLogView{Job: j, Text: lastLines(txt, tail)}
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), v)
			}
			printPipeline(cmd.OutOrStdout(), v, mr.WebURL)
			return nil
		},
	}
	cmd.Flags().StringVar(&logJob, "log", "", "print the log tail of this job (name or id)")
	cmd.Flags().IntVar(&tail, "tail", 50, "lines of the job log to print with --log (0: all that was read)")
	return cmd
}

// failedRank sorts failed jobs ahead of the rest.
func failedRank(j forge.Job) int {
	if j.Status == "failed" {
		return 0
	}
	return 1
}

// findJob picks a job by id or by name. A name several jobs share (a retried
// job, a matrix) picks the latest, which is the one the pipeline status counts.
func findJob(jobs []forge.Job, key string) (forge.Job, error) {
	if id, err := strconv.ParseInt(strings.TrimPrefix(key, "#"), 10, 64); err == nil {
		for _, j := range jobs {
			if j.ID == id {
				return j, nil
			}
		}
	}
	var found *forge.Job
	for i := range jobs {
		if jobs[i].Name == key && (found == nil || jobs[i].ID > found.ID) {
			found = &jobs[i]
		}
	}
	if found != nil {
		return *found, nil
	}
	names := make([]string, len(jobs))
	for i, j := range jobs {
		names[i] = j.Name
	}
	slices.Sort(names)
	return forge.Job{}, fmt.Errorf("no job %q (jobs: %s)", key, strings.Join(names, ", "))
}

// lastLines keeps the last n lines of s (all of s when n <= 0).
func lastLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if n <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// jobWord is a job or pipeline status in words, as the MR page's CI tab says it.
func jobWord(status string) string {
	switch status {
	case "success":
		return "passed"
	case "pending", "created", "waiting_for_resource", "preparing", "scheduled":
		return "pending"
	case "canceling":
		return "canceled"
	}
	return status
}

func printPipeline(w io.Writer, v pipelineView, mrURL string) {
	p := v.Pipeline
	fmt.Fprintf(w, "%s · pipeline #%d %s\n", v.Ref, p.ID, jobWord(p.Status))
	if u := cmp.Or(p.WebURL, mrURL); u != "" {
		fmt.Fprintf(w, "  %s\n", u)
	}
	if len(v.Jobs) == 0 {
		fmt.Fprintln(w, "  the pipeline has no jobs yet")
	} else {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  STATUS\tSTAGE\tJOB\tDURATION\tID")
		for _, j := range v.Jobs {
			dur := "-"
			if j.Duration > 0 {
				dur = fmt.Sprintf("%.0fs", j.Duration)
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%d\n", jobWord(j.Status), j.Stage, j.Name, dur, j.ID)
		}
		tw.Flush()
	}
	if v.Log != nil {
		fmt.Fprintf(w, "\nlog of %s (#%d, %s):\n", v.Log.Job.Name, v.Log.Job.ID, jobWord(v.Log.Job.Status))
		if v.Log.Text == "" {
			fmt.Fprintln(w, "  (empty)")
			return
		}
		for line := range strings.SplitSeq(v.Log.Text, "\n") {
			fmt.Fprintln(w, "  │ "+line)
		}
		return
	}
	var failed []string
	for _, j := range v.Jobs {
		if j.Status == "failed" {
			failed = append(failed, j.Name)
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(w, "%s failed: `ade mr pipeline %s --log %s` shows the log, `ade mr retry %s` retries\n",
			plural(len(failed), "job"), v.Ref, failed[0], v.Ref)
	}
}
