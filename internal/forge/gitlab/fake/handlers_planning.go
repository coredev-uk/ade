package fake

import (
	"net/http"
	"slices"
	"strings"
)

func (s *Server) planningRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v4/groups/{id}/milestones", s.groupMilestones)
	mux.HandleFunc("GET /api/v4/projects/{id}/milestones", s.projectMilestones)
	mux.HandleFunc("GET /api/v4/groups/{id}/iterations", s.groupIterations)
	mux.HandleFunc("GET /api/v4/projects/{id}/approval_rules", s.approvalRules)
	mux.HandleFunc("GET /api/v4/groups/{id}/projects", s.groupProjects)
}

func (s *Server) groupMilestones(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.st.hasGroup(r.PathValue("id")) {
		notFound(w)
		return
	}
	s.writeMilestones(w, r)
}

// projectMilestones serves the project endpoint; the sandbox keeps sprint
// milestones on the group, so a project sees them as ancestors only when
// include_ancestors is set, like GitLab.
func (s *Server) projectMilestones(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.findProject(r.PathValue("id")) == nil {
		notFound(w)
		return
	}
	if r.URL.Query().Get("include_ancestors") != "true" {
		writeJSON(w, http.StatusOK, []object{})
		return
	}
	s.writeMilestones(w, r)
}

func (s *Server) writeMilestones(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	var out []*milestone
	for _, m := range s.st.milestones {
		if state == "" || m.State == state {
			out = append(out, m)
		}
	}
	paged(s, w, r, out, func(m *milestone) any { return s.milestoneJSON(m) })
}

// groupIterations is Premium only: Free answers 403, verified on gitlab.com.
// The state filter is the only reliable way to learn an iteration's state,
// since the response carries an undocumented integer.
func (s *Server) groupIterations(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tier != Premium {
		forbidden(w)
		return
	}
	if !s.st.hasGroup(r.PathValue("id")) {
		notFound(w)
		return
	}
	q := r.URL.Query()
	state, search, in := q.Get("state"), q.Get("search"), q.Get("in")
	var out []*iteration
	for _, it := range s.st.iterations {
		switch {
		case state != "" && state != "all" && state != "opened" && s.iterationStateName(it) != state:
		case state == "opened" && s.iterationStateName(it) == "closed":
		case search != "" && in == "cadence_title" && !containsFold(it.Cadence, search):
		case search != "" && in != "cadence_title" && !containsFold(it.Title, search):
		default:
			out = append(out, it)
		}
	}
	slices.SortStableFunc(out, func(a, b *iteration) int { return a.Start.Compare(b.Start) })
	paged(s, w, r, out, func(it *iteration) any { return s.iterationJSON(it) })
}

// approvalRules answers 200 on every tier, like gitlab.com Free (an empty
// list); only Premium has rules to show.
func (s *Server) approvalRules(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.findProject(r.PathValue("id")) == nil {
		notFound(w)
		return
	}
	rules := []object{}
	if s.tier == Premium {
		rules = append(rules, object{
			"id": 1, "name": "All Members", "rule_type": "any_approver", "eligible_approvers": []object{},
			"approvals_required": s.st.approvalsRequired, "users": []object{}, "groups": []object{},
			"contains_hidden_groups": false, "protected_branches": []object{},
		})
	}
	writeJSON(w, http.StatusOK, rules)
}

func (s *Server) groupProjects(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.st.hasGroup(r.PathValue("id")) {
		notFound(w)
		return
	}
	paged(s, w, r, s.st.projects, func(p *project) any {
		return object{
			"id": p.ID, "name": p.Path[strings.LastIndex(p.Path, "/")+1:], "path_with_namespace": p.Path,
			"web_url": s.webURL(p.Path),
		}
	})
}
