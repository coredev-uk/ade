package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
)

// ciWriter serves jobs and logs for GatherCITriage; the rest of MRWriter is
// never called.
type ciWriter struct {
	forge.MRWriter
	jobs      []forge.Job
	logs      map[int64]string
	jobCalls  int
	tailBytes int
}

func (w *ciWriter) Jobs(context.Context, string, int64) ([]forge.Job, error) {
	w.jobCalls++
	return w.jobs, nil
}

func (w *ciWriter) JobLog(_ context.Context, _ string, id int64, tail int) (string, error) {
	w.tailBytes = tail
	if l, ok := w.logs[id]; ok {
		return l, nil
	}
	return "", forge.ErrNotFound
}

func ciMR() forge.MR {
	return forge.MR{Ref: forge.Ref{Project: "grp/web", IID: 1, Kind: forge.KindMR}, Title: "Add pager", SourceBranch: "pager", TargetBranch: "main", Pipeline: &forge.Pipeline{ID: 9001, Status: "failed"}}
}

func TestGatherCITriageReadsOnlyFailedJobsAndTheCleanTailOfTheirLogs(t *testing.T) {
	var long strings.Builder
	for i := range 200 {
		fmt.Fprintf(&long, "line %d\n", i)
	}
	long.WriteString("\x1b[31meslint: 2 errors\x1b[0m\n")
	w := &ciWriter{
		jobs: []forge.Job{
			{ID: 3, Name: "test", Stage: "test", Status: "success"},
			{ID: 2, Name: "lint", Stage: "lint", Status: "failed"},
			{ID: 1, Name: "build", Stage: "build", Status: "failed"},
		},
		logs: map[int64]string{2: long.String()},
	}
	in, err := GatherCITriage(context.Background(), w, ciMR(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Failures) != 2 || in.Failures[0].Job.Name != "build" || in.Failures[1].Job.Name != "lint" {
		t.Fatalf("failures = %+v", in.Failures)
	}
	if in.Failures[0].Log != "" {
		t.Error("a job without a log has an empty tail, not an error")
	}
	got := in.Failures[1].Log
	if !strings.HasSuffix(got, "eslint: 2 errors") || strings.Contains(got, "\x1b") || strings.Contains(got, "line 0\n") || strings.Count(got, "\n") > triageLogLines {
		t.Errorf("the log tail should be clean and bounded:\n%s", got)
	}
	if w.tailBytes != triageFetchTail {
		t.Errorf("fetched %d bytes, want the bounded tail %d", w.tailBytes, triageFetchTail)
	}
	job := CITriageJob("sandbox", in)
	if !strings.Contains(job.Prompt, "### Job lint") || !strings.Contains(job.Prompt, "eslint: 2 errors") || !strings.Contains(job.Prompt, "(the job has no log)") {
		t.Errorf("prompt:\n%s", job.Prompt)
	}
	if job.Feature != "ci-triage" || job.Role != RoleExplain {
		t.Errorf("job = %+v", job)
	}
}

func TestGatherCITriageUsesJobsTheCallerHasAndSaysWhyThereIsNothingToDo(t *testing.T) {
	w := &ciWriter{}
	passed := []forge.Job{{ID: 1, Name: "build", Status: "success"}}
	if _, err := GatherCITriage(context.Background(), w, ciMR(), passed); !errors.Is(err, ErrNoFailedJobs) || w.jobCalls != 0 {
		t.Fatalf("err = %v (jobs read %d times)", err, w.jobCalls)
	}
	mr := ciMR()
	mr.Pipeline = nil
	if _, err := GatherCITriage(context.Background(), w, mr, nil); !errors.Is(err, ErrNoPipeline) {
		t.Fatalf("err = %v", err)
	}
}

func TestCITriageIsCachedByWhatTheLogsSayNotByJobIDs(t *testing.T) {
	e := newEnv(t)
	build := func(id int64) Job {
		w := &ciWriter{jobs: []forge.Job{{ID: id, Name: "lint", Stage: "lint", Status: "failed"}}, logs: map[int64]string{id: "eslint: 2 errors\n"}}
		in, err := GatherCITriage(context.Background(), w, ciMR(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return CITriageJob("sandbox", in)
	}
	first, err := e.svc.Run(context.Background(), build(11))
	if err != nil || first.CacheHit {
		t.Fatalf("first: %+v %v", first, err)
	}
	// A retry fails the same way under a new job id: free.
	second, err := e.svc.Run(context.Background(), build(42))
	if err != nil || !second.CacheHit || e.drv.calls != 1 {
		t.Fatalf("second: %+v %v calls=%d", second, err, e.drv.calls)
	}
}

func TestCITriageRefusedByACapSendsNothing(t *testing.T) {
	e := newEnv(t)
	e.conf.Budget.DailyUSD = 0.01
	e.log.Append(auditSpend(0.0099))
	w := &ciWriter{jobs: []forge.Job{{ID: 1, Name: "lint", Status: "failed"}}, logs: map[int64]string{1: "boom\n"}}
	in, _ := GatherCITriage(context.Background(), w, ciMR(), nil)
	if _, err := e.svc.Run(context.Background(), CITriageJob("sandbox", in)); err == nil || e.drv.calls != 0 {
		t.Fatalf("want a refusal without a call: %v (calls %d)", err, e.drv.calls)
	}
}
