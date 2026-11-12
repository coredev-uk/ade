package gitlab

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/coredev-uk/ade/internal/forge"
)

// The hierarchy documents are validated against schema.graphql by the tests.
//
//go:embed queries/work_item_parents.graphql
var workItemParentsQuery string

//go:embed queries/work_item_children.graphql
var workItemChildrenQuery string

//go:embed queries/set_parent.graphql
var setParentMutation string

// workItemChunk is how many issues one hierarchy request reads, as MRDetails
// does for merge requests.
const workItemChunk = 25

var _ forge.Parenter = (*Client)(nil)

// workItem is what the hierarchy reads give for one issue.
type workItem struct {
	id       string // global id, "gid://gitlab/WorkItem/N"
	parent   forge.Ref
	children []forge.Ref
}

type wireWorkItemNode struct {
	ID        string `json:"id"`
	Reference string `json:"reference"`
	Widgets   []struct {
		Parent *struct {
			Reference string `json:"reference"`
		} `json:"parent"`
		Children *struct {
			Nodes []struct {
				Reference string `json:"reference"`
			} `json:"nodes"`
		} `json:"children"`
	} `json:"widgets"`
}

// hierarchyUnavailable is forge.ErrUnavailable once the instance is known to
// lack GraphQL work items, or an earlier hierarchy request was refused as
// unsupported.
func (c *Client) hierarchyUnavailable() error {
	if c.noHierarchy.Load() || c.missing.has(FeatureWorkItems) {
		return forge.ErrUnavailable
	}
	return nil
}

// workItems reads the work items behind refs, one request per chunk of one
// group, and returns the ones it found by ref. query is the document to send.
func (c *Client) workItems(ctx context.Context, refs []forge.Ref, query string) (map[forge.Ref]workItem, error) {
	if err := c.hierarchyUnavailable(); err != nil {
		return nil, err
	}
	byGroup := map[string][]forge.Ref{}
	var order []string
	for _, r := range refs {
		if err := wantKind(r, forge.KindIssue); err != nil {
			return nil, err
		}
		g := forge.Group(r.Project)
		if _, ok := byGroup[g]; !ok {
			order = append(order, g)
		}
		byGroup[g] = append(byGroup[g], r)
	}
	want := map[forge.Ref]bool{}
	for _, r := range refs {
		want[r] = true
	}
	out := map[forge.Ref]workItem{}
	var mu sync.Mutex
	var jobs []func() error
	for _, g := range order {
		group := byGroup[g]
		for len(group) > 0 {
			chunk := group[:min(len(group), workItemChunk)]
			group = group[len(chunk):]
			jobs = append(jobs, func() error {
				got, err := c.workItemChunk(ctx, g, chunk, query)
				if err != nil {
					return err
				}
				mu.Lock()
				defer mu.Unlock()
				for ref, wi := range got {
					if want[ref] {
						out[ref] = wi
					}
				}
				return nil
			})
		}
	}
	if err := concurrently(jobs...); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if unsupported(err) {
			c.noHierarchy.Store(true)
			return nil, fmt.Errorf("gitlab: work item hierarchy: %w", forge.ErrUnavailable)
		}
		return nil, err
	}
	return out, nil
}

func (c *Client) workItemChunk(ctx context.Context, group string, chunk []forge.Ref, query string) (map[forge.Ref]workItem, error) {
	iids := make([]string, len(chunk))
	for i, r := range chunk {
		iids[i] = strconv.Itoa(r.IID)
	}
	// iids repeat across the group's projects: allow for a few homonyms per issue.
	vars := map[string]any{"path": group, "iids": iids, "first": 4 * workItemChunk}
	var data struct {
		Group *struct {
			WorkItems *struct {
				Nodes []wireWorkItemNode `json:"nodes"`
			} `json:"workItems"`
		} `json:"group"`
	}
	if err := c.graphql(ctx, query, vars, &data); err != nil {
		return nil, err
	}
	if data.Group == nil {
		return nil, nil // not a group (a personal namespace) or not visible: no hierarchy to read
	}
	if data.Group.WorkItems == nil {
		return nil, &graphqlSchemaError{msg: "group.workItems is not available"}
	}
	out := map[forge.Ref]workItem{}
	for _, n := range data.Group.WorkItems.Nodes {
		ref, err := forge.ParseRef(n.Reference)
		if err != nil {
			continue
		}
		wi := workItem{id: n.ID}
		for _, w := range n.Widgets {
			if w.Parent != nil {
				if p, err := forge.ParseRef(w.Parent.Reference); err == nil {
					wi.parent = p
				}
			}
			if w.Children != nil {
				for _, k := range w.Children.Nodes {
					if kr, err := forge.ParseRef(k.Reference); err == nil {
						wi.children = append(wi.children, kr)
					}
				}
			}
		}
		out[ref] = wi
	}
	return out, nil
}

// Parents implements forge.Parenter. Issues the instance does not return
// (no permission, deleted) are absent, as are those without a parent.
func (c *Client) Parents(ctx context.Context, refs []forge.Ref) (map[forge.Ref]forge.Ref, error) {
	items, err := c.workItems(ctx, refs, workItemParentsQuery)
	if err != nil {
		return nil, err
	}
	out := map[forge.Ref]forge.Ref{}
	for ref, wi := range items {
		if !wi.parent.IsZero() {
			out[ref] = wi.parent
		}
	}
	return out, nil
}

// Children implements forge.Parenter.
func (c *Client) Children(ctx context.Context, parent forge.Ref) ([]forge.Ref, error) {
	items, err := c.workItems(ctx, []forge.Ref{parent}, workItemChildrenQuery)
	if err != nil {
		return nil, err
	}
	return items[parent].children, nil
}

// SetParent implements forge.Parenter through workItemUpdate: both issues'
// work item ids are read first. GitLab answers a pair of types that may not
// nest with a payload error, which is forge.ErrParentRefused.
func (c *Client) SetParent(ctx context.Context, child, parent forge.Ref) error {
	items, err := c.workItems(ctx, []forge.Ref{child, parent}, workItemParentsQuery)
	if err != nil {
		return err
	}
	for _, r := range []forge.Ref{child, parent} {
		if _, ok := items[r]; !ok {
			return fmt.Errorf("gitlab: %s has no work item the API can read: %w", r, forge.ErrNotFound)
		}
	}
	vars := map[string]any{"id": items[child].id, "parentId": items[parent].id}
	var out struct {
		WorkItemUpdate *struct {
			Errors []string `json:"errors"`
		} `json:"workItemUpdate"`
	}
	if err := c.graphql(ctx, setParentMutation, vars, &out); err != nil {
		if unsupported(err) {
			c.noHierarchy.Store(true)
			return fmt.Errorf("gitlab: workItemUpdate on %s: %w", child, forge.ErrUnavailable)
		}
		return err
	}
	if out.WorkItemUpdate == nil {
		return fmt.Errorf("gitlab: workItemUpdate on %s: %w", child, forge.ErrUnavailable)
	}
	if errs := out.WorkItemUpdate.Errors; len(errs) > 0 {
		return fmt.Errorf("gitlab: %s under %s: %w: %s", child, parent, forge.ErrParentRefused, strings.Join(errs, "; "))
	}
	return nil
}
