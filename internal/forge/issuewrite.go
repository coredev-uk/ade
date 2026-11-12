package forge

import (
	"context"
	"errors"
)

// ErrExists marks a create that found the thing already there (a branch with
// that name, an open MR for the same source branch).
var ErrExists = errors.New("already exists")

// IssueDraft is a new issue. Milestone and Weight are optional: the zero
// value leaves them unset, and a forge that does not license weight ignores
// it, so callers gate it on Caps.Weight.
type IssueDraft struct {
	Title       string
	Description string
	Labels      []string
	AssigneeIDs []int64
	MilestoneID int64
	Weight      *int
}

// MRDraft is a new merge request. A Draft MR is created in the forge's draft
// state. Quick actions in Description (`/label ~x`, `/assign_reviewer @a`) are
// executed by the forge on create; callers that do not want them strip them
// first.
type MRDraft struct {
	Project     string
	Source      string
	Target      string
	Title       string // without any "Draft:" prefix
	Description string
	Draft       bool
	Labels      []string
	// CommitPrefix is the prefix for any commit the forge must create to open
	// the MR (GitHub's empty start commit); empty means none.
	CommitPrefix string
}

// Branch is a created branch.
type Branch struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	SHA     string `json:"sha,omitempty"`
	WebURL  string `json:"web_url,omitempty"`
}

// IssueWriter is the Phase 3 write side of a forge: what Start work needs to
// create a child issue, link it, label it, branch and open the MR, plus the
// reads that make those writes idempotent. Drivers implement it next to Forge.
type IssueWriter interface {
	// DefaultBranch is the project's default branch.
	DefaultBranch(ctx context.Context, project string) (string, error)
	CreateIssue(ctx context.Context, project string, d IssueDraft) (Issue, error)
	// SetLabels adds and removes labels in one update. Removal is explicit
	// because "::" labels are not exclusive on every tier.
	SetLabels(ctx context.Context, ref Ref, add, remove []string) error
	// LinkIssues links a to b ("relates_to"); linking an existing pair is a
	// no-op.
	LinkIssues(ctx context.Context, a, b Ref, kind string) error
	// SetIteration moves the issue into an iteration. ErrUnavailable where
	// iterations are not licensed.
	SetIteration(ctx context.Context, ref Ref, iterationID int64) error
	// Branches lists branch names starting with prefix (all when empty).
	Branches(ctx context.Context, project, prefix string) ([]string, error)
	// CreateBranch creates name from the branch, tag or SHA from. ErrExists
	// when the branch is there already.
	CreateBranch(ctx context.Context, project, name, from string) (Branch, error)
	// CreateMR opens a merge request. ErrExists when an open MR already
	// covers the source branch.
	CreateMR(ctx context.Context, d MRDraft) (MR, error)
	// ReadFile returns a file at ref; ErrNotFound when it is missing.
	ReadFile(ctx context.Context, project, ref, path string) ([]byte, error)
	// ListFiles lists the file paths directly inside dir at ref; a missing
	// directory is empty, not an error.
	ListFiles(ctx context.Context, project, ref, dir string) ([]string, error)
}

// WeightWriter sets an issue's weight field, on forges that license one
// (Caps.Weight): GitLab Premium. A nil weight clears it. Forges without the
// field carry weights on labels, which IssueWriter.SetLabels already writes.
type WeightWriter interface {
	SetWeight(ctx context.Context, ref Ref, weight *int) error
}

// MilestoneWriter sets an issue's milestone, the sprint on forges whose
// sprints are milestones (GitLab Free, GitHub). milestoneID 0 clears it.
// The id is the one Milestone.ID reports; a forge whose ids are per
// repository resolves it by title for the issue's project.
type MilestoneWriter interface {
	SetMilestone(ctx context.Context, ref Ref, milestoneID int64) error
}
