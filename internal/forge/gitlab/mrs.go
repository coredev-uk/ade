package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/coredev-uk/ade/internal/forge"
)

// ListMRs implements forge.Forge. List payloads carry no approvals or thread
// counts (GitLab omits them from lists): use MR for the full picture.
func (c *Client) ListMRs(ctx context.Context, q forge.MRQuery) ([]forge.MR, error) {
	if q.Project == "" {
		return nil, errors.New("gitlab: ListMRs needs a project")
	}
	v := url.Values{}
	v.Set("scope", "all")
	v.Set("state", stateOr(q.State, "opened"))
	for k, val := range map[string]string{
		"author_username":   q.Author,
		"assignee_username": q.Assignee,
		"reviewer_username": q.Reviewer,
		"source_branch":     q.SourceBranch,
	} {
		if val != "" {
			v.Set(k, val)
		}
	}
	setUpdatedAfter(v, q.UpdatedAfter)
	return c.mrList(ctx, projectPath(q.Project)+"/merge_requests", v, q.Project)
}

func (c *Client) mrList(ctx context.Context, path string, v url.Values, project string) ([]forge.MR, error) {
	raw, err := getAll[wireMR](ctx, c, path, v, false)
	if err != nil {
		return nil, err
	}
	out := make([]forge.MR, len(raw))
	for i, w := range raw {
		out[i] = w.mr(project)
	}
	return out, nil
}

// MR implements forge.Forge: the merge request plus its approvals and the
// resolvable-thread counts. Approvals.Required is -1 when the instance does
// not license approval rules.
func (c *Client) MR(ctx context.Context, ref forge.Ref) (forge.MR, error) {
	if err := wantKind(ref, forge.KindMR); err != nil {
		return forge.MR{}, err
	}
	var (
		w         wireMR
		appr      wireApprovals
		discs     []forge.Discussion
		reviewers []wireReviewer
	)
	base := mrPath(ref)
	err := concurrently(
		func() error { return c.getJSON(ctx, base, nil, false, &w) },
		func() error { return c.getJSON(ctx, base+"/approvals", nil, false, &appr) },
		func() (err error) { discs, err = c.Discussions(ctx, ref); return },
		func() error {
			// Reviewer verdicts are best effort: an instance without the
			// endpoint just has no "changes requested" signal.
			err := c.getJSON(ctx, base+"/reviewers", nil, true, &reviewers)
			if errors.Is(err, forge.ErrUnavailable) || errors.Is(err, forge.ErrNotFound) {
				return nil
			}
			return err
		},
	)
	if err != nil {
		return forge.MR{}, err
	}
	mr := w.mr(ref.Project)
	mr.Approvals = appr.approvals()
	mr.Unresolved, mr.Resolved = threadCounts(discs)
	mr.Threads = forge.ThreadStates(discs)
	mr.ChangesRequestedBy = changesRequested(reviewers)
	return mr, nil
}

// MRsForIssue implements forge.Forge: the union of MRs that GitLab says
// will close the issue and MRs that merely mention it, closing ones first.
func (c *Client) MRsForIssue(ctx context.Context, ref forge.Ref) ([]forge.MR, error) {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return nil, err
	}
	var closing, related []forge.MR
	err := concurrently(
		func() (err error) {
			closing, err = c.mrList(ctx, issuePath(ref)+"/closed_by", nil, ref.Project)
			return
		},
		func() (err error) {
			related, err = c.mrList(ctx, issuePath(ref)+"/related_merge_requests", nil, ref.Project)
			return
		},
	)
	if err != nil {
		return nil, err
	}
	seen := make(map[forge.Ref]bool, len(closing)+len(related))
	out := make([]forge.MR, 0, len(closing)+len(related))
	for _, mr := range append(closing, related...) {
		if !seen[mr.Ref] {
			seen[mr.Ref] = true
			out = append(out, mr)
		}
	}
	return out, nil
}

// Discussions implements forge.Forge for merge requests and issues.
func (c *Client) Discussions(ctx context.Context, ref forge.Ref) ([]forge.Discussion, error) {
	var path string
	switch ref.Kind {
	case forge.KindMR:
		path = mrPath(ref)
	case forge.KindIssue:
		path = issuePath(ref)
	}
	if ref.Project == "" || ref.IID <= 0 || path == "" {
		return nil, fmt.Errorf("gitlab: invalid reference %q", ref)
	}
	raw, err := getAll[wireDiscussion](ctx, c, path+"/discussions", nil, false)
	if err != nil {
		return nil, err
	}
	out := make([]forge.Discussion, len(raw))
	for i, w := range raw {
		out[i] = w.discussion()
	}
	return out, nil
}

func mrPath(ref forge.Ref) string {
	return projectPath(ref.Project) + "/merge_requests/" + strconv.Itoa(ref.IID)
}
