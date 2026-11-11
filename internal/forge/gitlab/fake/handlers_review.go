package fake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// The review endpoints: diffs, threads, draft notes with bulk publish,
// approvals, merge, title edits and CI. Writes act as the sandbox user.

func (s *Server) reviewRoutes(mux *http.ServeMux) {
	const mrp = "/api/v4/projects/{id}/merge_requests/{iid}"
	mux.HandleFunc("GET "+mrp+"/versions", s.mrVersions)
	mux.HandleFunc("GET "+mrp+"/diffs", s.mrDiffs)
	mux.HandleFunc("PUT "+mrp, s.updateMR)
	mux.HandleFunc("POST "+mrp+"/discussions", s.createDiscussion)
	mux.HandleFunc("POST "+mrp+"/discussions/{did}/notes", s.replyDiscussion)
	mux.HandleFunc("PUT "+mrp+"/discussions/{did}", s.resolveDiscussion)
	mux.HandleFunc("GET "+mrp+"/draft_notes", s.listDrafts)
	mux.HandleFunc("POST "+mrp+"/draft_notes", s.createDraft)
	mux.HandleFunc("POST "+mrp+"/draft_notes/bulk_publish", s.bulkPublish)
	mux.HandleFunc("DELETE "+mrp+"/draft_notes/{draft}", s.deleteDraft)
	mux.HandleFunc("POST "+mrp+"/approve", s.approve(true))
	mux.HandleFunc("POST "+mrp+"/unapprove", s.approve(false))
	mux.HandleFunc("PUT "+mrp+"/merge", s.mergeMR)
	mux.HandleFunc("POST /api/v4/projects/{id}/pipelines/{pid}/retry", s.retryPipeline)
	mux.HandleFunc("GET /api/v4/projects/{id}/pipelines/{pid}/jobs", s.pipelineJobs)
	mux.HandleFunc("GET /api/v4/projects/{id}/jobs/{jid}/trace", s.jobTrace)
}

// params merges the JSON body, form fields and query string into one map, the
// way GitLab accepts any of them. "position[new_line]" keys nest under
// "position".
func params(r *http.Request) map[string]any {
	p := map[string]any{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(r.Body).Decode(&p)
	}
	_ = r.ParseForm()
	for k, vs := range r.Form {
		if len(vs) == 0 {
			continue
		}
		if name, sub, ok := strings.Cut(strings.TrimSuffix(k, "]"), "["); ok {
			m, _ := p[name].(map[string]any)
			if m == nil {
				m = map[string]any{}
				p[name] = m
			}
			m[sub] = vs[0]
			continue
		}
		p[k] = vs[0]
	}
	return p
}

func str(p map[string]any, k string) string {
	switch v := p[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func flag(p map[string]any, k string) bool { return str(p, k) == "true" }

func intOf(p map[string]any, k string) int {
	n, _ := strconv.Atoi(str(p, k))
	return n
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"message": "400 Bad request - " + msg})
}

func (s *Server) mrVersions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.lookupMR(w, r)
	if !ok {
		return
	}
	var out []object
	if m.Diff != nil {
		out = append(out, object{
			"id": m.ID, "head_commit_sha": m.Diff.Head, "base_commit_sha": m.Diff.Base, "start_commit_sha": m.Diff.Start,
			"created_at": ts(m.Created), "merge_request_id": m.ID, "state": "collected",
			"real_size": strconv.Itoa(len(m.Diff.Files)), "patch_id_sha": m.Diff.Head,
		})
	}
	paged(s, w, r, out, func(o object) any { return o })
}

func (s *Server) mrDiffs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.lookupMR(w, r)
	if !ok {
		return
	}
	var files []diffFile
	if m.Diff != nil {
		files = m.Diff.Files
	}
	paged(s, w, r, files, func(f diffFile) any {
		return object{
			"old_path": f.Old, "new_path": f.New, "a_mode": "100644", "b_mode": "100644", "diff": f.Diff,
			"collapsed": false, "too_large": false, "new_file": f.NewFile, "renamed_file": f.Renamed,
			"deleted_file": f.Deleted, "generated_file": false,
		}
	})
}

// updateMR handles the one field ADE edits: the title, whose "Draft:" prefix
// is the only way REST sets draft.
func (s *Server) updateMR(w http.ResponseWriter, r *http.Request) {
	p := params(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.lookupMR(w, r)
	if !ok {
		return
	}
	title := str(p, "title")
	if title == "" {
		badRequest(w, "title is empty")
		return
	}
	m.Title, m.Draft, m.Updated = title, hasDraftPrefix(title), s.st.clock
	writeJSON(w, http.StatusOK, s.mrJSON(m, m.Project, true))
}

func hasDraftPrefix(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	return strings.HasPrefix(t, "draft:") || strings.HasPrefix(t, "[draft]") || strings.HasPrefix(t, "(draft)")
}

// position validates and converts the request's position object the way
// GitLab does: it must quote the latest version's SHAs and name a line the
// diff shows, with both numbers for an unchanged line.
func (s *Server) position(m *mr, p map[string]any) (*position, string) {
	pm, _ := p["position"].(map[string]any)
	if pm == nil {
		return nil, ""
	}
	if str(pm, "position_type") != "text" {
		return nil, "position_type must be text"
	}
	if m.Diff == nil {
		return nil, "the merge request has no diff yet"
	}
	pos := &position{
		Path: str(pm, "new_path"), OldPath: str(pm, "old_path"), NewLine: intOf(pm, "new_line"), OldLine: intOf(pm, "old_line"),
		Base: str(pm, "base_sha"), Start: str(pm, "start_sha"), Head: str(pm, "head_sha"),
	}
	if pos.Base != m.Diff.Base || pos.Start != m.Diff.Start || pos.Head != m.Diff.Head {
		return nil, "Note {:line_code=>[\"can't be blank\", \"must be a valid line code\"], :position=>[\"is invalid: SHAs are not the latest version\"]}"
	}
	i := slices.IndexFunc(m.Diff.Files, func(f diffFile) bool { return f.New == pos.Path && f.Old == pos.OldPath })
	if i < 0 {
		return nil, "Note {:position=>[\"is invalid: file " + pos.Path + " is not in the diff\"]}"
	}
	if !m.Diff.Files[i].anchors(pos.OldLine, pos.NewLine) {
		return nil, "Note {:line_code=>[\"can't be blank\", \"must be a valid line code\"]}"
	}
	if pos.OldPath == pos.Path {
		pos.OldPath = ""
	}
	return pos, ""
}

func (s *Server) newThread(ref forge.Ref, body string, pos *position) *discussion {
	d := &discussion{ID: fmt.Sprintf("%040x", s.st.id()), Notes: []*note{{
		ID: s.st.id(), Author: s.st.me, Body: body, Created: s.st.clock, Resolvable: true, Position: pos,
	}}}
	s.st.threads[ref] = append(s.st.threads[ref], d)
	return d
}

func (s *Server) createDiscussion(w http.ResponseWriter, r *http.Request) {
	p := params(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.lookupMR(w, r)
	if !ok {
		return
	}
	body := str(p, "body")
	if body == "" {
		badRequest(w, "body is missing")
		return
	}
	pos, msg := s.position(m, p)
	if msg != "" {
		badRequest(w, msg)
		return
	}
	ref := s.st.ref(m.Project, m.IID, forge.KindMR)
	d := s.newThread(ref, body, pos)
	m.Updated = s.st.clock
	writeJSON(w, http.StatusCreated, s.discussionJSON(d, ref))
}

func (s *Server) lookupThread(w http.ResponseWriter, r *http.Request) (*mr, *discussion, bool) {
	m, ok := s.lookupMR(w, r)
	if !ok {
		return nil, nil, false
	}
	d := s.thread(s.st.ref(m.Project, m.IID, forge.KindMR), r.PathValue("did"))
	if d == nil {
		notFound(w)
		return nil, nil, false
	}
	return m, d, true
}

func (s *Server) replyDiscussion(w http.ResponseWriter, r *http.Request) {
	p := params(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	m, d, ok := s.lookupThread(w, r)
	if !ok {
		return
	}
	body := str(p, "body")
	switch {
	case body == "":
		badRequest(w, "body is missing")
		return
	case d.Individual:
		badRequest(w, "this discussion takes no replies")
		return
	}
	n := &note{ID: s.st.id(), Author: s.st.me, Body: body, Created: s.st.clock, Resolvable: d.Notes[0].Resolvable}
	d.Notes = append(d.Notes, n)
	m.Updated = s.st.clock
	writeJSON(w, http.StatusCreated, s.noteJSON(n, s.st.ref(m.Project, m.IID, forge.KindMR)))
}

func (s *Server) resolveDiscussion(w http.ResponseWriter, r *http.Request) {
	p := params(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	m, d, ok := s.lookupThread(w, r)
	if !ok {
		return
	}
	if _, set := p["resolved"]; !set {
		badRequest(w, "resolved is missing")
		return
	}
	if !d.Notes[0].Resolvable {
		badRequest(w, "this discussion is not resolvable")
		return
	}
	for _, n := range d.Notes {
		if n.Resolvable {
			n.Resolved = flag(p, "resolved")
		}
	}
	m.Updated = s.st.clock
	writeJSON(w, http.StatusOK, s.discussionJSON(d, s.st.ref(m.Project, m.IID, forge.KindMR)))
}

func (s *Server) draftJSON(m *mr, d *draftNote) object {
	o := object{
		"id": d.ID, "author_id": s.st.me.ID, "merge_request_id": m.ID, "resolve_discussion": false,
		"discussion_id": nil, "note": d.Body, "commit_id": nil, "line_code": nil, "position": nil,
	}
	if d.InReplyTo != "" {
		o["discussion_id"] = d.InReplyTo
	}
	if d.Position != nil {
		o["position"] = positionJSON(d.Position)
	}
	return o
}

func (s *Server) listDrafts(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.lookupMR(w, r)
	if !ok {
		return
	}
	paged(s, w, r, m.Drafts, func(d *draftNote) any { return s.draftJSON(m, d) })
}

func (s *Server) createDraft(w http.ResponseWriter, r *http.Request) {
	p := params(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.lookupMR(w, r)
	if !ok {
		return
	}
	d := &draftNote{Body: str(p, "note"), InReplyTo: str(p, "in_reply_to_discussion_id")}
	if d.Body == "" {
		badRequest(w, "note is missing")
		return
	}
	if d.InReplyTo != "" {
		if s.thread(s.st.ref(m.Project, m.IID, forge.KindMR), d.InReplyTo) == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "404 Discussion Not Found"})
			return
		}
	}
	pos, msg := s.position(m, p)
	if msg != "" {
		badRequest(w, msg)
		return
	}
	d.ID, d.Position = s.st.id(), pos
	m.Drafts = append(m.Drafts, d)
	writeJSON(w, http.StatusCreated, s.draftJSON(m, d))
}

func (s *Server) deleteDraft(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.lookupMR(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("draft"), 10, 64)
	i := slices.IndexFunc(m.Drafts, func(d *draftNote) bool { return d.ID == id })
	if i < 0 {
		notFound(w)
		return
	}
	m.Drafts = slices.Delete(m.Drafts, i, i+1)
	w.WriteHeader(http.StatusNoContent)
}

// bulkPublish turns every draft into a published note at once: new threads
// for positioned and overview drafts, replies for the others. All carry the
// same timestamp, as one publish does on GitLab.
func (s *Server) bulkPublish(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.lookupMR(w, r)
	if !ok {
		return
	}
	if len(m.Drafts) == 0 {
		notFound(w)
		return
	}
	ref := s.st.ref(m.Project, m.IID, forge.KindMR)
	for _, d := range m.Drafts {
		if d.InReplyTo != "" {
			if t := s.thread(ref, d.InReplyTo); t != nil {
				t.Notes = append(t.Notes, &note{
					ID: s.st.id(), Author: s.st.me, Body: d.Body, Created: s.st.clock, Resolvable: t.Notes[0].Resolvable,
				})
			}
			continue
		}
		s.newThread(ref, d.Body, d.Position)
	}
	m.Drafts = nil
	m.Publishes++
	m.Updated = s.st.clock
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) approve(add bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := params(r)
		s.mu.Lock()
		defer s.mu.Unlock()
		m, ok := s.lookupMR(w, r)
		if !ok {
			return
		}
		if m.State != "opened" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "401 Unauthorized"})
			return
		}
		if sha := str(p, "sha"); add && sha != "" && sha != m.SHA {
			writeJSON(w, http.StatusConflict, map[string]string{"message": "SHA does not match HEAD of source branch"})
			return
		}
		me := s.st.me
		m.Approved = slices.DeleteFunc(m.Approved, func(u *user) bool { return u == me })
		if add {
			m.Approved = append(m.Approved, me)
		}
		m.Updated = s.st.clock
		writeJSON(w, http.StatusCreated, s.approvalsJSON(m))
	}
}

func (s *Server) mergeMR(w http.ResponseWriter, r *http.Request) {
	p := params(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.lookupMR(w, r)
	if !ok {
		return
	}
	if sha := str(p, "sha"); sha != "" && sha != m.SHA {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "SHA does not match HEAD of source branch"})
		return
	}
	if m.State != "opened" || m.Draft {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "405 Method Not Allowed"})
		return
	}
	m.MergeParams = p
	pending := m.Pipeline != nil && (m.Pipeline.Status == "running" || m.Pipeline.Status == "pending")
	switch {
	case s.detailedStatus(m) != "mergeable" && !flag(p, "auto_merge"):
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "405 Method Not Allowed"})
		return
	case flag(p, "auto_merge") && (pending || s.detailedStatus(m) != "mergeable"):
		m.AutoMerge = true
	default:
		now := s.st.clock
		m.State, m.Merged, m.MergedBy = "merged", &now, s.st.me
	}
	m.Updated = s.st.clock
	writeJSON(w, http.StatusOK, s.mrJSON(m, m.Project, true))
}

// pipelineOf finds a project's pipeline by id and the MR that owns it.
func (s *Server) pipelineOf(r *http.Request) (*mr, *pipeline) {
	p := s.st.findProject(r.PathValue("id"))
	id, err := strconv.ParseInt(r.PathValue("pid"), 10, 64)
	if p == nil || err != nil {
		return nil, nil
	}
	for _, m := range s.st.mrs {
		if m.Project == p && m.Pipeline != nil && m.Pipeline.ID == id {
			return m, m.Pipeline
		}
	}
	return nil, nil
}

// retryPipeline retries the failed jobs of the same pipeline, as GitLab does:
// each gets a fresh pending job and the old one is hidden as retried.
func (s *Server) retryPipeline(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, p := s.pipelineOf(r)
	if p == nil {
		notFound(w)
		return
	}
	var fresh []*job
	for _, j := range p.Jobs {
		if !j.Retried && (j.Status == "failed" || j.Status == "canceled") {
			j.Retried = true
			fresh = append(fresh, &job{ID: s.st.id(), Name: j.Name, Stage: j.Stage, Status: "pending"})
		}
	}
	if len(fresh) > 0 {
		p.Jobs = append(p.Jobs, fresh...)
		p.Status, p.Updated, m.Updated = "pending", s.st.clock, s.st.clock
	}
	m.Retries++
	writeJSON(w, http.StatusCreated, s.pipelineJSON(p, m))
}

func (s *Server) jobJSON(j *job, m *mr, p *pipeline) object {
	return object{
		"id": j.ID, "name": j.Name, "stage": j.Stage, "status": j.Status, "duration": j.Duration,
		"web_url":  s.webURL(fmt.Sprintf("%s/-/jobs/%d", m.Project.Path, j.ID)),
		"pipeline": object{"id": p.ID, "project_id": m.Project.ID, "ref": m.Source, "sha": m.SHA, "status": p.Status},
	}
}

func (s *Server) pipelineJobs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, p := s.pipelineOf(r)
	if p == nil {
		notFound(w)
		return
	}
	jobs := slices.DeleteFunc(slices.Clone(p.Jobs), func(j *job) bool { return j.Retried && r.URL.Query().Get("include_retried") != "true" })
	slices.SortFunc(jobs, func(a, b *job) int { return int(b.ID - a.ID) })
	paged(s, w, r, jobs, func(j *job) any { return s.jobJSON(j, m, p) })
}

func (s *Server) jobTrace(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pr := s.st.findProject(r.PathValue("id"))
	id, _ := strconv.ParseInt(r.PathValue("jid"), 10, 64)
	for _, m := range s.st.mrs {
		if pr == nil || m.Project != pr || m.Pipeline == nil {
			continue
		}
		for _, j := range m.Pipeline.Jobs {
			if j.ID == id && j.Trace != "" {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				_, _ = w.Write([]byte(j.Trace))
				return
			}
		}
	}
	notFound(w)
}
