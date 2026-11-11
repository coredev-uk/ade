package fake

import (
	"strconv"
	"strings"
	"time"
)

// Renderers build the JSON GitHub's REST API answers with. They run under
// s.mu and use maps so absent-versus-null follows GitHub's payloads exactly.

func iso(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func isoOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return iso(t)
}

func strOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Server) apiURL() string          { return s.URL + apiPath }
func (s *Server) repoAPI(rp *repo) string { return s.apiURL() + "/repos/" + rp.Owner + "/" + rp.Name }

func (s *Server) repoHTML(rp *repo) string {
	return s.URL + "/" + rp.Owner + "/" + rp.Name
}

func (s *Server) renderUser(login string) any {
	u := s.st.user(login)
	if u == nil {
		return nil
	}
	return userJSON(s, u)
}

func userJSON(s *Server, u *user) map[string]any {
	prefix := "U"
	switch u.Type {
	case "Bot":
		prefix = "BOT"
	case "Organization":
		prefix = "O"
	}
	return map[string]any{
		"login": u.Login, "id": u.ID, "node_id": nodeID(prefix, u.ID), "type": u.Type, "site_admin": false,
		"url": s.apiURL() + "/users/" + u.Login, "html_url": s.URL + "/" + u.Login,
		"avatar_url": s.URL + "/avatars/u/" + u.Login,
	}
}

// renderFullUser adds the profile fields only /user and /users/{login} carry.
func (s *Server) renderFullUser(u *user) map[string]any {
	m := userJSON(s, u)
	m["name"] = u.Name
	m["created_at"] = iso(s.st.clock.Add(-365 * day))
	return m
}

func (s *Server) renderUsers(logins []string) []any {
	out := make([]any, 0, len(logins))
	for _, l := range logins {
		if u := s.renderUser(l); u != nil {
			out = append(out, u)
		}
	}
	return out
}

func (s *Server) renderRepo(rp *repo) map[string]any {
	return map[string]any{
		"id": rp.ID, "node_id": nodeID("R", rp.ID), "name": rp.Name, "full_name": rp.Owner + "/" + rp.Name,
		"private": true, "owner": s.renderUser(rp.Owner), "html_url": s.repoHTML(rp), "url": s.repoAPI(rp),
		"default_branch": rp.Default, "has_issues": true, "archived": false,
	}
}

func (s *Server) renderLabel(rp *repo, l *label) map[string]any {
	return map[string]any{
		"id": l.ID, "node_id": nodeID("LA", l.ID), "url": s.repoAPI(rp) + "/labels/" + l.Name,
		"name": l.Name, "color": l.Color, "default": false, "description": strOrNil(l.Description),
	}
}

func (s *Server) renderLabels(rp *repo, names []string) []any {
	out := make([]any, 0, len(names))
	for _, n := range names {
		if l := rp.label(n); l != nil {
			out = append(out, s.renderLabel(rp, l))
		}
	}
	return out
}

// milestoneCounts counts the open and closed issues and PRs in a milestone.
func (rp *repo) milestoneCounts(m *milestone) (open, closed int) {
	for _, it := range rp.Items {
		if it.Milestone != m.Number {
			continue
		}
		if it.State == "open" {
			open++
		} else {
			closed++
		}
	}
	return open, closed
}

func (s *Server) renderMilestone(rp *repo, m *milestone) map[string]any {
	open, closed := rp.milestoneCounts(m)
	return map[string]any{
		"id": m.ID, "node_id": nodeID("MI", m.ID), "number": m.Number, "title": m.Title,
		"url":         s.repoAPI(rp) + "/milestones/" + itoa(m.Number),
		"html_url":    s.repoHTML(rp) + "/milestone/" + itoa(m.Number),
		"description": strOrNil(m.Description), "state": m.State, "creator": s.renderUser(viewer),
		"open_issues": open, "closed_issues": closed,
		"created_at": iso(m.Created), "updated_at": iso(m.Updated),
		"due_on": isoOrNil(m.DueOn), "closed_at": isoOrNil(m.Closed),
	}
}

func (s *Server) renderIssue(rp *repo, it *item) map[string]any {
	api := s.repoAPI(rp)
	m := map[string]any{
		"id": it.ID, "node_id": nodeID(map[bool]string{true: "PR", false: "I"}[it.PR != nil], it.ID),
		"number": it.Number, "url": api + "/issues/" + itoa(it.Number), "repository_url": api,
		"html_url": s.itemHTML(rp, it), "title": it.Title, "body": strOrNil(it.Body),
		"user": s.renderUser(it.Author), "labels": s.renderLabels(rp, it.Labels), "state": it.State,
		"state_reason": strOrNil(it.StateReason), "locked": false, "assignees": s.renderUsers(it.Assignees),
		"assignee": nil, "milestone": nil, "comments": len(it.Comments),
		"created_at": iso(it.Created), "updated_at": iso(it.Updated), "closed_at": isoOrNil(it.Closed),
		"author_association": "MEMBER",
	}
	if len(it.Assignees) > 0 {
		m["assignee"] = s.renderUser(it.Assignees[0])
	}
	if ms := rp.milestone(it.Milestone); ms != nil {
		m["milestone"] = s.renderMilestone(rp, ms)
	}
	if it.Parent != nil {
		m["parent_issue_url"] = s.repoAPI(s.st.repoNamed(it.Parent.Repo)) + "/issues/" + itoa(it.Parent.Number)
	}
	if len(it.Subs) > 0 {
		done := 0
		for _, k := range it.Subs {
			if _, c := s.st.lookup(k); c != nil && c.State == "closed" {
				done++
			}
		}
		m["sub_issues_summary"] = map[string]any{
			"total": len(it.Subs), "completed": done, "percent_completed": done * 100 / len(it.Subs),
		}
	}
	if it.PR != nil {
		m["pull_request"] = map[string]any{
			"url": api + "/pulls/" + itoa(it.Number), "html_url": s.itemHTML(rp, it),
			"diff_url":  s.itemHTML(rp, it) + ".diff",
			"patch_url": s.itemHTML(rp, it) + ".patch", "merged_at": isoOrNil(it.PR.MergedAt),
		}
	}
	return m
}

func (s *Server) itemHTML(rp *repo, it *item) string {
	if it.PR != nil {
		return s.repoHTML(rp) + "/pull/" + itoa(it.Number)
	}
	return s.repoHTML(rp) + "/issues/" + itoa(it.Number)
}

func (s *Server) renderComment(rp *repo, it *item, c *comment) map[string]any {
	return map[string]any{
		"id": c.ID, "node_id": nodeID("IC", c.ID), "url": s.repoAPI(rp) + "/issues/comments/" + i64(c.ID),
		"html_url":  s.itemHTML(rp, it) + "#issuecomment-" + i64(c.ID),
		"issue_url": s.repoAPI(rp) + "/issues/" + itoa(it.Number), "body": c.Body,
		"user": s.renderUser(c.User), "created_at": iso(c.Created), "updated_at": iso(c.Updated),
		"author_association": "MEMBER",
	}
}

func (s *Server) renderIssueEvent(rp *repo, ev *issueEvent) map[string]any {
	m := map[string]any{
		"id": ev.ID, "node_id": nodeID("E", ev.ID), "url": s.repoAPI(rp) + "/issues/events/" + i64(ev.ID),
		"actor": s.renderUser(ev.Actor), "event": ev.Event, "commit_id": nil, "created_at": iso(ev.Created),
	}
	if l := rp.label(ev.Label); l != nil && ev.Label != "" {
		m["label"] = map[string]any{"name": l.Name, "color": l.Color}
	} else if ev.Label != "" {
		m["label"] = map[string]any{"name": ev.Label, "color": "ededed"}
	}
	if ev.Milestone != "" {
		m["milestone"] = map[string]any{"title": ev.Milestone}
	}
	if ev.Assignee != "" {
		m["assignee"] = s.renderUser(ev.Assignee)
	}
	return m
}

func (s *Server) renderFeedEvent(rp *repo, ev *feedEvent) map[string]any {
	actor := map[string]any{"login": ev.Actor, "display_login": ev.Actor}
	if u := s.st.user(ev.Actor); u != nil {
		actor["id"] = u.ID
	}
	return map[string]any{
		"id": i64(ev.ID), "type": ev.Type, "actor": actor, "public": false,
		"repo":       map[string]any{"id": rp.ID, "name": rp.Owner + "/" + rp.Name, "url": s.repoAPI(rp)},
		"payload":    ev.Payload,
		"created_at": iso(ev.Created),
	}
}

func (s *Server) renderCommit(rp *repo, c *commit) map[string]any {
	person := map[string]any{"name": "Demo User", "email": "demo-user@example.test", "date": iso(c.Created)}
	return map[string]any{
		"sha": c.SHA, "node_id": "C_" + c.SHA[:12], "url": s.repoAPI(rp) + "/commits/" + c.SHA,
		"html_url": s.repoHTML(rp) + "/commit/" + c.SHA,
		"commit":   map[string]any{"message": c.Message, "author": person, "committer": person},
		"author":   s.renderUser(viewer), "committer": s.renderUser(viewer), "parents": []any{},
	}
}

// renderPull renders a PR. Lists omit merged and the mergeability fields, as
// GitHub does: they are only computed for a single PR.
func (s *Server) renderPull(rp *repo, it *item, single bool) map[string]any {
	p := it.PR
	api := s.repoAPI(rp)
	repoRef := func(branch, sha string) map[string]any {
		return map[string]any{
			"label": rp.Owner + ":" + branch, "ref": branch, "sha": sha,
			"user": s.renderUser(rp.Owner), "repo": s.renderRepo(rp),
		}
	}
	m := map[string]any{
		"id": it.ID, "node_id": nodeID("PR", it.ID), "number": it.Number,
		"url": api + "/pulls/" + itoa(it.Number), "html_url": s.itemHTML(rp, it),
		"diff_url": s.itemHTML(rp, it) + ".diff", "patch_url": s.itemHTML(rp, it) + ".patch",
		"issue_url": api + "/issues/" + itoa(it.Number),
		"title":     it.Title, "body": strOrNil(it.Body), "state": it.State, "draft": p.Draft, "locked": false,
		"user": s.renderUser(it.Author), "labels": s.renderLabels(rp, it.Labels),
		"assignees": s.renderUsers(it.Assignees), "assignee": nil, "milestone": nil,
		"requested_reviewers": s.renderUsers(p.Reviewers), "requested_teams": []any{},
		"head": repoRef(p.Head, p.HeadSHA), "base": repoRef(p.Base, p.BaseSHA),
		"created_at": iso(it.Created), "updated_at": iso(it.Updated),
		"closed_at": isoOrNil(it.Closed), "merged_at": isoOrNil(p.MergedAt),
		"merge_commit_sha": strOrNil(p.MergeSHA), "author_association": "MEMBER", "auto_merge": nil,
	}
	if len(it.Assignees) > 0 {
		m["assignee"] = s.renderUser(it.Assignees[0])
	}
	if ms := rp.milestone(it.Milestone); ms != nil {
		m["milestone"] = s.renderMilestone(rp, ms)
	}
	if am := p.AutoMerge; am != nil {
		m["auto_merge"] = map[string]any{
			"enabled_by": s.renderUser(viewer), "merge_method": strings.ToLower(am.Method),
			"commit_title": nil, "commit_message": nil,
		}
	}
	if single {
		mergeable, mstate := rp.mergeable(it)
		adds, dels := 0, 0
		for _, f := range p.Files {
			adds += f.Additions
			dels += f.Deletions
		}
		m["merged"] = p.Merged
		m["mergeable"] = mergeable
		m["mergeable_state"] = mstate
		m["rebaseable"] = mergeable
		m["comments"] = len(it.Comments)
		m["commits"] = 1
		m["additions"], m["deletions"], m["changed_files"] = adds, dels, len(p.Files)
		if p.Merged {
			m["merged_by"] = s.renderUser(viewer)
		}
	}
	return m
}

func (s *Server) renderFile(rp *repo, f *prFile) map[string]any {
	m := map[string]any{
		"sha": f.SHA, "filename": f.Filename, "status": f.Status,
		"additions": f.Additions, "deletions": f.Deletions, "changes": f.Additions + f.Deletions,
		"blob_url": s.repoHTML(rp) + "/blob/" + f.SHA + "/" + f.Filename,
		"raw_url":  s.repoHTML(rp) + "/raw/" + f.SHA + "/" + f.Filename,
	}
	if f.Patch != "" {
		m["patch"] = f.Patch
	}
	if f.Previous != "" {
		m["previous_filename"] = f.Previous
	}
	return m
}

func (s *Server) renderReview(rp *repo, it *item, r *review) map[string]any {
	m := map[string]any{
		"id": r.ID, "node_id": nodeID("PRR", r.ID), "user": s.renderUser(r.User), "body": r.Body, "state": r.State,
		"html_url":           s.itemHTML(rp, it) + "#pullrequestreview-" + i64(r.ID),
		"pull_request_url":   s.repoAPI(rp) + "/pulls/" + itoa(it.Number),
		"author_association": "MEMBER", "commit_id": r.CommitID,
	}
	if !r.Submitted.IsZero() {
		m["submitted_at"] = iso(r.Submitted)
	}
	return m
}

func (s *Server) renderReviewComment(rp *repo, it *item, t *thread, c *reviewComment) map[string]any {
	m := map[string]any{
		"id": c.ID, "node_id": nodeID("PRRC", c.ID), "pull_request_review_id": c.Review.ID,
		"url":  s.repoAPI(rp) + "/pulls/comments/" + i64(c.ID),
		"path": t.Path, "line": t.Line, "original_line": t.Line, "side": t.Side,
		"start_line": nil, "original_start_line": nil, "subject_type": "line",
		"diff_hunk": diffHunk(it, t.Path), "body": c.Body, "user": s.renderUser(c.User),
		"commit_id": it.PR.HeadSHA, "original_commit_id": it.PR.HeadSHA,
		"created_at": iso(c.Created), "updated_at": iso(c.Updated),
		"html_url":         s.itemHTML(rp, it) + "#discussion_r" + i64(c.ID),
		"pull_request_url": s.repoAPI(rp) + "/pulls/" + itoa(it.Number), "author_association": "MEMBER",
	}
	if t.StartLn > 0 {
		m["start_line"], m["original_start_line"] = t.StartLn, t.StartLn
	}
	if c.InReplyTo != 0 {
		m["in_reply_to_id"] = c.InReplyTo
	}
	return m
}

func diffHunk(it *item, path string) string {
	for _, f := range it.PR.Files {
		if f.Filename == path {
			return f.Patch
		}
	}
	return ""
}

func (s *Server) runHTML(rp *repo, r *run) string {
	return s.repoHTML(rp) + "/actions/runs/" + i64(r.ID)
}

func (s *Server) renderRun(rp *repo, r *run) map[string]any {
	return map[string]any{
		"id": r.ID, "name": r.Name, "display_title": r.Name, "head_branch": r.HeadBranch, "head_sha": r.HeadSHA,
		"run_number": r.Number, "run_attempt": r.Attempt, "event": "pull_request", "status": r.Status,
		"conclusion": strOrNil(r.Conclusion), "workflow_id": r.ID / 100, "check_suite_id": r.SuiteID,
		"html_url": s.runHTML(rp, r), "url": s.repoAPI(rp) + "/actions/runs/" + i64(r.ID),
		"created_at": iso(r.Created), "updated_at": iso(r.Updated), "run_started_at": iso(r.Created),
	}
}

func (s *Server) renderJob(rp *repo, r *run, j *job) map[string]any {
	return map[string]any{
		"id": j.ID, "run_id": r.ID, "run_attempt": r.Attempt, "name": j.Name, "workflow_name": r.Name,
		"head_sha": r.HeadSHA, "head_branch": r.HeadBranch, "status": j.Status, "conclusion": strOrNil(j.Conclusion),
		"html_url": s.runHTML(rp, r) + "/job/" + i64(j.ID), "url": s.repoAPI(rp) + "/actions/jobs/" + i64(j.ID),
		"started_at": isoOrNil(j.Started), "completed_at": isoOrNil(j.Completed), "steps": []any{},
	}
}

func (s *Server) renderCheckRun(rp *repo, r *run, j *job) map[string]any {
	return map[string]any{
		"id": j.ID, "node_id": nodeID("CR", j.ID), "name": j.Name, "head_sha": r.HeadSHA, "status": j.Status,
		"conclusion": strOrNil(j.Conclusion), "html_url": s.runHTML(rp, r) + "/job/" + i64(j.ID),
		"details_url": s.runHTML(rp, r) + "/job/" + i64(j.ID), "url": s.repoAPI(rp) + "/check-runs/" + i64(j.ID),
		"started_at": isoOrNil(j.Started), "completed_at": isoOrNil(j.Completed),
		"check_suite": map[string]any{"id": r.SuiteID},
		"app":         map[string]any{"id": 15368, "slug": "github-actions", "name": "GitHub Actions"},
	}
}

func itoa(n int) string  { return strconv.Itoa(n) }
func i64(n int64) string { return strconv.FormatInt(n, 10) }
