package ai

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/joblog"
)

// CI triage (feedback F17): the failed jobs of an MR's pipeline, each with the
// end of its log, go to the model, which names the cause and a fix per job.
// The logs are read the way the MR page's log viewer reads them (JobLog, then
// joblog.Clean), so the model sees what the user would. Nothing is written to
// the forge.

const citriageVersion = "ci-triage-v1"

//go:embed prompts/citriage.md
var citriageSystem string

// Bounds that keep the prompt, and so the spend, small.
const (
	maxTriageJobs   = 6
	triageFetchTail = 32 << 10 // bytes of a job log read from the forge
	triageLogLines  = 80       // lines of it that reach the prompt
	triageLogChars  = 6000
)

// ErrNoPipeline and ErrNoFailedJobs are why there is nothing to triage.
var (
	ErrNoPipeline   = errors.New("the merge request has no pipeline")
	ErrNoFailedJobs = errors.New("the pipeline has no failed jobs")
)

// CIFailure is one failed job and the end of its log.
type CIFailure struct {
	Job forge.Job
	Log string // cleaned, last lines only; empty when the job has no log
}

// CITriageInput is what a triage job reads.
type CITriageInput struct {
	MR       forge.MR
	Failures []CIFailure
	// Omitted counts failed jobs beyond maxTriageJobs, left out of the prompt.
	Omitted int
}

// GatherCITriage reads the failed jobs of mr's pipeline and the tail of each
// log. jobs may carry the pipeline's jobs when the caller has them already
// (the MR page does); nil reads them from the forge.
func GatherCITriage(ctx context.Context, w forge.MRWriter, mr forge.MR, jobs []forge.Job) (CITriageInput, error) {
	if mr.Pipeline == nil {
		return CITriageInput{}, ErrNoPipeline
	}
	if jobs == nil {
		var err error
		if jobs, err = w.Jobs(ctx, mr.Ref.Project, mr.Pipeline.ID); err != nil {
			return CITriageInput{}, fmt.Errorf("read the jobs of %s: %w", mr.Ref, err)
		}
	}
	var failed []forge.Job
	for _, j := range jobs {
		if j.Status == "failed" {
			failed = append(failed, j)
		}
	}
	if len(failed) == 0 {
		return CITriageInput{}, ErrNoFailedJobs
	}
	slices.SortStableFunc(failed, func(a, b forge.Job) int { return int(a.ID - b.ID) })
	in := CITriageInput{MR: mr}
	if len(failed) > maxTriageJobs {
		in.Omitted, failed = len(failed)-maxTriageJobs, failed[:maxTriageJobs]
	}
	for _, j := range failed {
		f := CIFailure{Job: j}
		raw, err := w.JobLog(ctx, mr.Ref.Project, j.ID, triageFetchTail)
		switch {
		case errors.Is(err, forge.ErrNotFound):
		case err != nil:
			return CITriageInput{}, fmt.Errorf("read the log of job %s: %w", j.Name, err)
		default:
			lines := joblog.Clean(raw)
			if len(lines) > triageLogLines {
				lines = lines[len(lines)-triageLogLines:]
			}
			f.Log = tailChars(strings.Join(lines, "\n"), triageLogChars)
		}
		in.Failures = append(in.Failures, f)
	}
	return in, nil
}

// tailChars keeps the last n bytes of s, from a line start where one is near.
func tailChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[len(s)-n:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 && i < n/2 {
		cut = cut[i+1:]
	}
	return "… (earlier lines cut)\n" + cut
}

// CITriageJob builds the triage prompt. It holds what the logs say, not job
// ids or timings of the pipeline, so a retry that fails the same way is
// answered from the cache.
func CITriageJob(forgeName string, in CITriageInput) Job {
	mr := in.MR
	var b strings.Builder
	fmt.Fprintf(&b, "Merge request: %s\nBranches: %s -> %s\n", mr.Title, mr.SourceBranch, mr.TargetBranch)
	fmt.Fprintf(&b, "\n%d failed job(s):\n", len(in.Failures)+in.Omitted)
	for _, f := range in.Failures {
		fmt.Fprintf(&b, "\n### Job %s (stage %s)\n", f.Job.Name, f.Job.Stage)
		if f.Log == "" {
			b.WriteString("(the job has no log)\n")
			continue
		}
		b.WriteString("Log, last lines:\n```\n" + f.Log + "\n```\n")
	}
	if in.Omitted > 0 {
		fmt.Fprintf(&b, "\n(%d more failed jobs not shown)\n", in.Omitted)
	}
	return Job{
		Feature: "ci-triage", Role: RoleExplain, PromptVersion: citriageVersion,
		System: citriageSystem, Prompt: b.String(),
		Forge: forgeName, Ref: mr.Ref, Name: "citriage-mr" + fmt.Sprint(mr.Ref.IID),
		MaxTokens: 900,
		Params:    map[string]any{"failed": len(in.Failures) + in.Omitted},
	}
}
