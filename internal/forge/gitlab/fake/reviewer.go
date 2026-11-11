package fake

import (
	"fmt"

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
