package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/coredev-uk/ade/internal/commitmsg"
	"github.com/coredev-uk/ade/internal/forge"
)

// maxFileBytes bounds ReadFile: templates are a few KB, and a mistaken path to
// a large blob must not fill memory.
const maxFileBytes = 1 << 20

var sha40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// webBase is the address of the web UI, for links to things created here.
func (c *Client) webBase() string {
	if c.rest == publicAPI {
		return publicWeb
	}
	return strings.TrimSuffix(c.rest, "/api/v3")
}

// DefaultBranch implements forge.IssueWriter.
func (c *Client) DefaultBranch(ctx context.Context, project string) (string, error) {
	if owner, repo := splitProject(project); owner == "" || repo == "" {
		return "", fmt.Errorf("github: invalid project %q", project)
	}
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.getJSON(ctx, repoPath(project), nil, false, &r); err != nil {
		return "", err
	}
	if r.DefaultBranch == "" {
		return "", fmt.Errorf("github: repository %s has no default branch (empty repository?)", project)
	}
	return r.DefaultBranch, nil
}

// CreateIssue implements forge.IssueWriter. Assignees are sent by login and
// the milestone by number, both resolved here. Weight has no field on GitHub
// and is ignored: callers gate it on Caps.Weight.
func (c *Client) CreateIssue(ctx context.Context, project string, d forge.IssueDraft) (forge.Issue, error) {
	if d.Title == "" {
		return forge.Issue{}, errors.New("github: CreateIssue needs a title")
	}
	body := map[string]any{"title": d.Title}
	if d.Description != "" {
		body["body"] = d.Description
	}
	if len(d.Labels) > 0 {
		body["labels"] = d.Labels
	}
	if len(d.AssigneeIDs) > 0 {
		logins := make([]string, len(d.AssigneeIDs))
		for i, id := range d.AssigneeIDs {
			login, err := c.accountLogin(ctx, id)
			if err != nil {
				return forge.Issue{}, err
			}
			logins[i] = login
		}
		body["assignees"] = logins
	}
	if d.MilestoneID != 0 {
		n, err := c.milestoneIn(ctx, project, d.MilestoneID)
		if err != nil {
			return forge.Issue{}, err
		}
		body["milestone"] = n
	}
	var w wireIssue
	if err := c.send(ctx, http.MethodPost, repoPath(project)+"/issues", body, &w); err != nil {
		return forge.Issue{}, err
	}
	is := w.issue(project)
	c.noteMilestone(is.Milestone)
	return is, nil
}

// milestoneIn finds, in project, the milestone a milestone id stands for.
// Milestone ids are per repository, so an id read from the tracker names the
// sprint by its title here: the project's milestone of that title is used and
// created (same dates, same state) when the repository has none yet. An id the
// client has never seen is taken as a milestone number of project.
func (c *Client) milestoneIn(ctx context.Context, project string, id int64) (int, error) {
	c.mu.Lock()
	m, known := c.milestones[id]
	c.mu.Unlock()
	if !known {
		return int(id), nil
	}
	n, ok, err := c.milestoneNumber(ctx, project, m.Title)
	if err != nil || ok {
		return n, err
	}
	in := map[string]any{"title": m.Title, "state": "open"}
	if m.State == "closed" {
		in["state"] = "closed"
	}
	if !m.Start.IsZero() {
		in["description"] = "Start: " + m.Start.String()
	}
	if !m.Due.IsZero() {
		in["due_on"] = m.Due.String() + "T12:00:00Z"
	}
	var created wireMilestone
	if err := c.send(ctx, http.MethodPost, repoPath(project)+"/milestones", in, &created); err != nil {
		return 0, err
	}
	c.noteMilestone(new(created.milestone()))
	return created.Number, nil
}

// SetLabels implements forge.IssueWriter. Labels are added first, then removed,
// so an interrupted change leaves both rather than neither. Adding a label
// that does not exist creates it; removing one the issue lacks is a no-op.
func (c *Client) SetLabels(ctx context.Context, ref forge.Ref, add, remove []string) error {
	if err := wantAny(ref); err != nil {
		return err
	}
	if len(add) > 0 {
		if err := c.send(ctx, http.MethodPost, issuePath(ref)+"/labels", map[string][]string{"labels": add}, nil); err != nil {
			return err
		}
	}
	for _, l := range remove {
		err := c.send(ctx, http.MethodDelete, issuePath(ref)+"/labels/"+url.PathEscape(l), nil, nil)
		if err != nil && !errors.Is(err, forge.ErrNotFound) {
			return err
		}
	}
	return nil
}

// wantAny accepts an issue or merge request ref: GitHub's issue endpoints
// serve both.
func wantAny(ref forge.Ref) error {
	if err := wantKind(ref, ref.Kind); err != nil {
		return err
	}
	return nil
}

// LinkIssues implements forge.IssueWriter: b becomes a sub-issue of a, GitHub's
// native parent-child link, across repositories. kind "relates_to" (what start
// work sends), "sub_issue" and "" mean that; GitHub's other relations are not
// offered. Linking an existing pair is a no-op.
func (c *Client) LinkIssues(ctx context.Context, a, b forge.Ref, kind string) error {
	if err := errors.Join(wantKind(a, forge.KindIssue), wantKind(b, forge.KindIssue)); err != nil {
		return err
	}
	switch kind {
	case "", "relates_to", "sub_issue":
	default:
		return fmt.Errorf("github: link type %q: %w", kind, forge.ErrUnavailable)
	}
	subs, err := getAll[wireIssue](ctx, c, issuePath(a)+"/sub_issues", nil, false)
	if err != nil {
		return err
	}
	for _, s := range subs {
		if s.issue(a.Project).Ref == b {
			return nil
		}
	}
	child, err := c.Issue(ctx, b)
	if err != nil {
		return err
	}
	return c.send(ctx, http.MethodPost, issuePath(a)+"/sub_issues", map[string]int64{"sub_issue_id": child.ID}, nil)
}

// SetIteration implements forge.IssueWriter: GitHub has no iterations.
func (c *Client) SetIteration(_ context.Context, ref forge.Ref, _ int64) error {
	return fmt.Errorf("github: iteration of %s: %w", ref, forge.ErrUnavailable)
}

// Branches implements forge.IssueWriter through the matching-refs listing,
// which matches by prefix.
func (c *Client) Branches(ctx context.Context, project, prefix string) ([]string, error) {
	raw, err := getAll[struct {
		Ref string `json:"ref"`
	}](ctx, c, repoPath(project)+"/git/matching-refs/heads/"+escapeRefPath(prefix), nil, false)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if name, ok := strings.CutPrefix(r.Ref, "refs/heads/"); ok && strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	return out, nil
}

// CreateBranch implements forge.IssueWriter. from is a branch, a tag or a full
// commit SHA; GitHub's refs API wants the SHA, so a name is resolved first.
func (c *Client) CreateBranch(ctx context.Context, project, name, from string) (forge.Branch, error) {
	if name == "" || from == "" {
		return forge.Branch{}, errors.New("github: CreateBranch needs a name and a source")
	}
	sha := strings.ToLower(from)
	if !sha40.MatchString(sha) {
		var commit struct {
			SHA string `json:"sha"`
		}
		if err := c.getJSON(ctx, repoPath(project)+"/commits/"+escapeRefPath(from), nil, false, &commit); err != nil {
			return forge.Branch{}, fmt.Errorf("resolving %q: %w", from, err)
		}
		sha = commit.SHA
	}
	in := map[string]string{"ref": "refs/heads/" + name, "sha": sha}
	var out struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.send(ctx, http.MethodPost, repoPath(project)+"/git/refs", in, &out); err != nil {
		return forge.Branch{}, existsError(err, "already exists")
	}
	return forge.Branch{
		Project: project, Name: name, SHA: out.Object.SHA,
		WebURL: c.webBase() + "/" + project + "/tree/" + escapeRefPath(name),
	}, nil
}

// existsError maps GitHub's 422 "already exists" answers to forge.ErrExists.
func existsError(err error, marker string) error {
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity &&
		strings.Contains(strings.ToLower(ae.Message), marker) {
		return fmt.Errorf("%w: %w", forge.ErrExists, err)
	}
	return err
}

// CreateMR implements forge.IssueWriter. Labels are added after creation,
// as pull request creation takes none.
//
// GitHub refuses a pull request whose head has no commit of its own ("No
// commits between base and head"), which is exactly what start work's freshly
// created branch is. The branch then gets one empty commit (the base's tree,
// titled after the MR) and the create is retried once, so the draft PR that
// start work promises exists.
func (c *Client) CreateMR(ctx context.Context, d forge.MRDraft) (forge.MR, error) {
	if d.Project == "" || d.Source == "" || d.Target == "" || d.Title == "" {
		return forge.MR{}, errors.New("github: CreateMR needs project, source, target and title")
	}
	body := map[string]any{"title": d.Title, "head": d.Source, "base": d.Target, "draft": d.Draft}
	if d.Description != "" {
		body["body"] = d.Description
	}
	var w wirePull
	path := repoPath(d.Project) + "/pulls"
	err := c.send(ctx, http.MethodPost, path, body, &w)
	if isStatus(err, http.StatusUnprocessableEntity) && strings.Contains(err.Error(), "No commits between") {
		if cerr := c.emptyCommit(ctx, d.Project, d.Source, commitmsg.Apply(d.CommitPrefix, "Start work: "+d.Title)); cerr != nil {
			return forge.MR{}, fmt.Errorf("github: %s has no commits yet and an empty start commit failed: %w", d.Source, cerr)
		}
		err = c.send(ctx, http.MethodPost, path, body, &w)
	}
	if err != nil {
		return forge.MR{}, existsError(err, "already exists")
	}
	mr := w.mr(d.Project)
	if len(d.Labels) > 0 {
		if err := c.SetLabels(ctx, forge.Ref{Project: d.Project, IID: w.Number, Kind: forge.KindIssue}, d.Labels, nil); err != nil {
			return mr, fmt.Errorf("labelling %s: %w", mr.Ref, err)
		}
	}
	return mr, nil
}

// emptyCommit advances branch by one commit that changes nothing.
func (c *Client) emptyCommit(ctx context.Context, project, branch, message string) error {
	repo := repoPath(project)
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.getJSON(ctx, repo+"/git/ref/heads/"+escapeRefPath(branch), nil, false, &ref); err != nil {
		return err
	}
	var parent struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.getJSON(ctx, repo+"/git/commits/"+ref.Object.SHA, nil, false, &parent); err != nil {
		return err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	in := map[string]any{"message": message, "tree": parent.Tree.SHA, "parents": []string{ref.Object.SHA}}
	if err := c.send(ctx, http.MethodPost, repo+"/git/commits", in, &commit); err != nil {
		return err
	}
	return c.send(ctx, http.MethodPatch, repo+"/git/refs/heads/"+escapeRefPath(branch), map[string]any{"sha": commit.SHA}, nil)
}

// contentsPath is the escaped contents API path of a file or directory.
func contentsPath(project, path string) string {
	p := repoPath(project) + "/contents"
	if path = strings.Trim(path, "/"); path != "" {
		p += "/" + escapeRefPath(path)
	}
	return p
}

// ReadFile implements forge.IssueWriter (the contents API, raw media type).
func (c *Client) ReadFile(ctx context.Context, project, ref, path string) ([]byte, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	resp, err := c.do(ctx, request{
		method: http.MethodGet, path: contentsPath(project, path), query: q, accept: "application/vnd.github.raw+json",
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("github: reading %s: %w", path, err)
	}
	if len(b) > maxFileBytes {
		return nil, fmt.Errorf("github: %s is larger than %d bytes", path, maxFileBytes)
	}
	return b, nil
}

// ListFiles implements forge.IssueWriter: the files directly inside dir. A
// missing directory, or a path that is a file, is empty.
func (c *Client) ListFiles(ctx context.Context, project, ref, dir string) ([]string, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	var raw json.RawMessage
	err := c.getJSON(ctx, contentsPath(project, dir), q, false, &raw)
	if errors.Is(err, forge.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &entries) != nil {
		return nil, nil // a file answers with an object
	}
	var out []string
	for _, e := range entries {
		if e.Type == "file" {
			out = append(out, e.Path)
		}
	}
	return out, nil
}
