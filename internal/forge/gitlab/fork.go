package gitlab

import (
	"context"
	"strconv"

	"github.com/coredev-uk/ade/internal/forge"
)

// forkOf reports whether the MR's source branch lives in another project than
// its target: GitLab sends source_project_id and target_project_id on every
// merge request (docs/gitlab-api/workflow.md, "Merge request fields").
func (w wireMR) forkOf() bool {
	return w.SourceProjectID != 0 && w.TargetProjectID != 0 && w.SourceProjectID != w.TargetProjectID
}

// withSource converts w like mr, then, for a fork MR, names the source project
// (see sourcePath). The lookup is best effort: a source project the token
// cannot read leaves SourceProject empty, and Fork still says it is a fork.
func (c *Client) withSource(ctx context.Context, w wireMR, project string) forge.MR {
	mr := w.mr(project)
	if mr.Fork {
		mr.SourceProject = c.sourcePath(ctx, w.SourceProjectID)
	}
	return mr
}

// sourcePath is the full path of project id, read once per client from
// GET /projects/:id (path_with_namespace) and then cached: a list of MRs from
// one fork costs one request, and MRs of the same project none.
func (c *Client) sourcePath(ctx context.Context, id int64) string {
	if p, ok := c.projectPaths.Load(id); ok {
		return p.(string)
	}
	var p struct {
		Path string `json:"path_with_namespace"`
	}
	if err := c.getJSON(ctx, projectPath(strconv.FormatInt(id, 10)), nil, false, &p); err != nil {
		return ""
	}
	c.projectPaths.Store(id, p.Path)
	return p.Path
}
