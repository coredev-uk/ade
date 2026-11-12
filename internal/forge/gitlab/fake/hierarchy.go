package fake

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// WithoutHierarchy makes the work item queries and the parent mutation fail
// like an instance whose GraphQL schema lacks them.
func WithoutHierarchy() Option { return func(o *options) { o.noHierarchy = true } }

// WithRefusedParents makes workItemUpdate refuse every parent, as GitLab does
// for a pair of work item types that may not nest (an issue under an issue).
func WithRefusedParents() Option { return func(o *options) { o.refuseParents = true } }

const refusedParentMessage = "No matching work item found. Make sure that you are adding a valid work item ID."

func workItemID(is *issue) string { return fmt.Sprintf("gid://gitlab/WorkItem/%d", is.ID) }

func (s *Server) noHierarchyReply(w http.ResponseWriter) bool {
	if !s.opts.noHierarchy {
		return false
	}
	writeJSON(w, http.StatusOK, object{"errors": []object{{"message": "Field 'workItems' doesn't exist on type 'Group'"}}})
	return true
}

// workItems answers WorkItemParents and WorkItemChildren: the issues of the
// group's projects whose iid is asked for, with the hierarchy widget.
func (s *Server) workItems(w http.ResponseWriter, query string, vars map[string]any) {
	if s.noHierarchyReply(w) {
		return
	}
	path, _ := vars["path"].(string)
	if !s.st.hasGroup(path) {
		writeJSON(w, http.StatusOK, object{"data": object{"group": nil}})
		return
	}
	first, _ := vars["first"].(float64)
	wantIID := map[int]bool{}
	iids, _ := vars["iids"].([]any)
	for _, v := range iids {
		if n, err := strconv.Atoi(fmt.Sprint(v)); err == nil {
			wantIID[n] = true
		}
	}
	nodes := []object{}
	for _, is := range s.st.issues {
		if !wantIID[is.IID] || !strings.HasPrefix(is.Project.Path, path+"/") || len(nodes) >= int(first) {
			continue
		}
		node := object{"reference": s.st.ref(is.Project, is.IID, forge.KindIssue).String()}
		if strings.Contains(query, "query WorkItemChildren") {
			kids := []object{}
			for _, k := range s.st.issues {
				if k.Parent == is {
					kids = append(kids, object{"reference": s.st.ref(k.Project, k.IID, forge.KindIssue).String()})
				}
			}
			node["widgets"] = []object{{"children": object{"nodes": kids}}}
		} else {
			node["id"] = workItemID(is)
			var parent any
			if is.Parent != nil {
				parent = object{"reference": s.st.ref(is.Parent.Project, is.Parent.IID, forge.KindIssue).String()}
			}
			node["widgets"] = []object{{"parent": parent}}
		}
		nodes = append(nodes, node)
	}
	writeJSON(w, http.StatusOK, object{"data": object{"group": object{"workItems": object{"nodes": nodes}}}})
}

// setParentMutation answers workItemUpdate with a hierarchyWidget parentId.
func (s *Server) setParentMutation(w http.ResponseWriter, vars map[string]any) {
	if s.noHierarchyReply(w) {
		return
	}
	reply := func(errs ...string) {
		if errs == nil {
			errs = []string{}
		}
		writeJSON(w, http.StatusOK, object{"data": object{"workItemUpdate": object{"errors": errs, "workItem": nil}}})
	}
	byID := func(key string) *issue {
		for _, is := range s.st.issues {
			if workItemID(is) == fmt.Sprint(vars[key]) {
				return is
			}
		}
		return nil
	}
	child, parent := byID("id"), byID("parentId")
	if child == nil || parent == nil || s.opts.refuseParents {
		reply(refusedParentMessage)
		return
	}
	child.Parent = parent
	child.Updated = s.st.clock
	reply()
}

// SetParent places child under parent in the work item hierarchy.
func (s *Server) SetParent(child, parent forge.Ref) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.issue(child)
	if err != nil {
		return err
	}
	p, err := s.issue(parent)
	if err != nil {
		return err
	}
	c.Parent = p
	return nil
}

// Parent reports the hierarchy parent of an issue, if it has one.
func (s *Server) Parent(ref forge.Ref) (forge.Ref, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, err := s.issue(ref)
	if err != nil || is.Parent == nil {
		return forge.Ref{}, false
	}
	return s.st.ref(is.Parent.Project, is.Parent.IID, forge.KindIssue), true
}

// HierarchyPair is a tracker issue and the code-repo issue that belongs to it.
type HierarchyPair struct{ Tracker, Child forge.Ref }

// HierarchyCases are the ways a child can belong to a tracker issue without a
// "Parent:" line in its description. Each child is open and assigned to the
// sandbox user.
type HierarchyCases struct {
	// HierarchyOnly: the child sits under the tracker in the work item
	// hierarchy and has no link. The tracker is assigned and in the sprint.
	HierarchyOnly HierarchyPair
	// LinkOnly: the child has a relates_to link to the tracker and no hierarchy
	// parent. The tracker is assigned and in the sprint.
	LinkOnly HierarchyPair
	// HierarchyOutside and LinkOutside are the same two cases with a tracker
	// that is neither assigned to the sandbox user nor in any sprint, so
	// nothing but the child brings it into view.
	HierarchyOutside, LinkOutside HierarchyPair
}

// SeedHierarchyCases adds the four cases of HierarchyCases. The default seed
// has none of them, so tests that count issues are not disturbed.
func (s *Server) SeedHierarchyCases() HierarchyCases {
	s.mu.Lock()
	defer s.mu.Unlock()
	tracker, api, web := s.st.projects[0], s.st.projects[1], s.st.projects[2]
	add := func(p *project, title string, assigned, inSprint bool) *issue {
		is := s.st.newIssue(s.tier, p, issueSpec{title: title, labels: "type::story", sprint: 1, assigned: assigned}, len(s.st.issues))
		if !inSprint {
			is.Milestone, is.Iteration = nil, nil
		}
		return is
	}
	ref := func(is *issue) forge.Ref { return s.st.ref(is.Project, is.IID, forge.KindIssue) }
	pair := func(t, c *issue) HierarchyPair { return HierarchyPair{Tracker: ref(t), Child: ref(c)} }
	addLink := func(child, tr *issue) {
		s.st.links = append(s.st.links, &link{ID: s.st.id(), A: child, B: tr, Type: "relates_to", Created: s.st.clock})
	}

	var out HierarchyCases
	t, c := add(tracker, "Move the site to Tailwind", true, true), add(web, "Tailwind: port the layout components", true, true)
	c.Parent = t
	out.HierarchyOnly = pair(t, c)

	t, c = add(tracker, "Adopt the new date picker", true, true), add(api, "Date picker: API formats", true, true)
	addLink(c, t)
	out.LinkOnly = pair(t, c)

	t, c = add(tracker, "Retire the legacy theme", false, false), add(web, "Legacy theme: remove the stylesheet", true, true)
	c.Parent = t
	out.HierarchyOutside = pair(t, c)

	t, c = add(tracker, "Audit third-party scripts", false, false), add(api, "Scripts audit: CSP headers", true, true)
	addLink(c, t)
	out.LinkOutside = pair(t, c)
	return out
}
