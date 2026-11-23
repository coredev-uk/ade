package fake

import (
	"crypto/sha1" //nolint:gosec // a fake commit id, not a security hash
	"encoding/hex"
	"net/http"

	"github.com/coredev-uk/ade/internal/forge"
)

// rebaseState is how an MR relates to its target branch and what a rebase of
// it does. The zero value is an MR that is up to date.
type rebaseState struct {
	Behind    bool // the target moved: detailed_merge_status need_rebase
	Conflicts bool // has_conflicts: a rebase fails with merge_error
	// Delay is how many polls (GET with include_rebase_in_progress) still see
	// rebase_in_progress after PUT .../rebase, as the real background job does.
	Delay int

	pending bool
	polls   int
	err     string // merge_error of the last finished attempt

	Calls  int  // PUT .../rebase requests accepted
	SkipCI bool // skip_ci of the last one
}

// mergeError is what GitLab answers when it cannot rebase.
const mergeError = "Rebase failed. Please rebase locally"

// SetBehind marks the MR as behind its target (need_rebase), or up to date.
func (s *Server) SetBehind(ref forge.Ref, behind bool) error {
	return s.rebase(ref, func(r *rebaseState) { r.Behind = behind })
}

// SetConflicts marks the MR as conflicting with its target: has_conflicts is
// true and a rebase fails with merge_error.
func (s *Server) SetConflicts(ref forge.Ref, conflicts bool) error {
	return s.rebase(ref, func(r *rebaseState) { r.Conflicts = conflicts })
}

// SetRebaseDelay makes a rebase report rebase_in_progress for the next n polls.
func (s *Server) SetRebaseDelay(ref forge.Ref, n int) error {
	return s.rebase(ref, func(r *rebaseState) { r.Delay = n })
}

// Rebases is how many rebases the MR accepted and whether the last skipped CI.
func (s *Server) Rebases(ref forge.Ref) (calls int, skipCI bool, err error) {
	err = s.rebase(ref, func(r *rebaseState) { calls, skipCI = r.Calls, r.SkipCI })
	return calls, skipCI, err
}

func (s *Server) rebase(ref forge.Ref, fn func(*rebaseState)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return err
	}
	fn(&m.Rebase)
	m.Updated = s.st.clock
	return nil
}

// rebaseMR handles PUT .../rebase: 202 and a background job that the next
// polls see through. Only an open MR can be rebased (403 otherwise, as for a
// user who cannot push); a second request while one runs is a 409.
func (s *Server) rebaseMR(w http.ResponseWriter, r *http.Request) {
	p := params(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.lookupMR(w, r)
	if !ok {
		return
	}
	switch {
	case m.State != "opened":
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "403 Forbidden"})
		return
	case m.Rebase.pending:
		writeJSON(w, http.StatusConflict, map[string]string{"message": "Failed to enqueue the rebase operation, possibly due to a long-lived transaction. Try again later."})
		return
	}
	m.Rebase.Calls++
	m.Rebase.SkipCI = flag(p, "skip_ci")
	m.Rebase.pending, m.Rebase.polls, m.Rebase.err = true, 0, ""
	writeJSON(w, http.StatusAccepted, object{"rebase_in_progress": true})
}

// pollRebase settles a pending rebase once enough polls have seen it running:
// success moves the head and clears need_rebase, a conflict leaves the branch
// alone and sets merge_error.
func (s *Server) pollRebase(m *mr) {
	rb := &m.Rebase
	if !rb.pending {
		return
	}
	if rb.polls < rb.Delay {
		rb.polls++
		return
	}
	rb.pending = false
	if rb.Conflicts {
		rb.err = mergeError
		return
	}
	rb.Behind = false
	sum := sha1.Sum([]byte("rebase " + m.SHA + m.Target)) //nolint:gosec // see import
	m.SHA = hex.EncodeToString(sum[:])
	if m.Diff != nil {
		m.Diff.Head = m.SHA
	}
	m.Updated = s.st.clock
}

// rebaseJSON adds the keys GET .../merge_requests/:iid?include_rebase_in_progress=true
// returns.
func (s *Server) rebaseJSON(m *mr, o object) {
	s.pollRebase(m)
	o["sha"], o["updated_at"] = m.SHA, ts(m.Updated)
	o["rebase_in_progress"] = m.Rebase.pending
	if m.Rebase.err != "" {
		o["merge_error"] = m.Rebase.err
	} else {
		o["merge_error"] = nil
	}
}
