package fake

import (
	"net/http"
	"slices"
	"time"
)

func (s *Server) checkRoutes(mux *http.ServeMux) {
	const r = "/repos/{owner}/{repo}"
	s.handle(mux, "GET "+r+"/commits/{ref}/check-runs", s.listCheckRuns)
	s.handle(mux, "GET "+r+"/commits/{ref}/status", s.combinedStatus)
	s.handle(mux, "GET "+r+"/actions/runs", s.listRuns)
	s.handle(mux, "GET "+r+"/actions/runs/{id}/jobs", s.listJobs)
	s.handle(mux, "GET "+r+"/actions/jobs/{id}/logs", s.jobLogs)
	s.handle(mux, "POST "+r+"/actions/runs/{id}/rerun-failed-jobs", s.rerunFailed)
}

type checkRef struct {
	run *run
	job *job
}

func (s *Server) listCheckRuns(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	ref := r.PathValue("ref")
	c := rp.resolveRef(ref)
	if c == nil {
		apiError(w, http.StatusUnprocessableEntity, "No commit found for SHA: "+ref)
		return
	}
	q := r.URL.Query()
	var out []checkRef
	for _, ru := range rp.runsFor(c.SHA) {
		for _, j := range ru.Jobs {
			if (q.Get("check_name") == "" || q.Get("check_name") == j.Name) && (q.Get("status") == "" || q.Get("status") == j.Status) {
				out = append(out, checkRef{ru, j})
			}
		}
	}
	pagedEnvelope(s, w, r, "check_runs", out, func(c checkRef) any { return s.renderCheckRun(rp, c.run, c.job) })
}

// combinedStatus answers like GitHub for a repo that only uses Actions: no
// commit statuses exist, and GitHub reports "pending" with none.
func (s *Server) combinedStatus(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	ref := r.PathValue("ref")
	c := rp.resolveRef(ref)
	if c == nil {
		apiError(w, http.StatusUnprocessableEntity, "No commit found for SHA: "+ref)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state": "pending", "sha": c.SHA, "total_count": 0, "statuses": []any{},
		"commit_url": s.repoAPI(rp) + "/commits/" + c.SHA, "url": s.repoAPI(rp) + "/commits/" + c.SHA + "/status",
	})
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	q := r.URL.Query()
	var out []*run
	for _, ru := range rp.Runs {
		switch {
		case q.Get("head_sha") != "" && ru.HeadSHA != q.Get("head_sha"):
		case q.Get("branch") != "" && ru.HeadBranch != q.Get("branch"):
		case q.Get("status") != "" && ru.Status != q.Get("status"):
		case q.Get("event") != "" && q.Get("event") != "pull_request":
		default:
			out = append(out, ru)
		}
	}
	slices.SortStableFunc(out, func(a, b *run) int {
		if c := b.Created.Compare(a.Created); c != 0 {
			return c
		}
		return int(b.ID - a.ID)
	})
	pagedEnvelope(s, w, r, "workflow_runs", out, func(ru *run) any { return s.renderRun(rp, ru) })
}

func (s *Server) runOf(w http.ResponseWriter, r *http.Request) (*repo, *run) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return nil, nil
	}
	ru := rp.run(atoi64(r.PathValue("id")))
	if ru == nil {
		notFound(w)
		return nil, nil
	}
	return rp, ru
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	rp, ru := s.runOf(w, r)
	if ru == nil {
		return
	}
	pagedEnvelope(s, w, r, "jobs", ru.Jobs, func(j *job) any { return s.renderJob(rp, ru, j) })
}

func (s *Server) jobLogs(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	_, j := rp.job(atoi64(r.PathValue("id")))
	if j == nil {
		notFound(w)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(j.Log))
}

// rerunFailed requeues the failed jobs of a completed run.
func (s *Server) rerunFailed(w http.ResponseWriter, r *http.Request) {
	_, ru := s.runOf(w, r)
	if ru == nil {
		return
	}
	var failed []*job
	for _, j := range ru.Jobs {
		if j.Conclusion == "failure" || j.Conclusion == "timed_out" || j.Conclusion == "cancelled" {
			failed = append(failed, j)
		}
	}
	if ru.Status != "completed" || len(failed) == 0 {
		apiError(w, http.StatusForbidden, "This workflow run cannot be retried")
		return
	}
	at := s.st.now()
	for _, j := range failed {
		j.Status, j.Conclusion, j.Started, j.Completed = "queued", "", at, time.Time{}
	}
	ru.Status, ru.Conclusion, ru.Updated = "in_progress", "", at
	ru.Attempt++
	ru.Reruns++
	writeJSON(w, http.StatusCreated, map[string]any{})
}
