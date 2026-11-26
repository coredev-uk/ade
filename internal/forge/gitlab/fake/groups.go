package fake

import (
	"net/http"
	"slices"
	"strings"
)

// Group membership, for reviewer groups: GET /groups/:id (shared_with_groups),
// GET /groups/:id/members (direct members) and GET /groups/:id/members/all
// (direct, inherited from ancestor groups and from invited groups).

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v4/groups/{id}", s.getGroup)
		mux.HandleFunc("GET /api/v4/groups/{id}/members", s.directGroupMembers)
		mux.HandleFunc("GET /api/v4/groups/{id}/members/all", s.allGroupMembers)
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

type group struct {
	id      int64
	members []GroupMember
	invited []string // paths of groups invited into this one (ShareGroup)
}

// groups maps a group path to the groups added with AddGroup. The sandbox
// group itself has none unless added.
type groups map[string]*group

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
// the given direct members, replacing an earlier group of that path. Members
// must be users of the instance. A group whose ancestor ("acme") was added too
// inherits the ancestor's members, as GitLab's members/all reports them.
func (s *Server) AddGroup(path string, members ...GroupMember) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups()[path] = &group{id: s.st.id(), members: slices.Clone(members)}
}

// ShareGroup invites the group at invited into the group at path (Group →
// Manage → Members → Invite a group). Both must have been added.
func (s *Server) ShareGroup(path, invited string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groups()[path]
	if g == nil || s.groups()[invited] == nil {
		panic("fake: ShareGroup needs two added groups")
	}
	g.invited = append(g.invited, invited)
}

func (s *Server) getGroup(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := r.PathValue("id")
	g, ok := s.groups()[path]
	if !ok {
		notFound(w)
		return
	}
	shared := []object{}
	for _, p := range g.invited {
		shared = append(shared, object{
			"group_id": s.groups()[p].id, "group_name": p[strings.LastIndex(p, "/")+1:],
			"group_full_path": p, "group_access_level": 30, "expires_at": nil,
		})
	}
	writeJSON(w, http.StatusOK, object{
		"id": g.id, "name": path[strings.LastIndex(path, "/")+1:], "path": path[strings.LastIndex(path, "/")+1:],
		"full_path": path, "web_url": s.webURL("groups/" + path), "shared_with_groups": shared,
	})
}

func (s *Server) directGroupMembers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups()[r.PathValue("id")]
	if !ok {
		notFound(w)
		return
	}
	s.renderMembers(w, r, g.members)
}

// allGroupMembers lists the group's direct members, then those of its added
// ancestors (nearest first), then those of its invited groups, each user once.
func (s *Server) allGroupMembers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := r.PathValue("id")
	g, ok := s.groups()[path]
	if !ok {
		notFound(w)
		return
	}
	all := slices.Clone(g.members)
	for p := path; strings.Contains(p, "/"); {
		p = p[:strings.LastIndex(p, "/")]
		if a := s.groups()[p]; a != nil {
			all = append(all, a.members...)
		}
	}
	for _, p := range g.invited {
		all = append(all, s.groups()[p].members...)
	}
	seen := map[string]bool{}
	all = slices.DeleteFunc(all, func(m GroupMember) bool {
		dup := seen[m.Username]
		seen[m.Username] = true
		return dup
	})
	s.renderMembers(w, r, all)
}

func (s *Server) renderMembers(w http.ResponseWriter, r *http.Request, members []GroupMember) {
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
