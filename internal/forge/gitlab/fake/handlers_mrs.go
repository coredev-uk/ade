package fake

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

func (s *Server) mrRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v4/groups/{id}/merge_requests", s.groupMRs)
	mux.HandleFunc("GET /api/v4/projects/{id}/merge_requests", s.projectMRs)
	mux.HandleFunc("GET /api/v4/projects/{id}/merge_requests/{iid}", s.getMR)
	mux.HandleFunc("GET /api/v4/projects/{id}/merge_requests/{iid}/approvals", s.mrApprovals)
	mux.HandleFunc("GET /api/v4/projects/{id}/merge_requests/{iid}/discussions", s.discussions(forge.KindMR))
}

func (s *Server) groupMRs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.st.hasGroup(r.PathValue("id")) {
		notFound(w)
		return
	}
	s.writeMRs(w, r, s.st.mrs, nil)
}

func (s *Server) projectMRs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.st.findProject(r.PathValue("id"))
	if p == nil {
		notFound(w)
		return
	}
	var in []*mr
	for _, m := range s.st.mrs {
		if m.Project == p {
			in = append(in, m)
		}
	}
	s.writeMRs(w, r, in, p)
}

func (s *Server) writeMRs(w http.ResponseWriter, r *http.Request, in []*mr, ctx *project) {
	q := r.URL.Query()
	state := q.Get("state")
	after, _ := time.Parse(time.RFC3339, q.Get("updated_after"))
	var out []*mr
	for _, m := range in {
		switch {
		case state != "" && state != "all" && m.State != state:
		case q.Get("author_username") != "" && m.Author.Username != q.Get("author_username"):
		case q.Get("assignee_username") != "" && !hasUser(m.Assignees, q.Get("assignee_username")):
		case q.Get("reviewer_username") != "" && !hasUser(m.Reviewers, q.Get("reviewer_username")):
		case q.Get("source_branch") != "" && m.Source != q.Get("source_branch"):
		case !after.IsZero() && m.Updated.Before(after):
		default:
			out = append(out, m)
		}
	}
	slices.SortStableFunc(out, func(a, b *mr) int { return int(b.ID - a.ID) })
	paged(s, w, r, out, func(m *mr) any { return s.mrJSON(m, ctx, false) })
}

func hasUser(us []*user, username string) bool {
	return slices.ContainsFunc(us, func(u *user) bool { return u.Username == username })
}

func (s *Server) lookupMR(w http.ResponseWriter, r *http.Request) (*mr, bool) {
	p := s.st.findProject(r.PathValue("id"))
	iid, err := strconv.Atoi(r.PathValue("iid"))
	if p == nil || err != nil {
		notFound(w)
		return nil, false
	}
	m := s.st.findMR(p, iid)
	if m == nil {
		notFound(w)
		return nil, false
	}
	return m, true
}

func (s *Server) getMR(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.lookupMR(w, r); ok {
		writeJSON(w, http.StatusOK, s.mrJSON(m, m.Project, true))
	}
}

func (s *Server) mrApprovals(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.lookupMR(w, r); ok {
		writeJSON(w, http.StatusOK, s.approvalsJSON(m))
	}
}
