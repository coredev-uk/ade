package fake

import (
	"maps"

	"github.com/coredev-uk/ade/internal/forge"
)

// The accessors let tests assert what a driver did to the fake without going
// back through the API they are testing.

// Publishes is how many bulk_publish calls published drafts on the MR.
func (s *Server) Publishes(ref forge.Ref) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return 0, err
	}
	return m.Publishes, nil
}

// PendingDrafts is how many unpublished draft notes the MR holds.
func (s *Server) PendingDrafts(ref forge.Ref) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return 0, err
	}
	return len(m.Drafts), nil
}

// Approvers lists the usernames that approved the MR.
func (s *Server) Approvers(ref forge.Ref) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(m.Approved))
	for i, u := range m.Approved {
		out[i] = u.Username
	}
	return out, nil
}

// MergeState reports whether the MR is merged or set to auto-merge, and the
// parameters of the last merge request made to it.
func (s *Server) MergeState(ref forge.Ref) (merged, auto bool, params map[string]any, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return false, false, nil, err
	}
	return m.State == "merged", m.AutoMerge, maps.Clone(m.MergeParams), nil
}

// Retries is how many pipeline retries were requested on the MR's pipeline.
func (s *Server) Retries(ref forge.Ref) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return 0, err
	}
	return m.Retries, nil
}

// Title is the MR's current title.
func (s *Server) Title(ref forge.Ref) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return "", err
	}
	return m.Title, nil
}

// Description is the MR's current description and how many times a client
// rewrote it.
func (s *Server) Description(ref forge.Ref) (desc string, writes int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return "", 0, err
	}
	return m.Desc, m.DescWrites, nil
}
