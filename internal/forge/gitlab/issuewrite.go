package gitlab

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

var (
	_ forge.IssueWriter         = (*Client)(nil)
	_ forge.ReviewerGroupReader = (*Client)(nil)
)

// setIterationMutation is validated against schema.graphql with the other
// documents in queries/. REST has no iteration write.
//
//go:embed queries/set_iteration.graphql
var setIterationMutation string

// maxFileBytes bounds ReadFile: templates are a few KB, and a mistaken path to
// a large blob must not fill memory.
const maxFileBytes = 1 << 20

// DefaultBranch implements forge.IssueWriter.
func (c *Client) DefaultBranch(ctx context.Context, project string) (string, error) {
	var p struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.getJSON(ctx, projectPath(project), nil, false, &p); err != nil {
		return "", err
	}
	if p.DefaultBranch == "" {
		return "", fmt.Errorf("gitlab: project %s has no default branch (empty repository?)", project)
	}
	return p.DefaultBranch, nil
}

// CreateIssue implements forge.IssueWriter.
func (c *Client) CreateIssue(ctx context.Context, project string, d forge.IssueDraft) (forge.Issue, error) {
	if d.Title == "" {
		return forge.Issue{}, errors.New("gitlab: CreateIssue needs a title")
	}
	body := map[string]any{"title": d.Title}
	if d.Description != "" {
		body["description"] = d.Description
	}
	if len(d.Labels) > 0 {
		body["labels"] = strings.Join(d.Labels, ",")
	}
	if len(d.AssigneeIDs) > 0 {
		body["assignee_ids"] = d.AssigneeIDs
	}
	if d.MilestoneID != 0 {
		body["milestone_id"] = d.MilestoneID
	}
	if d.Weight != nil {
		body["weight"] = *d.Weight
	}
	var w wireIssue
	if err := c.send(ctx, http.MethodPost, projectPath(project)+"/issues", body, &w); err != nil {
		return forge.Issue{}, err
	}
	return w.issue(project, c.now()), nil
}

// SetLabels implements forge.IssueWriter with one update, so the add and the
// removals land together.
func (c *Client) SetLabels(ctx context.Context, ref forge.Ref, add, remove []string) error {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return err
	}
	body := map[string]string{}
	if len(add) > 0 {
		body["add_labels"] = strings.Join(add, ",")
	}
	if len(remove) > 0 {
		body["remove_labels"] = strings.Join(remove, ",")
	}
	if len(body) == 0 {
		return nil
	}
	return c.send(ctx, http.MethodPut, issuePath(ref), body, nil)
}

var _ forge.AssigneeWriter = (*Client)(nil)

// SetAssignees implements forge.AssigneeWriter: the issue update's
// assignee_ids replaces the list, and an empty list clears it.
func (c *Client) SetAssignees(ctx context.Context, ref forge.Ref, assignees []forge.User) error {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return err
	}
	ids := make([]int64, len(assignees))
	for i, u := range assignees {
		ids[i] = u.ID
	}
	return c.send(ctx, http.MethodPut, issuePath(ref), map[string][]int64{"assignee_ids": ids}, nil)
}

var _ forge.MilestoneWriter = (*Client)(nil)

// SetMilestone implements forge.MilestoneWriter. GitLab's issue update takes
// milestone_id, and 0 removes the milestone.
func (c *Client) SetMilestone(ctx context.Context, ref forge.Ref, milestoneID int64) error {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return err
	}
	if milestoneID < 0 {
		return fmt.Errorf("gitlab: milestone id %d on %s: want a milestone id, or 0 to clear", milestoneID, ref)
	}
	return c.send(ctx, http.MethodPut, issuePath(ref), map[string]int64{"milestone_id": milestoneID}, nil)
}

// LinkIssues implements forge.IssueWriter. GitLab answers 409 for a pair that
// is already linked, which is the state the caller asked for.
func (c *Client) LinkIssues(ctx context.Context, a, b forge.Ref, kind string) error {
	if err := errors.Join(wantKind(a, forge.KindIssue), wantKind(b, forge.KindIssue)); err != nil {
		return err
	}
	if kind == "" {
		kind = "relates_to"
	}
	body := map[string]any{"target_project_id": b.Project, "target_issue_iid": b.IID, "link_type": kind}
	err := c.send(ctx, http.MethodPost, issuePath(a)+"/links", body, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusConflict {
		return nil
	}
	return err
}

// SetIteration implements forge.IssueWriter through GraphQL issueSetIteration.
// An instance without iterations answers with a null mutation payload next to
// an error (the field is gated), which is forge.ErrUnavailable.
func (c *Client) SetIteration(ctx context.Context, ref forge.Ref, iterationID int64) error {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return err
	}
	vars := map[string]any{
		"projectPath": ref.Project,
		"iid":         strconv.Itoa(ref.IID),
		"iterationId": "gid://gitlab/Iteration/" + strconv.FormatInt(iterationID, 10),
	}
	var out struct {
		IssueSetIteration *struct {
			Errors []string `json:"errors"`
		} `json:"issueSetIteration"`
	}
	if err := c.graphql(ctx, setIterationMutation, vars, &out); err != nil {
		return err
	}
	if out.IssueSetIteration == nil {
		return fmt.Errorf("gitlab: issueSetIteration on %s: %w", ref, forge.ErrUnavailable)
	}
	if errs := out.IssueSetIteration.Errors; len(errs) > 0 {
		return fmt.Errorf("gitlab: issueSetIteration on %s: %s", ref, strings.Join(errs, "; "))
	}
	return nil
}

// Branches implements forge.IssueWriter. GitLab's search takes ^ for a prefix.
func (c *Client) Branches(ctx context.Context, project, prefix string) ([]string, error) {
	q := url.Values{}
	if prefix != "" {
		q.Set("search", "^"+prefix)
	}
	raw, err := getAll[struct {
		Name string `json:"name"`
	}](ctx, c, projectPath(project)+"/repository/branches", q, false)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raw))
	for _, b := range raw {
		// The search is a substring match on older versions: enforce the prefix.
		if strings.HasPrefix(b.Name, prefix) {
			out = append(out, b.Name)
		}
	}
	return out, nil
}

// CreateBranch implements forge.IssueWriter.
func (c *Client) CreateBranch(ctx context.Context, project, name, from string) (forge.Branch, error) {
	var w struct {
		Name   string `json:"name"`
		WebURL string `json:"web_url"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	err := c.send(ctx, http.MethodPost, projectPath(project)+"/repository/branches", map[string]string{"branch": name, "ref": from}, &w)
	if err != nil {
		return forge.Branch{}, existsError(err, "already exists")
	}
	return forge.Branch{Project: project, Name: w.Name, SHA: w.Commit.ID, WebURL: w.WebURL}, nil
}

// CreateMR implements forge.IssueWriter. REST has no draft parameter: GitLab
// derives draft from the title prefix.
func (c *Client) CreateMR(ctx context.Context, d forge.MRDraft) (forge.MR, error) {
	if d.Project == "" || d.Source == "" || d.Target == "" || d.Title == "" {
		return forge.MR{}, errors.New("gitlab: CreateMR needs project, source, target and title")
	}
	title := readyTitle(d.Title)
	if d.Draft {
		title = "Draft: " + title
	}
	body := map[string]any{
		"source_branch": d.Source, "target_branch": d.Target, "title": title,
	}
	if d.Description != "" {
		body["description"] = d.Description
	}
	if len(d.Labels) > 0 {
		body["labels"] = strings.Join(d.Labels, ",")
	}
	if len(d.ReviewerIDs) > 0 {
		body["reviewer_ids"] = d.ReviewerIDs
	}
	var w wireMR
	if err := c.send(ctx, http.MethodPost, projectPath(d.Project)+"/merge_requests", body, &w); err != nil {
		return forge.MR{}, existsError(err, "already exists")
	}
	return w.mr(d.Project), nil
}

type wireGroupMember struct {
	wireUser
	State           string `json:"state"`
	MembershipState string `json:"membership_state"`
	Bot             bool   `json:"bot"`
}

// ReviewerGroupMembers implements forge.ReviewerGroupReader
// (GET /groups/:id/members/all, which includes members inherited from parent
// groups). Blocked and deactivated users, invitations still awaiting
// acceptance and bots are left out.
func (c *Client) ReviewerGroupMembers(ctx context.Context, group string) ([]forge.User, error) {
	if group == "" {
		return nil, errors.New("gitlab: ReviewerGroupMembers needs a group")
	}
	raw, err := getAll[wireGroupMember](ctx, c, groupPath(group)+"/members/all", nil, false)
	if err != nil {
		return nil, err
	}
	var out []forge.User
	for _, m := range raw {
		if m.Bot || (m.State != "" && m.State != "active") || (m.MembershipState != "" && m.MembershipState != "active") {
			continue
		}
		out = append(out, m.user())
	}
	slices.SortFunc(out, func(a, b forge.User) int { return strings.Compare(a.Username, b.Username) })
	return slices.CompactFunc(out, func(a, b forge.User) bool { return a.ID == b.ID }), nil
}

// existsError maps GitLab's 400/409 "already exists" answers to forge.ErrExists.
func existsError(err error, marker string) error {
	var ae *APIError
	if errors.As(err, &ae) && (ae.Status == http.StatusBadRequest || ae.Status == http.StatusConflict) &&
		strings.Contains(strings.ToLower(ae.Message), marker) {
		return fmt.Errorf("%w: %w", forge.ErrExists, err)
	}
	return err
}

// ReadFile implements forge.IssueWriter (GET .../repository/files/:path/raw).
func (c *Client) ReadFile(ctx context.Context, project, ref, path string) ([]byte, error) {
	resp, err := c.do(ctx, request{
		method: http.MethodGet, path: projectPath(project) + "/repository/files/" + url.PathEscape(path) + "/raw",
		query: url.Values{"ref": {ref}}, accept: "*/*",
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("gitlab: reading %s: %w", path, err)
	}
	if len(b) > maxFileBytes {
		return nil, fmt.Errorf("gitlab: %s is larger than %d bytes", path, maxFileBytes)
	}
	return b, nil
}

// ListFiles implements forge.IssueWriter (the repository tree of one level).
func (c *Client) ListFiles(ctx context.Context, project, ref, dir string) ([]string, error) {
	q := url.Values{"path": {dir}, "ref": {ref}}
	raw, err := getAll[struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}](ctx, c, projectPath(project)+"/repository/tree", q, false)
	if errors.Is(err, forge.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range raw {
		if e.Type == "blob" {
			out = append(out, e.Path)
		}
	}
	return out, nil
}
