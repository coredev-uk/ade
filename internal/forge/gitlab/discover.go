package gitlab

import (
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/coredev-uk/ade/internal/forge"
)

var (
	_ forge.Discoverer = (*Client)(nil)
	_ forge.FileWriter = (*Client)(nil)
)

// cadencesQuery reads a group's iteration cadences; it is validated against
// schema.graphql with the other documents in queries/.
//
//go:embed queries/cadences.graphql
var cadencesQuery string

// Repos implements forge.Discoverer: the projects of a group and its
// subgroups. Projects shared into the group from elsewhere are not its repos.
// A path that is no group is tried as a user namespace.
func (c *Client) Repos(ctx context.Context, group string) ([]forge.Repo, error) {
	if group == "" {
		return nil, errors.New("gitlab: Repos needs a group")
	}
	type wireProject struct {
		Path          string `json:"path"`
		FullPath      string `json:"path_with_namespace"`
		DefaultBranch string `json:"default_branch"`
		Archived      bool   `json:"archived"`
	}
	q := url.Values{"include_subgroups": {"true"}, "with_shared": {"false"}}
	raw, err := getAll[wireProject](ctx, c, groupPath(group)+"/projects", q, false)
	if errors.Is(err, forge.ErrNotFound) {
		raw, err = getAll[wireProject](ctx, c, "/users/"+url.PathEscape(group)+"/projects", nil, false)
	}
	if err != nil {
		return nil, err
	}
	out := make([]forge.Repo, len(raw))
	for i, p := range raw {
		out[i] = forge.Repo{Path: p.FullPath, Name: p.Path, DefaultBranch: p.DefaultBranch, Archived: p.Archived}
	}
	return out, nil
}

// Labels implements forge.Discoverer: the project's labels and those of its
// ancestor groups, which is where scoped labels usually live.
func (c *Client) Labels(ctx context.Context, project string) ([]string, error) {
	q := url.Values{"include_ancestor_groups": {"true"}}
	raw, err := getAll[struct {
		Name string `json:"name"`
	}](ctx, c, projectPath(project)+"/labels", q, false)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(raw))
	for i, l := range raw {
		out[i] = l.Name
	}
	return out, nil
}

// Cadences implements forge.Discoverer through GraphQL, which is the only
// place a cadence's title, length and start date are visible. A null
// iterationCadences field (the gated one) is forge.ErrUnavailable.
func (c *Client) Cadences(ctx context.Context, group string) ([]forge.Cadence, error) {
	if group == "" {
		return nil, errors.New("gitlab: Cadences needs a group")
	}
	var out struct {
		Group *struct {
			IterationCadences *struct {
				Nodes []struct {
					Title           string     `json:"title"`
					DurationInWeeks int        `json:"durationInWeeks"`
					StartDate       forge.Date `json:"startDate"`
					Automatic       bool       `json:"automatic"`
				} `json:"nodes"`
			} `json:"iterationCadences"`
		} `json:"group"`
	}
	if err := c.graphql(ctx, cadencesQuery, map[string]any{"path": group}, &out); err != nil {
		return nil, err
	}
	if out.Group == nil {
		return nil, fmt.Errorf("gitlab: group %s: %w", group, forge.ErrNotFound)
	}
	if out.Group.IterationCadences == nil {
		return nil, fmt.Errorf("gitlab: cadences of %s: %w", group, forge.ErrUnavailable)
	}
	res := make([]forge.Cadence, len(out.Group.IterationCadences.Nodes))
	for i, n := range out.Group.IterationCadences.Nodes {
		res[i] = forge.Cadence{Title: n.Title, Weeks: n.DurationInWeeks, Start: n.StartDate, Automatic: n.Automatic}
	}
	return res, nil
}

// CommitFile implements forge.FileWriter: the repository files API creates
// the file, or updates it when GitLab says it exists.
func (c *Client) CommitFile(ctx context.Context, project, branch, path string, content []byte, message string) error {
	if project == "" || branch == "" || path == "" || message == "" {
		return errors.New("gitlab: CommitFile needs project, branch, path and message")
	}
	body := map[string]string{
		"branch": branch, "commit_message": message,
		"content": base64.StdEncoding.EncodeToString(content), "encoding": "base64",
	}
	endpoint := projectPath(project) + "/repository/files/" + url.PathEscape(path)
	err := c.send(ctx, http.MethodPost, endpoint, body, nil)
	if err != nil {
		err = existsError(err, "already exists")
	}
	if errors.Is(err, forge.ErrExists) {
		err = c.send(ctx, http.MethodPut, endpoint, body, nil)
	}
	if err != nil {
		return fmt.Errorf("gitlab: committing %s to %s@%s: %w", path, project, branch, err)
	}
	return nil
}
