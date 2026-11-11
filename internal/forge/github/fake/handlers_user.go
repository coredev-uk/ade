package fake

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

func (s *Server) userRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /user", s.getMe)
	s.handle(mux, "GET /user/{id}", s.getUserByID)
	s.handle(mux, "GET /users/{login}", s.getUser)
	s.handle(mux, "GET /repos/{owner}/{repo}", s.getRepo)
	s.handle(mux, "GET /repos/{owner}/{repo}/milestones", s.listMilestones)
	s.handle(mux, "POST /repos/{owner}/{repo}/milestones", s.createMilestone)
}

// repoOf resolves the {owner}/{repo} of a route, answering 404 when unknown.
func (s *Server) repoOf(w http.ResponseWriter, r *http.Request) *repo {
	rp := s.st.repo(r.PathValue("owner"), r.PathValue("repo"))
	if rp == nil {
		notFound(w)
	}
	return rp
}

func (s *Server) getMe(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.renderFullUser(s.st.user(viewer)))
}

func (s *Server) getUserByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || s.st.userByID(id) == nil {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, s.renderFullUser(s.st.userByID(id)))
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	u := s.st.user(r.PathValue("login"))
	if u == nil {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, s.renderFullUser(u))
}

func (s *Server) getRepo(w http.ResponseWriter, r *http.Request) {
	if rp := s.repoOf(w, r); rp != nil {
		writeJSON(w, http.StatusOK, s.renderRepo(rp))
	}
}

func (s *Server) listMilestones(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	var out []*milestone
	for _, m := range rp.Milestones {
		if state == "all" || m.State == state {
			out = append(out, m)
		}
	}
	less := func(a, b *milestone) int {
		switch {
		case a.DueOn.IsZero() && b.DueOn.IsZero():
			return a.Number - b.Number
		case a.DueOn.IsZero():
			return 1
		case b.DueOn.IsZero():
			return -1
		}
		return a.DueOn.Compare(b.DueOn)
	}
	if q.Get("sort") == "completeness" {
		less = func(a, b *milestone) int {
			ao, ac := rp.milestoneCounts(a)
			bo, bc := rp.milestoneCounts(b)
			return ac*(bo+bc) - bc*(ao+ac) // percent complete, cross-multiplied
		}
	}
	slices.SortStableFunc(out, less)
	if q.Get("direction") == "desc" {
		slices.Reverse(out)
	}
	paged(s, w, r, out, func(m *milestone) any { return s.renderMilestone(rp, m) })
}

func (s *Server) createMilestone(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	var in struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		DueOn       string `json:"due_on"`
		State       string `json:"state"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch {
	case strings.TrimSpace(in.Title) == "":
		validation(w, fieldError{Resource: "Milestone", Field: "title", Code: "missing_field"})
		return
	case in.State != "" && in.State != "open" && in.State != "closed":
		validation(w, fieldError{Resource: "Milestone", Field: "state", Code: "invalid"})
		return
	}
	for _, m := range rp.Milestones {
		if strings.EqualFold(m.Title, in.Title) {
			validation(w, fieldError{Resource: "Milestone", Field: "title", Code: "already_exists"})
			return
		}
	}
	var due time.Time
	if in.DueOn != "" {
		var err error
		if due, err = time.Parse(time.RFC3339, in.DueOn); err != nil {
			validation(w, fieldError{Resource: "Milestone", Field: "due_on", Code: "invalid"})
			return
		}
	}
	at := s.st.now()
	m := &milestone{
		ID: s.st.id(), Number: len(rp.Milestones) + 1, Title: in.Title, Description: in.Description,
		State: "open", DueOn: due.UTC(), Created: at, Updated: at,
	}
	if in.State == "closed" {
		m.State, m.Closed = "closed", at
	}
	rp.Milestones = append(rp.Milestones, m)
	writeJSON(w, http.StatusCreated, s.renderMilestone(rp, m))
}
