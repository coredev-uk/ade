package fake

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// The accessors let tests assert what a driver did to the fake without going
// back through the API they are testing.

// item resolves a ref to its issue or PR. A KindMR ref must name a PR; a
// KindIssue ref names any item, as every PR is also an issue on GitHub.
func (s *Server) item(ref forge.Ref) (*repo, *item, error) {
	owner, name, ok := splitProject(ref.Project)
	rp := s.st.repo(owner, name)
	if !ok || rp == nil {
		return nil, nil, fmt.Errorf("no repo %q", ref.Project)
	}
	it := rp.item(ref.IID)
	if it == nil || (ref.Kind == forge.KindMR && it.PR == nil) {
		return nil, nil, fmt.Errorf("no such item %s", ref)
	}
	return rp, it, nil
}

func splitProject(p string) (owner, name string, ok bool) {
	return strings.Cut(p, "/")
}

func (s *Server) pull(ref forge.Ref) (*pull, error) {
	_, it, err := s.item(ref)
	if err != nil {
		return nil, err
	}
	if it.PR == nil {
		return nil, fmt.Errorf("%s is not a pull request", ref)
	}
	return it.PR, nil
}

// Publishes is how many reviews were submitted on the PR, whether through
// the submit-pending endpoint or by creating a review with an event.
func (s *Server) Publishes(ref forge.Ref) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.pull(ref)
	if err != nil {
		return 0, err
	}
	return p.Publishes, nil
}

// PendingReviewComments is how many comments the viewer's unsubmitted
// review of the PR holds.
func (s *Server) PendingReviewComments(ref forge.Ref) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.pull(ref)
	if err != nil {
		return 0, err
	}
	rv := p.pendingOf(viewer)
	if rv == nil {
		return 0, nil
	}
	return p.commentCount(rv), nil
}

// Approvers lists the logins whose latest verdict on the PR is an approval.
func (s *Server) Approvers(ref forge.Ref) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.pull(ref)
	if err != nil {
		return nil, err
	}
	return p.approvers(), nil
}

// MergeState reports whether the PR is merged or set to auto-merge, and the
// parameters of the last merge or auto-merge request made to it (merge_method
// in lower case, plus sha, commit_title and commit_message when given).
func (s *Server) MergeState(ref forge.Ref) (merged, auto bool, params map[string]any, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.pull(ref)
	if err != nil {
		return false, false, nil, err
	}
	return p.Merged, p.AutoMerge != nil, maps.Clone(p.MergeParams), nil
}

// Reruns is how many times rerun-failed-jobs was accepted for a workflow run.
func (s *Server) Reruns(project string, runID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, name, _ := splitProject(project)
	if rp := s.st.repo(owner, name); rp != nil {
		if ru := rp.run(runID); ru != nil {
			return ru.Reruns
		}
	}
	return 0
}

// IsDraft reports whether the PR is a draft.
func (s *Server) IsDraft(ref forge.Ref) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.pull(ref)
	if err != nil {
		return false, err
	}
	return p.Draft, nil
}

// Labels lists the item's label names in the order they were applied.
func (s *Server) Labels(ref forge.Ref) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, it, err := s.item(ref)
	if err != nil {
		return nil, err
	}
	return slices.Clone(it.Labels), nil
}

// SubIssues lists the issue's sub-issues in the order they were attached.
func (s *Server) SubIssues(ref forge.Ref) ([]forge.Ref, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, it, err := s.item(ref)
	if err != nil {
		return nil, err
	}
	out := make([]forge.Ref, len(it.Subs))
	for i, k := range it.Subs {
		out[i] = forge.Ref{Project: rp.Owner + "/" + k.Repo, IID: k.Number, Kind: forge.KindIssue}
	}
	return out, nil
}

// Branches lists the repo's branch names, sorted; nil for an unknown repo.
func (s *Server) Branches(project string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, name, _ := splitProject(project)
	rp := s.st.repo(owner, name)
	if rp == nil {
		return nil
	}
	out := make([]string, 0, len(rp.Branches))
	for b := range rp.Branches {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// Milestone is a repo milestone as the fake holds it.
type Milestone struct {
	Number      int
	Title       string
	Description string
	State       string
	DueOn       time.Time
}

// Milestones lists the repo's milestones by number; nil for an unknown repo.
func (s *Server) Milestones(project string) []Milestone {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, name, _ := splitProject(project)
	rp := s.st.repo(owner, name)
	if rp == nil {
		return nil
	}
	out := make([]Milestone, len(rp.Milestones))
	for i, m := range rp.Milestones {
		out[i] = Milestone{Number: m.Number, Title: m.Title, Description: m.Description, State: m.State, DueOn: m.DueOn}
	}
	return out
}
