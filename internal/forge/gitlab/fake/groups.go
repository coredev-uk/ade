package fake

import (
	"net/http"
	"slices"
)

// Group membership, for reviewer groups: GET /groups/:id/members/all.

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v4/groups/{id}/members/all", s.groupMembers)
	})
}

// GroupMember is one membership of a group added with AddGroup. State is the
// user's state ("active" when empty; "blocked" and "deactivated" are what
// GitLab reports for users that cannot act) and Bot marks a bot account such
// as a group access token. Awaiting marks an invitation the user has not
// accepted yet (membership_state "awaiting").
type GroupMember struct {
	Username string
	State    string
	Bot      bool
	Awaiting bool
}

// groups maps a group path to its members, in listing order. The sandbox group
// itself has none unless added.
type groups map[string][]GroupMember

func (s *Server) groups() groups {
	return s.ext("groups", func() any { return groups{} }).(groups)
}

// AddUser adds a user to the instance, as an admin creating an account would,
// and returns the id. An existing username keeps its id.
func (s *Server) AddUser(username, name string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u := s.st.findUser(username); u != nil {
		return u.ID
	}
	u := &user{ID: s.st.id(), Username: username, Name: name}
	s.st.users = append(s.st.users, u)
	return u.ID
}

// AddGroup creates a group at path (subgroups included: "acme/reviewers") with
// the given members, replacing an earlier group of that path. Members must be
// users of the instance.
func (s *Server) AddGroup(path string, members ...GroupMember) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups()[path] = slices.Clone(members)
}

func (s *Server) groupMembers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	members, ok := s.groups()[r.PathValue("id")]
	if !ok {
		notFound(w)
		return
	}
	var out []GroupMember
	for _, m := range members {
		if s.st.findUser(m.Username) != nil {
			out = append(out, m)
		}
	}
	paged(s, w, r, out, func(m GroupMember) any {
		j := userJSON(s.st.findUser(m.Username))
		if m.State != "" {
			j["state"] = m.State
		}
		if m.Bot {
			j["bot"] = true
		}
		if m.Awaiting {
			j["membership_state"] = "awaiting"
		}
		j["access_level"] = 30
		return j
	})
}
