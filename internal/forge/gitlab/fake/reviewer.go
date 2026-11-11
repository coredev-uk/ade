package fake

import (
	"fmt"
	"net/http"
	"slices"

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
