package fake

import (
	"fmt"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// The mutators change the served state between requests so tests can drive a
// scenario (CI fails, a reviewer comments, the MR merges). Each stamps the
// object's updated time with the fake clock. Later phases add write endpoints
// that call the same state helpers.

func (s *Server) issue(ref forge.Ref) (*issue, error) {
	if p := s.st.findProject(ref.Project); p != nil && ref.Kind == forge.KindIssue {
		if is := s.st.findIssue(p, ref.IID); is != nil {
			return is, nil
		}
	}
	return nil, fmt.Errorf("fake: no issue %s", ref)
}

func (s *Server) mr(ref forge.Ref) (*mr, error) {
	if p := s.st.findProject(ref.Project); p != nil && ref.Kind == forge.KindMR {
		if m := s.st.findMR(p, ref.IID); m != nil {
			return m, nil
		}
	}
	return nil, fmt.Errorf("fake: no merge request %s", ref)
}

// AddNote posts body as author on the issue or MR. An empty discussionID
// starts a new resolvable thread (a plain note on an issue); otherwise the note
// replies in that thread. It returns the discussion ID.
func (s *Server) AddNote(ref forge.Ref, discussionID, author, body string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.st.findUser(author)
	if u == nil {
		return "", fmt.Errorf("fake: unknown user %q", author)
	}
	if err := s.touch(ref); err != nil {
		return "", err
	}
	n := &note{ID: s.st.id(), Author: u, Body: body, Created: s.st.clock, Resolvable: ref.Kind == forge.KindMR}
	if discussionID == "" {
		d := &discussion{ID: fmt.Sprintf("%040x", s.st.id()), Notes: []*note{n}}
		s.st.threads[ref] = append(s.st.threads[ref], d)
		return d.ID, nil
	}
	d := s.thread(ref, discussionID)
	if d == nil {
		return "", fmt.Errorf("fake: no discussion %s on %s", discussionID, ref)
	}
	d.Notes = append(d.Notes, n)
	return d.ID, nil
}

// ResolveThread marks every resolvable note of a discussion (un)resolved.
func (s *Server) ResolveThread(ref forge.Ref, discussionID string, resolved bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.thread(ref, discussionID)
	if d == nil {
		return fmt.Errorf("fake: no discussion %s on %s", discussionID, ref)
	}
	for _, n := range d.Notes {
		if n.Resolvable {
			n.Resolved = resolved
		}
	}
	return s.touch(ref)
}

func (s *Server) thread(ref forge.Ref, id string) *discussion {
	for _, d := range s.st.threads[ref] {
		if d.ID == id {
			return d
		}
	}
	return nil
}

// touch bumps updated_at of the referenced object to the clock.
func (s *Server) touch(ref forge.Ref) error {
	if ref.Kind == forge.KindMR {
		m, err := s.mr(ref)
		if err == nil {
			m.Updated = s.st.clock
		}
		return err
	}
	is, err := s.issue(ref)
	if err == nil {
		is.Updated = s.st.clock
	}
	return err
}

// SetPipelineStatus sets the status of an MR's head pipeline, creating one if
// the MR had none.
func (s *Server) SetPipelineStatus(ref forge.Ref, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return err
	}
	if m.Pipeline == nil {
		m.Pipeline = &pipeline{ID: s.st.id()}
	}
	m.Pipeline.Status, m.Pipeline.Updated, m.Updated = status, s.st.clock, s.st.clock
	return nil
}

// CloseIssue closes an issue at the clock's time.
func (s *Server) CloseIssue(ref forge.Ref) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, err := s.issue(ref)
	if err != nil {
		return err
	}
	now := s.st.clock
	is.State, is.Closed, is.Updated = "closed", &now, now
	return nil
}

// MergeMR merges an open MR at the clock's time.
func (s *Server) MergeMR(ref forge.Ref) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return err
	}
	if m.State != "opened" {
		return fmt.Errorf("fake: %s is %s, not opened", ref, m.State)
	}
	now := s.st.clock
	m.State, m.Draft, m.Merged, m.Updated = "merged", false, &now, now
	return nil
}

// MergeMRAs merges an open MR as username, who then shows as its merger.
func (s *Server) MergeMRAs(ref forge.Ref, username string) error {
	if err := s.MergeMR(ref); err != nil {
		return err
	}
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
	m.MergedBy = u
	return nil
}

// Advance moves the fake clock forward; later mutations are stamped with it.
func (s *Server) Advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.clock = s.st.clock.Add(d)
}
