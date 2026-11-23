package fake

import (
	"fmt"
	"slices"

	"github.com/coredev-uk/ade/internal/forge"
)

// SubmitReviewAs records a submitted review by login, a teammate rather than
// the viewer. state is APPROVED, CHANGES_REQUESTED or COMMENTED. As on GitHub,
// submitting a review answers the reviewer's pending request: they are no
// longer in requested_reviewers until asked again.
func (s *Server) SubmitReviewAs(ref forge.Ref, login, state, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, it, err := s.item(ref)
	if err != nil {
		return err
	}
	if it.PR == nil {
		return fmt.Errorf("%s is not a pull request", ref)
	}
	switch state {
	case stateApproved, stateChanges, stateCommented:
	default:
		return fmt.Errorf("fake github: review state %q", state)
	}
	at := s.st.now()
	s.st.addReview(it.PR, login, state, body, at)
	it.PR.Reviewers = slices.DeleteFunc(it.PR.Reviewers, func(l string) bool { return l == login })
	it.Updated = at
	return nil
}

// RequestedReviewers are the logins with a pending review request on the PR.
func (s *Server) RequestedReviewers(ref forge.Ref) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.pull(ref)
	if err != nil {
		return nil, err
	}
	return slices.Clone(p.Reviewers), nil
}
