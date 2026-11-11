package forge

import "context"

// Repo is one repository of a group (GitLab) or an owner (GitHub), as
// `ade project bootstrap` sees it.
type Repo struct {
	Path          string `json:"path"` // group/name
	Name          string `json:"name"` // last path segment
	DefaultBranch string `json:"default_branch,omitempty"`
	Archived      bool   `json:"archived,omitempty"`
}

// Cadence is a GitLab iteration cadence. Weeks is the length of one
// iteration; Start is the day the first iteration begins.
type Cadence struct {
	Title     string `json:"title"`
	Weeks     int    `json:"weeks,omitempty"`
	Start     Date   `json:"start,omitzero"`
	Automatic bool   `json:"automatic,omitempty"`
}

// Discoverer is what bootstrapping a manifest needs to read of a forge beyond
// Forge and IssueWriter: which repositories a group holds, which labels exist
// and which iteration cadences are configured. Drivers implement it next to
// Forge.
type Discoverer interface {
	// Repos lists the repositories of a GitLab group (subgroups included) or
	// of a GitHub organisation or user, archived ones included and flagged.
	Repos(ctx context.Context, group string) ([]Repo, error)
	// Labels lists the names of the labels a project offers: its own and,
	// on GitLab, those of its groups.
	Labels(ctx context.Context, project string) ([]string, error)
	// Cadences lists a group's iteration cadences; ErrUnavailable where the
	// forge has none to offer.
	Cadences(ctx context.Context, group string) ([]Cadence, error)
}

// FileWriter commits a file to a branch, the write `ade project bootstrap
// --publish` needs on top of IssueWriter's branch and MR creation.
type FileWriter interface {
	// CommitFile creates path on branch, or replaces it when it exists, as
	// one commit. The branch must exist.
	CommitFile(ctx context.Context, project, branch, path string, content []byte, message string) error
}
