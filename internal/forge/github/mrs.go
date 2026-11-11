package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// ListMRs implements forge.Forge. List payloads carry no approvals, thread
// counts, pipeline or mergeability: use MR for the full picture. GitHub's
// list has no author, assignee or reviewer filters, so they are applied here
// (Reviewer matches a pending review request). Results are newest-updated
// first and listing stops at q.UpdatedAfter.
func (c *Client) ListMRs(ctx context.Context, q forge.MRQuery) ([]forge.MR, error) {
	if q.Project == "" {
		return nil, errors.New("github: ListMRs needs a project")
	}
	state := stateOr(q.State, "opened")
	v := url.Values{"sort": {"updated"}, "direction": {"desc"}}
	switch state {
	case "opened":
		v.Set("state", "open")
	case "merged", "closed":
		v.Set("state", "closed")
	case "all":
		v.Set("state", "all")
	default:
		return nil, fmt.Errorf("github: unknown merge request state %q", state)
	}
	if q.SourceBranch != "" {
		owner, _ := splitProject(q.Project)
		v.Set("head", owner+":"+q.SourceBranch)
	}
	var stop func([]wirePull) bool
	if !q.UpdatedAfter.IsZero() {
		stop = func(page []wirePull) bool {
			return len(page) > 0 && page[len(page)-1].UpdatedAt.Before(q.UpdatedAfter)
		}
	}
	raw, err := getPages(ctx, c, repoPath(q.Project)+"/pulls", v, false, stop, func(b []byte) ([]wirePull, error) {
		return decodeList[wirePull](b)
	})
	if err != nil {
		return nil, err
	}
	var out []forge.MR
	for _, w := range raw {
		mr := w.mr(q.Project)
		switch {
		case !q.UpdatedAfter.IsZero() && mr.UpdatedAt.Before(q.UpdatedAfter):
		case state == "merged" && mr.State != "merged", state == "closed" && mr.State != "closed":
		case q.Author != "" && !strings.EqualFold(mr.Author.Username, q.Author):
		case q.Assignee != "" && !hasUser(mr.Assignees, q.Assignee):
		case q.Reviewer != "" && !hasUser(mr.Reviewers, q.Reviewer):
		default:
			out = append(out, mr)
		}
	}
	return out, nil
}

func hasUser(us []forge.User, login string) bool {
	return slices.ContainsFunc(us, func(u forge.User) bool { return strings.EqualFold(u.Username, login) })
}

// MR implements forge.Forge: the pull request plus its approvals, the
// resolvable-thread counts and the head commit's CI.
func (c *Client) MR(ctx context.Context, ref forge.Ref) (forge.MR, error) {
	if err := wantKind(ref, forge.KindMR); err != nil {
		return forge.MR{}, err
	}
	var (
		w       wirePull
		pl      *forge.Pipeline
		reviews []wireReview
		threads []gqlThread
	)
	err := concurrently(
		func() error {
			if err := c.getJSON(ctx, pullPath(ref), nil, false, &w); err != nil {
				return err
			}
			var err error
			pl, err = c.headPipeline(ctx, ref.Project, w.Head.SHA)
			return err
		},
		func() (err error) {
			reviews, err = getAll[wireReview](ctx, c, pullPath(ref)+"/reviews", nil, false)
			return err
		},
		func() (err error) {
			_, threads, err = c.reviewThreads(ctx, ref)
			return err
		},
	)
	if err != nil {
		return forge.MR{}, err
	}
	mr := w.mr(ref.Project)
	mr.Pipeline = pl
	mr.Approvals = approvals(reviews)
	var ds []forge.Discussion
	for _, t := range threads {
		if d, ok := t.discussion(); ok {
			ds = append(ds, d)
		}
	}
	mr.Unresolved, mr.Resolved = threadCounts(ds)
	mr.Threads = forge.ThreadStates(ds)
	mr.ChangesRequestedBy = changesRequested(reviews)
	return mr, nil
}

// reviewThreads reads every review thread of the pull request and its GraphQL
// node id, following the cursor.
func (c *Client) reviewThreads(ctx context.Context, ref forge.Ref) (nodeID string, threads []gqlThread, err error) {
	owner, name := splitProject(ref.Project)
	var after *string
	for {
		var out struct {
			Repository *struct {
				PullRequest *struct {
					ID            string `json:"id"`
					ReviewThreads struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []gqlThread `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		vars := map[string]any{"owner": owner, "name": name, "number": ref.IID, "after": after}
		if err := c.graphql(ctx, reviewThreadsQuery, vars, &out); err != nil {
			return "", nil, err
		}
		if out.Repository == nil || out.Repository.PullRequest == nil {
			return "", nil, fmt.Errorf("github: %s: %w", ref, forge.ErrNotFound)
		}
		pr := out.Repository.PullRequest
		nodeID = pr.ID
		threads = append(threads, pr.ReviewThreads.Nodes...)
		if !pr.ReviewThreads.PageInfo.HasNextPage {
			return nodeID, threads, nil
		}
		cursor := pr.ReviewThreads.PageInfo.EndCursor
		after = &cursor
	}
}

// MRsForIssue implements forge.Forge: pull requests that GitHub says will
// close the issue, then those that merely mention it. GitHub's closing
// references cross repositories only within the same owner's keywords, so
// both lists are read from GraphQL and each PR is then fetched.
func (c *Client) MRsForIssue(ctx context.Context, ref forge.Ref) ([]forge.MR, error) {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return nil, err
	}
	owner, name := splitProject(ref.Project)
	var out struct {
		Repository *struct {
			Issue *struct {
				Closing struct {
					Nodes []prRef `json:"nodes"`
				} `json:"closedByPullRequestsReferences"`
				Timeline struct {
					Nodes []struct {
						Source struct {
							Typename string `json:"__typename"`
							prRef
						} `json:"source"`
					} `json:"nodes"`
				} `json:"timelineItems"`
			} `json:"issue"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": owner, "name": name, "number": ref.IID}
	if err := c.graphql(ctx, issuePRsQuery, vars, &out); err != nil {
		return nil, err
	}
	if out.Repository == nil || out.Repository.Issue == nil {
		return nil, fmt.Errorf("github: %s: %w", ref, forge.ErrNotFound)
	}
	var refs []forge.Ref
	add := func(p prRef) {
		r := forge.Ref{Project: p.Repository.NameWithOwner, IID: p.Number, Kind: forge.KindMR}
		if r.Project != "" && r.IID > 0 && !slices.Contains(refs, r) {
			refs = append(refs, r)
		}
	}
	for _, p := range out.Repository.Issue.Closing.Nodes {
		add(p)
	}
	for _, n := range out.Repository.Issue.Timeline.Nodes {
		if n.Source.Typename == "PullRequest" {
			add(n.Source.prRef)
		}
	}
	mrs := make([]forge.MR, len(refs))
	fns := make([]func() error, len(refs))
	for i, r := range refs {
		fns[i] = func() error {
			var w wirePull
			if err := c.getJSON(ctx, pullPath(r), nil, false, &w); err != nil {
				return err
			}
			mrs[i] = w.mr(r.Project)
			return nil
		}
	}
	if err := concurrently(fns...); err != nil {
		return nil, err
	}
	return mrs, nil
}

type prRef struct {
	Number     int `json:"number"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

// Discussions implements forge.Forge. A pull request's discussions are its
// review threads (resolvable), its conversation comments and the bodies of
// submitted reviews (neither resolvable), oldest first. An issue has only
// conversation comments.
func (c *Client) Discussions(ctx context.Context, ref forge.Ref) ([]forge.Discussion, error) {
	switch {
	case ref.Kind != forge.KindMR && ref.Kind != forge.KindIssue:
		return nil, fmt.Errorf("github: invalid reference %q", ref)
	case ref.Project == "" || ref.IID <= 0:
		return nil, fmt.Errorf("github: invalid reference %q", ref)
	}
	var (
		comments []wireComment
		reviews  []wireReview
		threads  []gqlThread
	)
	fns := []func() error{
		func() (err error) {
			comments, err = getAll[wireComment](ctx, c, issuePath(ref)+"/comments", nil, false)
			return err
		},
	}
	if ref.Kind == forge.KindMR {
		fns = append(fns,
			func() (err error) {
				reviews, err = getAll[wireReview](ctx, c, pullPath(ref)+"/reviews", nil, false)
				return err
			},
			func() (err error) {
				_, threads, err = c.reviewThreads(ctx, ref)
				return err
			},
		)
	}
	if err := concurrently(fns...); err != nil {
		return nil, err
	}

	var out []forge.Discussion
	for _, t := range threads {
		if d, ok := t.discussion(); ok {
			out = append(out, d)
		}
	}
	for _, cm := range comments {
		out = append(out, forge.Discussion{
			ID: issueCommentPrefix + strconv.FormatInt(cm.ID, 10), Notes: []forge.Note{cm.note()},
		})
	}
	for _, r := range reviews {
		if r.State == "PENDING" || strings.TrimSpace(r.Body) == "" || r.SubmittedAt == nil {
			continue
		}
		out = append(out, forge.Discussion{
			ID: reviewPrefix + strconv.FormatInt(r.ID, 10),
			Notes: []forge.Note{{
				ID: r.ID, Author: r.User.user(), Body: r.Body, CreatedAt: *r.SubmittedAt,
			}},
		})
	}
	slices.SortStableFunc(out, func(a, b forge.Discussion) int { return firstNote(a).Compare(firstNote(b)) })
	return out, nil
}

func firstNote(d forge.Discussion) time.Time {
	if len(d.Notes) == 0 {
		return time.Time{}
	}
	return d.Notes[0].CreatedAt
}
