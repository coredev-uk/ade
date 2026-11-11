package fake

import (
	"fmt"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// Renderers build the JSON documents as maps so tier-gated keys can be left
// out entirely, which is how GitLab Free behaves (absent, not null).

type object = map[string]any

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func tsp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func (s *Server) webURL(path string) string { return s.URL + "/" + path }

func userJSON(u *user) object {
	return object{"id": u.ID, "username": u.Username, "name": u.Name, "state": "active", "avatar_url": nil}
}

func usersJSON(us []*user) []object {
	out := make([]object, 0, len(us))
	for _, u := range us {
		out = append(out, userJSON(u))
	}
	return out
}

func (s *Server) milestoneJSON(m *milestone) any {
	if m == nil {
		return nil
	}
	return object{
		"id": m.ID, "iid": m.IID, "group_id": groupID, "title": m.Title, "description": nil,
		"state": m.State, "start_date": m.Start, "due_date": m.Due, "expired": false,
		"web_url": s.webURL(fmt.Sprintf("groups/%s/-/milestones/%d", s.st.group, m.IID)),
	}
}

// iterationState is the integer REST reports: 1 upcoming, 2 current, 3 closed
// (an inference from GitLab's source; the docs never define it).
func (s *Server) iterationState(it *iteration) int {
	today := s.st.clock.Truncate(24 * time.Hour)
	switch {
	case today.Before(it.Start):
		return 1
	case today.After(it.Due):
		return 3
	default:
		return 2
	}
}

// iterationStateName is the state filter value an iteration answers to.
func (s *Server) iterationStateName(it *iteration) string {
	return [...]string{"", "upcoming", "current", "closed"}[s.iterationState(it)]
}

func (s *Server) iterationJSON(it *iteration) any {
	if it == nil {
		return nil
	}
	var title any
	if it.Title != "" {
		title = it.Title
	}
	return object{
		"id": it.ID, "iid": it.IID, "sequence": it.Seq, "group_id": groupID,
		"title": title, "description": nil, "state": s.iterationState(it),
		"created_at": ts(s.st.clock), "updated_at": ts(s.st.clock),
		"start_date": it.Start.Format("2006-01-02"), "due_date": it.Due.Format("2006-01-02"),
		"web_url": s.webURL(fmt.Sprintf("groups/%s/-/iterations/%d", s.st.group, it.ID)),
	}
}

// refs renders GitLab's references object. relative names the object from the
// context project's point of view: short inside it, full outside.
func refsJSON(full forge.Ref, ctx *project) object {
	sep := "#"
	if full.Kind == forge.KindMR {
		sep = "!"
	}
	short := fmt.Sprintf("%s%d", sep, full.IID)
	rel := full.String()
	if ctx != nil && full.Project == ctx.Path {
		rel = short
	} else if ctx != nil && forge.Group(full.Project) == forge.Group(ctx.Path) {
		rel = full.Project[len(forge.Group(full.Project))+1:] + short
	}
	return object{"short": short, "relative": rel, "full": full.String()}
}

// issueJSON renders an issue. ctx is the project the request addressed
// (nil for group and global lists).
func (s *Server) issueJSON(is *issue, ctx *project) object {
	done, total := taskCounts(is.Desc)
	labels := is.Labels
	if labels == nil {
		labels = []string{}
	}
	o := object{
		"id": is.ID, "iid": is.IID, "project_id": is.Project.ID, "title": is.Title,
		"description": is.Desc, "state": is.State, "created_at": ts(is.Created), "updated_at": ts(is.Updated),
		"closed_at": tsp(is.Closed), "closed_by": nil, "labels": labels,
		"milestone": s.milestoneJSON(is.Milestone), "assignees": usersJSON(is.Assignees),
		"author": userJSON(is.Author), "type": "ISSUE", "issue_type": "issue",
		"user_notes_count": s.userNotes(s.st.ref(is.Project, is.IID, forge.KindIssue)), "merge_requests_count": 0, "due_date": nil, "confidential": false,
		"web_url":    s.webURL(fmt.Sprintf("%s/-/issues/%d", is.Project.Path, is.IID)),
		"references": refsJSON(s.st.ref(is.Project, is.IID, forge.KindIssue), ctx),
		"has_tasks":  total > 0, "task_completion_status": object{"count": total, "completed_count": done},
	}
	if len(is.Assignees) > 0 {
		o["assignee"] = userJSON(is.Assignees[0])
	} else {
		o["assignee"] = nil
	}
	if s.tier == Premium {
		o["weight"] = nilOrInt(is.Weight)
		o["iteration"] = s.iterationJSON(is.Iteration)
		if is.Epic != "" {
			o["epic"] = object{"id": 42, "iid": 5, "title": is.Epic, "group_id": groupID}
		} else {
			o["epic"] = nil
		}
	}
	return o
}

// userNotes counts the human (non-system) notes of an issue's discussions,
// like GitLab's user_notes_count.
func (s *Server) userNotes(ref forge.Ref) int {
	n := 0
	for _, d := range s.st.threads[ref] {
		for _, note := range d.Notes {
			if !note.System {
				n++
			}
		}
	}
	return n
}

func nilOrInt(n *int) any {
	if n == nil {
		return nil
	}
	return *n
}

func (s *Server) pipelineJSON(p *pipeline, m *mr) any {
	if p == nil {
		return nil
	}
	return object{
		"id": p.ID, "iid": p.ID - 9000, "project_id": m.Project.ID, "status": p.Status, "source": "merge_request_event",
		"ref": m.Source, "sha": m.SHA, "created_at": ts(p.Updated), "updated_at": ts(p.Updated),
		"web_url": s.webURL(fmt.Sprintf("%s/-/pipelines/%d", m.Project.Path, p.ID)),
	}
}

func (s *Server) detailedStatus(m *mr) string {
	switch {
	case m.State != "opened":
		return "not_open"
	case m.Draft:
		return "draft_status"
	case s.tier == Premium && len(m.Approved) < s.st.approvalsRequired:
		return "not_approved"
	default:
		return "mergeable"
	}
}

// mrJSON renders a merge request. Lists carry only the deprecated `pipeline`
// key; the single-MR view adds head_pipeline, as GitLab does.
func (s *Server) mrJSON(m *mr, ctx *project, single bool) object {
	o := object{
		"id": m.ID, "iid": m.IID, "project_id": m.Project.ID, "title": m.Title, "description": m.Desc,
		"state": m.State, "draft": m.Draft, "work_in_progress": m.Draft,
		"created_at": ts(m.Created), "updated_at": ts(m.Updated), "merged_at": tsp(m.Merged),
		"target_branch": m.Target, "source_branch": m.Source, "sha": m.SHA,
		"author": userJSON(m.Author), "assignees": usersJSON(m.Assignees), "reviewers": usersJSON(m.Reviewers),
		"labels": append([]string{}, m.Labels...), "milestone": nil, "user_notes_count": 0,
		"web_url":               s.webURL(fmt.Sprintf("%s/-/merge_requests/%d", m.Project.Path, m.IID)),
		"references":            refsJSON(s.st.ref(m.Project, m.IID, forge.KindMR), ctx),
		"has_conflicts":         false,
		"detailed_merge_status": s.detailedStatus(m),
		"pipeline":              s.pipelineJSON(m.Pipeline, m),
	}
	if m.MergedBy != nil {
		o["merged_by"], o["merge_user"] = userJSON(m.MergedBy), userJSON(m.MergedBy)
	}
	if single {
		o["head_pipeline"] = s.pipelineJSON(m.Pipeline, m)
	}
	return o
}

// approvalsJSON renders GET .../approvals. EE attributes are always present;
// on Free the licence flags are false and approvals_required is 0, as seen on
// the gitlab.com Free group.
func (s *Server) approvalsJSON(m *mr) object {
	by := make([]object, 0, len(m.Approved))
	for _, u := range m.Approved {
		by = append(by, object{"user": userJSON(u), "approved_at": ts(s.st.clock.Add(-time.Hour))})
	}
	required, premium := 0, s.tier == Premium
	if premium {
		required = s.st.approvalsRequired
	}
	left := max(required-len(m.Approved), 0)
	return object{
		"id": m.ID, "iid": m.IID, "project_id": m.Project.ID, "title": m.Title, "state": m.State,
		"approved": left == 0, "approvals_required": required, "approvals_left": left,
		"approved_by": by, "approvers": []object{}, "approver_groups": []object{}, "suggested_approvers": []object{},
		"user_has_approved": hasUser(m.Approved, s.st.me.Username), "user_can_approve": false,
		"approval_rules_left": []object{}, "has_approval_rules": premium,
		"merge_request_approvers_available": premium, "multiple_approval_rules_available": premium,
	}
}

func (s *Server) discussionJSON(d *discussion, ref forge.Ref) object {
	notes := make([]object, 0, len(d.Notes))
	for _, n := range d.Notes {
		notes = append(notes, s.noteJSON(n, ref))
	}
	return object{"id": d.ID, "individual_note": d.Individual, "notes": notes}
}

func (s *Server) noteJSON(n *note, ref forge.Ref) object {
	kind := "MergeRequest"
	if ref.Kind == forge.KindIssue {
		kind = "Issue"
	}
	o := object{
		"id": n.ID, "type": nil, "body": n.Body, "attachment": nil, "author": userJSON(n.Author),
		"created_at": ts(n.Created), "updated_at": ts(n.Created), "system": n.System,
		"noteable_id": 1, "noteable_type": kind, "noteable_iid": ref.IID, //nolint:misspell // GitLab's own key
		"resolvable": n.Resolvable, "resolved": n.Resolved,
	}
	if n.Resolvable {
		o["resolved_by"], o["resolved_at"] = nil, nil
	}
	if n.Position != nil {
		o["type"] = "DiffNote"
		o["position"] = positionJSON(n.Position)
	}
	return o
}

// positionJSON renders a text position. Seeded positions carry no SHAs and
// get placeholders.
func positionJSON(p *position) object {
	oldPath := p.OldPath
	if oldPath == "" {
		oldPath = p.Path
	}
	pos := object{
		"base_sha": orDefault(p.Base, "b5d6e7b1"), "start_sha": orDefault(p.Start, "7c9c2ead"), "head_sha": orDefault(p.Head, "4803c71e"),
		"position_type": "text", "old_path": oldPath, "new_path": p.Path,
	}
	if p.NewLine > 0 {
		pos["new_line"] = p.NewLine
	}
	if p.OldLine > 0 {
		pos["old_line"] = p.OldLine
	}
	return pos
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
