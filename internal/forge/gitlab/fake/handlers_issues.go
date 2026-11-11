package fake

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

func (s *Server) userRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, http.StatusOK, userJSON(s.st.me))
	})
}

func (s *Server) issueRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v4/issues", s.globalIssues)
	mux.HandleFunc("GET /api/v4/groups/{id}/issues", s.groupIssues)
	mux.HandleFunc("GET /api/v4/projects/{id}/issues", s.projectIssues)
	mux.HandleFunc("GET /api/v4/projects/{id}/issues/{iid}", s.getIssue)
	mux.HandleFunc("GET /api/v4/projects/{id}/issues/{iid}/links", s.issueLinks)
	mux.HandleFunc("GET /api/v4/projects/{id}/issues/{iid}/discussions", s.discussions(forge.KindIssue))
	mux.HandleFunc("GET /api/v4/projects/{id}/issues/{iid}/related_merge_requests", s.issueMRs(false))
	mux.HandleFunc("GET /api/v4/projects/{id}/issues/{iid}/closed_by", s.issueMRs(true))
}

func (s *Server) globalIssues(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var in []*issue
	switch scope := r.URL.Query().Get("scope"); scope {
	case "assigned_to_me":
		for _, is := range s.st.issues {
			if slices.Contains(is.Assignees, s.st.me) {
				in = append(in, is)
			}
		}
	case "all":
		// gitlab.com rejects this while restrict_global_issues_scope_all is on.
		writeJSON(w, http.StatusBadRequest, object{"message": "400 Bad request - scope=all is not allowed here"})
		return
	default: // created_by_me is the global default
		for _, is := range s.st.issues {
			if is.Author == s.st.me {
				in = append(in, is)
			}
		}
	}
	s.writeIssues(w, r, in, nil)
}

func (s *Server) groupIssues(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.st.hasGroup(r.PathValue("id")) {
		notFound(w)
		return
	}
	s.writeIssues(w, r, s.st.issues, nil)
}

func (s *Server) projectIssues(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.st.findProject(r.PathValue("id"))
	if p == nil {
		notFound(w)
		return
	}
	var in []*issue
	for _, is := range s.st.issues {
		if is.Project == p {
			in = append(in, is)
		}
	}
	s.writeIssues(w, r, in, p)
}

// writeIssues filters, orders (newest first, GitLab's default) and pages.
func (s *Server) writeIssues(w http.ResponseWriter, r *http.Request, in []*issue, ctx *project) {
	out := s.filterIssues(r, in)
	slices.SortStableFunc(out, func(a, b *issue) int { return int(b.ID - a.ID) })
	paged(s, w, r, out, func(is *issue) any { return s.issueJSON(is, ctx) })
}

func (s *Server) filterIssues(r *http.Request, in []*issue) []*issue {
	q := r.URL.Query()
	state := q.Get("state")
	labels := queryList(r, "labels")
	assignees := queryList(r, "assignee_username")
	after, _ := time.Parse(time.RFC3339, q.Get("updated_after"))
	iterationID, _ := strconv.ParseInt(q.Get("iteration_id"), 10, 64)

	var out []*issue
	for _, is := range in {
		switch {
		case state != "" && state != "all" && is.State != state:
		case !hasAll(is.Labels, labels):
		case q.Get("milestone") != "" && (is.Milestone == nil || is.Milestone.Title != q.Get("milestone")):
		// Free applies the filter too, and no issue there has an iteration.
		case iterationID != 0 && (is.Iteration == nil || is.Iteration.ID != iterationID):
		case len(assignees) > 0 && !assignedToAll(is, assignees):
		case !after.IsZero() && is.Updated.Before(after):
		default:
			out = append(out, is)
		}
	}
	return out
}

func hasAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

func assignedToAll(is *issue, usernames []string) bool {
	for _, name := range usernames {
		if !slices.ContainsFunc(is.Assignees, func(u *user) bool { return u.Username == name }) {
			return false
		}
	}
	return true
}

// lookupIssue resolves {id}/{iid} path values; it writes the 404 itself.
func (s *Server) lookupIssue(w http.ResponseWriter, r *http.Request) (*issue, bool) {
	p := s.st.findProject(r.PathValue("id"))
	iid, err := strconv.Atoi(r.PathValue("iid"))
	if p == nil || err != nil {
		notFound(w)
		return nil, false
	}
	is := s.st.findIssue(p, iid)
	if is == nil {
		notFound(w)
		return nil, false
	}
	return is, true
}

func (s *Server) getIssue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if is, ok := s.lookupIssue(w, r); ok {
		writeJSON(w, http.StatusOK, s.issueJSON(is, is.Project))
	}
}

// issueLinks lists links as GitLab does: issue objects of the other side with
// the link fields added, link_type relative to the queried issue.
func (s *Server) issueLinks(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, ok := s.lookupIssue(w, r)
	if !ok {
		return
	}
	type side struct {
		other *issue
		typ   string
		l     *link
	}
	var sides []side
	for _, l := range s.st.links {
		switch is {
		case l.A:
			sides = append(sides, side{l.B, l.Type, l})
		case l.B:
			sides = append(sides, side{l.A, inverse(l.Type), l})
		}
	}
	paged(s, w, r, sides, func(sd side) any {
		o := s.issueJSON(sd.other, is.Project)
		o["issue_link_id"] = sd.l.ID
		o["link_type"] = sd.typ
		o["link_created_at"] = ts(sd.l.Created)
		o["link_updated_at"] = ts(sd.l.Created)
		return o
	})
}

func inverse(t string) string {
	switch t {
	case "blocks":
		return "is_blocked_by"
	case "is_blocked_by":
		return "blocks"
	default:
		return t
	}
}

// issueMRs serves related_merge_requests (any MR referencing the issue) and,
// when closing is set, closed_by (MRs whose description closes it).
func (s *Server) issueMRs(closing bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		is, ok := s.lookupIssue(w, r)
		if !ok {
			return
		}
		var out []*mr
		for _, m := range s.st.mrs {
			closes := slices.Contains(m.Closes, is) && m.State != "closed"
			mentions := slices.Contains(m.Mentions, is)
			if (closing && closes) || (!closing && (closes || mentions)) {
				out = append(out, m)
			}
		}
		paged(s, w, r, out, func(m *mr) any { return s.mrJSON(m, is.Project, false) })
	}
}

func (s *Server) discussions(kind forge.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		p := s.st.findProject(r.PathValue("id"))
		iid, err := strconv.Atoi(r.PathValue("iid"))
		if p == nil || err != nil || !s.exists(p, iid, kind) {
			notFound(w)
			return
		}
		ref := s.st.ref(p, iid, kind)
		paged(s, w, r, s.st.threads[ref], func(d *discussion) any { return s.discussionJSON(d, ref) })
	}
}

func (s *Server) exists(p *project, iid int, kind forge.Kind) bool {
	if kind == forge.KindMR {
		return s.st.findMR(p, iid) != nil
	}
	return s.st.findIssue(p, iid) != nil
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}
