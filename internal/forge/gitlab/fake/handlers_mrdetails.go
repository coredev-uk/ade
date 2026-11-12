package fake

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// WithoutMRDetails makes the fake answer the batched merge-request query the
// way an older self-managed GitLab does: a validation error naming a field it
// lacks, with no data. The driver must fall back to REST.
func WithoutMRDetails() Option { return func(o *options) { o.noMRDetails = true } }

// mrDetails answers the driver's MRDetails document: pipeline, approvals,
// reviewer verdicts and conversations of the project's merge requests named
// by iids (the caller holds s.mu).
func (s *Server) mrDetails(w http.ResponseWriter, vars map[string]any) {
	if s.opts.noMRDetails {
		writeJSON(w, http.StatusOK, object{"errors": []object{{
			"message": "Field 'reviewers' doesn't exist on type 'MergeRequest'",
		}}})
		return
	}
	path, _ := vars["path"].(string)
	p := s.st.findProject(path)
	if p == nil {
		writeJSON(w, http.StatusOK, object{"data": object{"project": nil}})
		return
	}
	first, _ := vars["first"].(float64)
	nodes := []object{}
	iids, _ := vars["iids"].([]any)
	for _, v := range iids {
		iid, err := strconv.Atoi(fmt.Sprint(v))
		if err != nil || len(nodes) >= int(first) {
			continue
		}
		if m := s.st.findMR(p, iid); m != nil {
			nodes = append(nodes, s.mrDetailNode(m))
		}
	}
	writeJSON(w, http.StatusOK, object{"data": object{"project": object{"mergeRequests": object{"nodes": nodes}}}})
}

func gqlUser(u *user) object {
	return object{"id": fmt.Sprintf("gid://gitlab/User/%d", u.ID), "username": u.Username, "name": u.Name}
}

func (s *Server) mrDetailNode(m *mr) object {
	required := 0
	if s.tier == Premium {
		required = s.st.approvalsRequired
	}
	approvedBy := make([]object, 0, len(m.Approved))
	for _, u := range m.Approved {
		approvedBy = append(approvedBy, gqlUser(u))
	}
	var pipeline any
	if p := m.Pipeline; p != nil {
		pipeline = object{
			"id": fmt.Sprintf("gid://gitlab/Ci::Pipeline/%d", p.ID), "status": strings.ToUpper(p.Status),
			"path": fmt.Sprintf("/%s/-/pipelines/%d", m.Project.Path, p.ID), "updatedAt": ts(p.Updated),
		}
	}
	reviewers := make([]object, 0, len(m.Reviewers))
	for _, u := range m.Reviewers {
		state := m.ReviewState[u.Username]
		if state == "" {
			state = "unreviewed"
		}
		r := gqlUser(u)
		r["mergeRequestInteraction"] = object{"reviewState": strings.ToUpper(state)}
		reviewers = append(reviewers, r)
	}
	ref := s.st.ref(m.Project, m.IID, forge.KindMR)
	discussions := []object{}
	for _, d := range s.st.threads[ref] {
		resolvable, resolved := false, true
		notes := make([]object, 0, len(d.Notes))
		for _, n := range d.Notes {
			if n.Resolvable {
				resolvable = true
				resolved = resolved && n.Resolved
			}
			notes = append(notes, object{"system": n.System, "author": gqlUser(n.Author)})
		}
		discussions = append(discussions, object{
			"id": "gid://gitlab/Discussion/" + d.ID, "resolvable": resolvable, "resolved": resolvable && resolved,
			"notes": object{"pageInfo": object{"hasNextPage": false}, "nodes": notes},
		})
	}
	return object{
		"iid": strconv.Itoa(m.IID), "approvalsRequired": required,
		"approvedBy": object{"nodes": approvedBy}, "headPipeline": pipeline,
		"reviewers":   object{"nodes": reviewers},
		"discussions": object{"pageInfo": object{"hasNextPage": false}, "nodes": discussions},
	}
}
