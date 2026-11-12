package gitlab

import (
	"context"
	_ "embed"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// mrDetailsQuery is validated against schema.graphql by the tests.
//
//go:embed queries/mr_details.graphql
var mrDetailsQuery string

// mrDetailChunk is how many merge requests one GraphQL request reads. With
// 100 conversations of 100 notes each, 25 costs about 100 of GitLab's default
// complexity limit of 250; 50 would cost about 150.
const mrDetailChunk = 25

var _ forge.MRDetailer = (*Client)(nil)

// MRDetails implements forge.MRDetailer with one GraphQL request per chunk of
// merge requests of a project (REST needs MR, approvals, discussions and
// reviewers per merge request). Instances whose GraphQL cannot answer the
// query (older self-managed versions lacking a field, GraphQL disabled) are
// remembered and served through MR from then on; so is any single MR whose
// conversations do not fit the first page.
func (c *Client) MRDetails(ctx context.Context, mrs []forge.MR, caps forge.Caps) ([]forge.MR, error) {
	out := make([]forge.MR, len(mrs))
	copy(out, mrs)

	byProject := map[string][]int{}
	var order []string
	for i, mr := range mrs {
		if _, ok := byProject[mr.Ref.Project]; !ok {
			order = append(order, mr.Ref.Project)
		}
		byProject[mr.Ref.Project] = append(byProject[mr.Ref.Project], i)
	}

	var rest []int // MRs to read one by one
	for _, project := range order {
		idx := byProject[project]
		for len(idx) > 0 {
			n := min(len(idx), mrDetailChunk)
			chunk := idx[:n]
			idx = idx[n:]
			if c.noBatch.Load() {
				rest = append(rest, chunk...)
				continue
			}
			got, err := c.mrDetailChunk(ctx, project, mrs, chunk, caps)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if unsupported(err) {
					c.noBatch.Store(true)
				}
				rest = append(rest, chunk...)
				continue
			}
			for _, i := range chunk {
				if d, ok := got[i]; ok {
					out[i] = d
				} else {
					rest = append(rest, i)
				}
			}
		}
	}

	if len(rest) == 0 {
		return out, nil
	}
	errs := make([]error, len(rest))
	done := make([]func() error, len(rest))
	for k, i := range rest {
		done[k] = func() error {
			full, err := c.MR(ctx, mrs[i].Ref)
			if err != nil {
				errs[k] = err
				return nil
			}
			out[i] = full
			return nil
		}
	}
	_ = concurrently(done...)
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// unsupported reports whether err says this instance cannot answer the batch
// query at all, as opposed to a transient failure of one request.
func unsupported(err error) bool {
	var se *graphqlSchemaError
	if errors.As(err, &se) {
		return true
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
			return true
		}
	}
	return false
}

type wireDetailNode struct {
	IID               string `json:"iid"`
	ApprovalsRequired *int   `json:"approvalsRequired"`
	ApprovedBy        *struct {
		Nodes []gqlUser `json:"nodes"`
	} `json:"approvedBy"`
	HeadPipeline *struct {
		ID        string    `json:"id"`
		Status    string    `json:"status"`
		Path      string    `json:"path"`
		UpdatedAt time.Time `json:"updatedAt"`
	} `json:"headPipeline"`
	Reviewers *struct {
		Nodes []struct {
			gqlUser
			Interaction *struct {
				ReviewState string `json:"reviewState"`
			} `json:"mergeRequestInteraction"`
		} `json:"nodes"`
	} `json:"reviewers"`
	Discussions *struct {
		PageInfo struct {
			HasNextPage bool `json:"hasNextPage"`
		} `json:"pageInfo"`
		Nodes []struct {
			ID         string `json:"id"`
			Resolvable bool   `json:"resolvable"`
			Resolved   bool   `json:"resolved"`
			Notes      struct {
				PageInfo struct {
					HasNextPage bool `json:"hasNextPage"`
				} `json:"pageInfo"`
				Nodes []struct {
					System bool     `json:"system"`
					Author *gqlUser `json:"author"`
				} `json:"nodes"`
			} `json:"notes"`
		} `json:"nodes"`
	} `json:"discussions"`
}

type gqlUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

func (u gqlUser) user() forge.User {
	return forge.User{ID: gidNumber(u.ID), Username: u.Username, Name: u.Name}
}

// gidNumber is the numeric tail of a GraphQL global id
// ("gid://gitlab/User/12" is 12); 0 when there is none.
func gidNumber(gid string) int64 {
	n, _ := strconv.ParseInt(gid[strings.LastIndex(gid, "/")+1:], 10, 64)
	return n
}

// mrDetailChunk reads one chunk (indexes into mrs, all of one project). The
// result holds the MRs the answer covered completely; the rest are absent.
func (c *Client) mrDetailChunk(ctx context.Context, project string, mrs []forge.MR, chunk []int, caps forge.Caps) (map[int]forge.MR, error) {
	iids := make([]string, len(chunk))
	for k, i := range chunk {
		iids[k] = strconv.Itoa(mrs[i].Ref.IID)
	}
	var data struct {
		Project *struct {
			MergeRequests *struct {
				Nodes []wireDetailNode `json:"nodes"`
			} `json:"mergeRequests"`
		} `json:"project"`
	}
	vars := map[string]any{"path": project, "iids": iids, "first": len(iids)}
	if err := c.graphql(ctx, mrDetailsQuery, vars, &data); err != nil {
		return nil, err
	}
	got := map[int]forge.MR{}
	if data.Project == nil || data.Project.MergeRequests == nil {
		return got, nil
	}
	nodes := make(map[string]wireDetailNode, len(data.Project.MergeRequests.Nodes))
	for _, n := range data.Project.MergeRequests.Nodes {
		nodes[n.IID] = n
	}
	for k, i := range chunk {
		n, ok := nodes[iids[k]]
		if !ok || n.Discussions == nil || n.Discussions.PageInfo.HasNextPage {
			continue
		}
		if d, ok := n.apply(mrs[i], c.base, caps); ok {
			got[i] = d
		}
	}
	return got, nil
}

// origin is scheme://host of a base URL: GraphQL paths already carry the
// instance's relative URL root.
func origin(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return base
	}
	return u.Scheme + "://" + u.Host
}

// apply fills mr in from the node, the way MR fills it from REST. ok is false
// when the node is incomplete (a conversation with more notes than one page).
func (n wireDetailNode) apply(mr forge.MR, base string, caps forge.Caps) (forge.MR, bool) {
	discs := make([]forge.Discussion, 0, len(n.Discussions.Nodes))
	for _, d := range n.Discussions.Nodes {
		if d.Notes.PageInfo.HasNextPage {
			return mr, false
		}
		disc := forge.Discussion{ID: d.ID[strings.LastIndex(d.ID, "/")+1:], Resolvable: d.Resolvable, Resolved: d.Resolvable && d.Resolved}
		for _, note := range d.Notes.Nodes {
			var by forge.User
			if note.Author != nil {
				by = note.Author.user()
			}
			disc.Notes = append(disc.Notes, forge.Note{Author: by, System: note.System})
		}
		discs = append(discs, disc)
	}
	mr.Unresolved, mr.Resolved = threadCounts(discs)
	mr.Threads = forge.ThreadStates(discs)

	mr.Approvals = forge.Approvals{Required: -1}
	if caps.ApprovalRules && n.ApprovalsRequired != nil {
		mr.Approvals.Required = *n.ApprovalsRequired
	}
	if n.ApprovedBy != nil {
		for _, u := range n.ApprovedBy.Nodes {
			mr.Approvals.ApprovedBy = append(mr.Approvals.ApprovedBy, u.user())
		}
	}

	mr.ChangesRequestedBy = nil
	if n.Reviewers != nil {
		for _, r := range n.Reviewers.Nodes {
			if r.Interaction != nil && strings.EqualFold(r.Interaction.ReviewState, "REQUESTED_CHANGES") {
				mr.ChangesRequestedBy = append(mr.ChangesRequestedBy, r.user())
			}
		}
	}

	if p := n.HeadPipeline; p != nil {
		mr.Pipeline = &forge.Pipeline{
			ID: gidNumber(p.ID), Status: strings.ToLower(p.Status), WebURL: origin(base) + p.Path, UpdatedAt: p.UpdatedAt,
		}
	}
	return mr, true
}
