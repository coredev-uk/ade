package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

var (
	_ forge.Discoverer = (*Client)(nil)
	_ forge.FileWriter = (*Client)(nil)
)

// Repos implements forge.Discoverer: the repositories of an organisation or,
// when owner names no organisation, of a user. A user's own repositories come
// from /user/repos, the only listing that includes the private ones; another
// user's are the public ones.
func (c *Client) Repos(ctx context.Context, owner string) ([]forge.Repo, error) {
	if owner == "" {
		return nil, errors.New("github: Repos needs an organisation or user")
	}
	type wireRepo struct {
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Archived      bool   `json:"archived"`
	}
	raw, err := getAll[wireRepo](ctx, c, "/orgs/"+url.PathEscape(owner)+"/repos", url.Values{"type": {"all"}}, false)
	if errors.Is(err, forge.ErrNotFound) {
		path, q := "/users/"+url.PathEscape(owner)+"/repos", url.Values{"type": {"owner"}}
		if me, merr := c.Me(ctx); merr == nil && strings.EqualFold(me.Username, owner) {
			path, q = "/user/repos", url.Values{"affiliation": {"owner"}}
		}
		raw, err = getAll[wireRepo](ctx, c, path, q, false)
	}
	if err != nil {
		return nil, err
	}
	out := make([]forge.Repo, len(raw))
	for i, r := range raw {
		out[i] = forge.Repo{Path: r.FullName, Name: r.Name, DefaultBranch: r.DefaultBranch, Archived: r.Archived}
	}
	return out, nil
}

// Labels implements forge.Discoverer: the labels of one repository.
func (c *Client) Labels(ctx context.Context, project string) ([]string, error) {
	if owner, repo := splitProject(project); owner == "" || repo == "" {
		return nil, fmt.Errorf("github: invalid project %q", project)
	}
	raw, err := getAll[struct {
		Name string `json:"name"`
	}](ctx, c, repoPath(project)+"/labels", nil, false)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(raw))
	for i, l := range raw {
		out[i] = l.Name
	}
	return out, nil
}

// Cadences implements forge.Discoverer: GitHub has none.
func (c *Client) Cadences(_ context.Context, group string) ([]forge.Cadence, error) {
	return nil, fmt.Errorf("github: cadences of %s: %w", group, forge.ErrUnavailable)
}

// CommitFile implements forge.FileWriter through the contents API, which
// wants the blob sha of a file it replaces.
func (c *Client) CommitFile(ctx context.Context, project, branch, path string, content []byte, message string) error {
	if project == "" || branch == "" || path == "" || message == "" {
		return errors.New("github: CommitFile needs project, branch, path and message")
	}
	endpoint := contentsPath(project, path)
	body := map[string]string{
		"message": message, "branch": branch, "content": base64.StdEncoding.EncodeToString(content),
	}
	var existing struct {
		SHA string `json:"sha"`
	}
	var raw json.RawMessage
	err := c.getJSON(ctx, endpoint, url.Values{"ref": {branch}}, false, &raw)
	switch {
	case errors.Is(err, forge.ErrNotFound):
	case err != nil:
		return fmt.Errorf("github: looking for %s on %s@%s: %w", path, project, branch, err)
	default:
		if json.Unmarshal(raw, &existing) == nil {
			body["sha"] = existing.SHA
		}
	}
	if err := c.send(ctx, http.MethodPut, endpoint, body, nil); err != nil {
		return fmt.Errorf("github: committing %s to %s@%s: %w", path, project, branch, err)
	}
	return nil
}
