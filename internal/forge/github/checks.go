package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// GitHub has no pipeline object. ADE's Pipeline is the head commit's CI as a
// whole: its status aggregates every check run and commit status, its ID is
// the newest Actions workflow run (0 when the checks come from elsewhere),
// and Jobs are that run's jobs.

type wireCheckRun struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	HTMLURL     string     `json:"html_url"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type wireCombinedStatus struct {
	TotalCount int `json:"total_count"`
	Statuses   []struct {
		State     string    `json:"state"`
		UpdatedAt time.Time `json:"updated_at"`
	} `json:"statuses"`
}

type wireRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type wireJob struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	WorkflowName string     `json:"workflow_name"`
	Status       string     `json:"status"`
	Conclusion   string     `json:"conclusion"`
	HTMLURL      string     `json:"html_url"`
	StartedAt    *time.Time `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at"`
}

// Pipeline statuses ordered by how much they should dominate an aggregate.
var statusRank = map[string]int{
	"skipped": 0, "success": 1, "canceled": 2, "manual": 3, "pending": 4, "running": 5, "failed": 6,
}

// checkStatus maps a check run or job's status and conclusion to the
// forge.Pipeline vocabulary.
func checkStatus(status, conclusion string) string {
	switch status {
	case "in_progress":
		return "running"
	case "queued", "waiting", "requested", "pending":
		return "pending"
	}
	switch conclusion {
	case "success", "neutral":
		return "success"
	case "skipped":
		return "skipped"
	case "cancelled":
		return "canceled"
	case "action_required":
		return "manual"
	case "":
		return "pending"
	}
	return "failed" // failure, timed_out, startup_failure, stale
}

// statusState maps a commit status state.
func statusState(state string) string {
	switch state {
	case "success":
		return "success"
	case "pending":
		return "pending"
	}
	return "failed" // failure, error
}

// worst folds statuses into one, keeping the most urgent.
func worst(statuses []string) string {
	out := ""
	for _, s := range statuses {
		if out == "" || statusRank[s] > statusRank[out] {
			out = s
		}
	}
	return out
}

// headPipeline reads the CI state of sha, or nil when nothing reports on it.
// Anything GitHub withholds (no Actions, no checks permission) is treated as
// nothing to report: a missing pipeline must never fail an MR read.
func (c *Client) headPipeline(ctx context.Context, project, sha string) (*forge.Pipeline, error) {
	if sha == "" {
		return nil, nil
	}
	var (
		checks []wireCheckRun
		status wireCombinedStatus
		runs   []wireRun
	)
	base := repoPath(project)
	err := concurrently(
		func() (err error) {
			checks, err = getAllIn[wireCheckRun](ctx, c, base+"/commits/"+url.PathEscape(sha)+"/check-runs", nil, false, "check_runs")
			return ignoreMissing(err)
		},
		func() error {
			return ignoreMissing(c.getJSON(ctx, base+"/commits/"+url.PathEscape(sha)+"/status", nil, false, &status))
		},
		func() error {
			var wrap struct {
				Runs []wireRun `json:"workflow_runs"`
			}
			err := c.getJSON(ctx, base+"/actions/runs", url.Values{"head_sha": {sha}, "per_page": {"1"}}, false, &wrap)
			runs = wrap.Runs
			return ignoreMissing(err)
		},
	)
	if err != nil {
		return nil, err
	}
	var (
		all     []string
		updated time.Time
		webURL  string
	)
	for _, ch := range checks {
		all = append(all, checkStatus(ch.Status, ch.Conclusion))
		for _, t := range []*time.Time{ch.StartedAt, ch.CompletedAt} {
			if t != nil && t.After(updated) {
				updated = *t
			}
		}
		if webURL == "" {
			webURL = ch.HTMLURL
		}
	}
	if status.TotalCount > 0 {
		for _, s := range status.Statuses {
			all = append(all, statusState(s.State))
			updated = laterOf(updated, s.UpdatedAt)
		}
	}
	p := &forge.Pipeline{Status: worst(all), WebURL: webURL, UpdatedAt: updated}
	if len(runs) > 0 {
		p.ID, p.WebURL = runs[0].ID, runs[0].HTMLURL
		p.UpdatedAt = laterOf(p.UpdatedAt, runs[0].UpdatedAt)
		if p.Status == "" {
			p.Status = checkStatus(runs[0].Status, runs[0].Conclusion)
		}
	}
	if p.Status == "" {
		return nil, nil
	}
	return p, nil
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// ignoreMissing drops the answers that mean "no such CI data here".
func ignoreMissing(err error) error {
	if errors.Is(err, forge.ErrNotFound) || errors.Is(err, forge.ErrUnavailable) {
		return nil
	}
	return err
}

// Jobs implements forge.MRWriter: the jobs of a workflow run, in GitHub's
// order. A zero pipelineID (checks that are not Actions) has no jobs.
func (c *Client) Jobs(ctx context.Context, project string, pipelineID int64) ([]forge.Job, error) {
	if pipelineID <= 0 {
		return nil, nil
	}
	raw, err := getAllIn[wireJob](ctx, c, runPath(project, pipelineID)+"/jobs", nil, false, "jobs")
	if err != nil {
		return nil, err
	}
	out := make([]forge.Job, len(raw))
	for i, j := range raw {
		out[i] = forge.Job{
			ID: j.ID, Name: j.Name, Stage: j.WorkflowName, Status: checkStatus(j.Status, j.Conclusion), WebURL: j.HTMLURL,
		}
		if j.StartedAt != nil && j.CompletedAt != nil {
			out[i].Duration = j.CompletedAt.Sub(*j.StartedAt).Seconds()
		}
	}
	return out, nil
}

// JobLog implements forge.MRWriter. GitHub answers with a redirect to the
// log archive; the HTTP client follows it (dropping the token on the way,
// as the target is another host). Only the last tailBytes are kept.
func (c *Client) JobLog(ctx context.Context, project string, jobID int64, tailBytes int) (string, error) {
	path := repoPath(project) + "/actions/jobs/" + strconv.FormatInt(jobID, 10) + "/logs"
	resp, err := c.do(ctx, request{method: http.MethodGet, path: path, accept: "*/*"})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return readTail(resp.Body, tailBytes)
}

// RetryPipeline implements forge.MRWriter: GitHub re-runs the failed jobs of
// the workflow run. A run that is still going cannot be re-run (403).
func (c *Client) RetryPipeline(ctx context.Context, project string, pipelineID int64) error {
	return c.send(ctx, http.MethodPost, runPath(project, pipelineID)+"/rerun-failed-jobs", nil, nil)
}

func runPath(project string, id int64) string {
	return repoPath(project) + "/actions/runs/" + strconv.FormatInt(id, 10)
}
