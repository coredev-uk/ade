package fake

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// RequestReview adds username as a reviewer of the MR, as a teammate asking
// for a review would: the MR then lists under "Reviews requested" for that
// user. A user already requested is not added twice.
func (s *Server) RequestReview(ref forge.Ref, username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return err
	}
	u := s.st.findUser(username)
	if u == nil {
		return fmt.Errorf("fake gitlab: no user %q", username)
	}
	for _, r := range m.Reviewers {
		if r == u {
			return nil
		}
	}
	m.Reviewers = append(m.Reviewers, u)
	return nil
}

// ApproveAs approves the MR as username, who may be the sandbox user or a
// teammate.
func (s *Server) ApproveAs(ref forge.Ref, username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return err
	}
	u := s.st.findUser(username)
	if u == nil {
		return fmt.Errorf("fake gitlab: no user %q", username)
	}
	if !slices.Contains(m.Approved, u) {
		m.Approved = append(m.Approved, u)
	}
	m.Updated = s.st.clock
	return nil
}

// SetReviewState records username's verdict on the MR (unreviewed,
// review_started, approved, requested_changes), adding them as a reviewer when
// they were not one, as GitLab does when someone reviews without being asked.
func (s *Server) SetReviewState(ref forge.Ref, username, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return err
	}
	u := s.st.findUser(username)
	if u == nil {
		return fmt.Errorf("fake gitlab: no user %q", username)
	}
	if !slices.Contains(m.Reviewers, u) {
		m.Reviewers = append(m.Reviewers, u)
	}
	if m.ReviewState == nil {
		m.ReviewState = map[string]string{}
	}
	m.ReviewState[username] = state
	m.Updated = s.st.clock
	return nil
}

// mrReviewers serves GET .../reviewers: each reviewer with their verdict.
func (s *Server) mrReviewers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.lookupMR(w, r)
	if !ok {
		return
	}
	out := make([]object, 0, len(m.Reviewers))
	for _, u := range m.Reviewers {
		state := m.ReviewState[u.Username]
		if state == "" {
			state = "unreviewed"
		}
		out = append(out, object{"user": userJSON(u), "state": state, "created_at": ts(m.Created)})
	}
	writeJSON(w, http.StatusOK, out)
}

// ReviewerState is username's verdict on the MR: "unreviewed" for a reviewer
// who has not reviewed, "" for someone who is not a reviewer.
func (s *Server) ReviewerState(ref forge.Ref, username string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return "", err
	}
	u := s.st.findUser(username)
	if u == nil || !slices.Contains(m.Reviewers, u) {
		return "", nil
	}
	if st := m.ReviewState[username]; st != "" {
		return st, nil
	}
	return "unreviewed", nil
}

// listUsers serves GET /users?username=: an exact, case-insensitive lookup
// (the only filter the driver uses); without it, every user.
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := r.URL.Query().Get("username")
	var out []*user
	for _, u := range s.st.users {
		if name == "" || strings.EqualFold(u.Username, name) {
			out = append(out, u)
		}
	}
	writeJSON(w, http.StatusOK, usersJSON(out))
}

// setReviewerIDs applies reviewer_ids of PUT .../merge_requests/:iid: it
// replaces the reviewer set, and 0 or an empty value unsets all of them. A
// reviewer who stays keeps their verdict; one who goes loses it. It reports
// false after answering 400 for an id no user has.
func (s *Server) setReviewerIDs(w http.ResponseWriter, m *mr, p map[string]any) bool {
	var ids []int64
	switch v := p["reviewer_ids"].(type) {
	case []any:
		for _, x := range v {
			n, _ := strconv.ParseInt(fmt.Sprint(x), 10, 64)
			ids = append(ids, n)
		}
	case nil:
	default:
		n, _ := strconv.ParseInt(fmt.Sprint(v), 10, 64)
		ids = append(ids, n)
	}
	var set []*user
	for _, id := range ids {
		if id == 0 {
			continue
		}
		i := slices.IndexFunc(s.st.users, func(u *user) bool { return u.ID == id })
		if i < 0 {
			badRequest(w, fmt.Sprintf("reviewer_ids contains %d, which is not a user", id))
			return false
		}
		if u := s.st.users[i]; !slices.Contains(set, u) {
			set = append(set, u)
		}
	}
	for _, u := range m.Reviewers {
		if !slices.Contains(set, u) {
			delete(m.ReviewState, u.Username)
		}
	}
	m.Reviewers = set
	return true
}

// reviewerRereviewMutation answers mergeRequestReviewerRereview: the reviewer's
// verdict goes back to unreviewed. Like GitLab it refuses a user who is not a
// reviewer of the MR, with the error in the payload.
func (s *Server) reviewerRereviewMutation(w http.ResponseWriter, vars map[string]any) {
	reply := func(errs []string, mr any) {
		writeJSON(w, http.StatusOK, object{"data": object{"mergeRequestReviewerRereview": object{"errors": errs, "mergeRequest": mr}}})
	}
	p := s.st.findProject(fmt.Sprint(vars["projectPath"]))
	iid, _ := strconv.Atoi(fmt.Sprint(vars["iid"]))
	var m *mr
	if p != nil {
		m = s.st.findMR(p, iid)
	}
	if m == nil {
		reply([]string{"The resource that you are attempting to access does not exist or you don't have permission to perform this action"}, nil)
		return
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(fmt.Sprint(vars["userId"]), "gid://gitlab/User/"), 10, 64)
	i := slices.IndexFunc(m.Reviewers, func(u *user) bool { return u.ID == id })
	if i < 0 {
		reply([]string{"User is not a reviewer of this merge request"}, nil)
		return
	}
	delete(m.ReviewState, m.Reviewers[i].Username)
	m.Updated = s.st.clock
	reply([]string{}, object{"iid": strconv.Itoa(iid)})
}
