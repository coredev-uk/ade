package fake

import (
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// The Start work endpoints: creating issues, labels, links, branches and merge
// requests, reading template files, and GraphQL issueSetIteration. Writes act
// as the sandbox user.

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		const pp = "/api/v4/projects/{id}"
		mux.HandleFunc("GET "+pp, s.getProject)
		mux.HandleFunc("POST "+pp+"/issues", s.createIssue)
		mux.HandleFunc("PUT "+pp+"/issues/{iid}", s.updateIssue)
		mux.HandleFunc("POST "+pp+"/issues/{iid}/links", s.createLink)
		mux.HandleFunc("GET "+pp+"/repository/branches", s.listBranches)
		mux.HandleFunc("POST "+pp+"/repository/branches", s.createBranch)
		mux.HandleFunc("GET "+pp+"/repository/tree", s.repoTree)
		mux.HandleFunc("GET "+pp+"/repository/files/{path}/raw", s.rawFile)
		mux.HandleFunc("POST "+pp+"/merge_requests", s.createMR)
	})
}

// repos is the git side the fake emulates: branches and files per project.
// Branch names are in creation order; the first is the default branch. files
// are on every branch; overlay holds what commits added on one branch
// (key "project@branch"), as the files API writes them.
type repos struct {
	branches map[string][]string
	files    map[string]map[string]string // project path -> file path -> content
	overlay  map[string]map[string]string
}

func (s *Server) repos() *repos {
	return s.ext("issuewrite", func() any {
		r := &repos{
			branches: map[string][]string{}, files: map[string]map[string]string{},
			overlay: map[string]map[string]string{},
		}
		for _, p := range s.st.projects {
			r.branches[p.Path] = []string{"main"}
		}
		// Seeded MRs have their source branches.
		for _, m := range s.st.mrs {
			if !slices.Contains(r.branches[m.Project.Path], m.Source) {
				r.branches[m.Project.Path] = append(r.branches[m.Project.Path], m.Source)
			}
		}
		return r
	}).(*repos)
}

// view is the project's files as seen on ref: the shared files plus what was
// committed to that branch.
func (r *repos) view(project, ref string) map[string]string {
	out := maps.Clone(r.files[project])
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, r.overlay[project+"@"+ref])
	return out
}

// AddFile puts a file in the project's repository (on every branch).
func (s *Server) AddFile(project, path, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.repos()
	if r.files[project] == nil {
		r.files[project] = map[string]string{}
	}
	r.files[project][path] = content
}

// BranchNames lists the project's branches, default first.
func (s *Server) BranchNames(project string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.repos().branches[project])
}

// MRLabels are the labels on an MR (including those quick actions added).
func (s *Server) MRLabels(ref forge.Ref) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return nil, err
	}
	return slices.Clone(m.Labels), nil
}

// IssueLabels are the labels on an issue.
func (s *Server) IssueLabels(ref forge.Ref) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, err := s.issue(ref)
	if err != nil {
		return nil, err
	}
	return slices.Clone(is.Labels), nil
}

// MRReviewers are the usernames requested as reviewers on an MR.
func (s *Server) MRReviewers(ref forge.Ref) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.mr(ref)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(m.Reviewers))
	for i, u := range m.Reviewers {
		out[i] = u.Username
	}
	return out, nil
}

// lookupProject resolves {id}; it writes the 404 itself.
func (s *Server) lookupProject(w http.ResponseWriter, r *http.Request) (*project, bool) {
	p := s.st.findProject(r.PathValue("id"))
	if p == nil {
		notFound(w)
	}
	return p, p != nil
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.lookupProject(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, object{
		"id": p.ID, "path_with_namespace": p.Path, "default_branch": s.repos().branches[p.Path][0],
		"web_url": s.webURL(p.Path),
	})
}

func (s *Server) nextIssueIID(p *project) int {
	n := 0
	for _, is := range s.st.issues {
		if is.Project == p {
			n = max(n, is.IID)
		}
	}
	return n + 1
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request) {
	p := params(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	proj, ok := s.lookupProject(w, r)
	if !ok {
		return
	}
	title := str(p, "title")
	if title == "" {
		badRequest(w, "title is missing")
		return
	}
	now := s.st.clock
	is := &issue{
		ID: s.st.id(), IID: s.nextIssueIID(proj), Project: proj, Title: title, Desc: str(p, "description"),
		State: "opened", Author: s.st.me, Created: now, Updated: now,
	}
	if l := str(p, "labels"); l != "" {
		is.Labels = splitLabels(l)
	}
	if ids, _ := p["assignee_ids"].([]any); len(ids) > 0 {
		for _, raw := range ids {
			f, _ := raw.(float64)
			u := s.userByID(int64(f))
			if u == nil {
				badRequest(w, "assignee_ids is invalid")
				return
			}
			is.Assignees = append(is.Assignees, u)
		}
	}
	if id := int64(intOf(p, "milestone_id")); id != 0 {
		i := slices.IndexFunc(s.st.milestones, func(m *milestone) bool { return m.ID == id })
		if i < 0 {
			badRequest(w, "milestone_id is invalid")
			return
		}
		is.Milestone = s.st.milestones[i]
	}
	// Free ignores weight (the field does not exist there).
	if _, has := p["weight"]; has && s.tier == Premium {
		n := intOf(p, "weight")
		is.Weight = &n
	}
	s.st.issues = append(s.st.issues, is)
	writeJSON(w, http.StatusCreated, s.issueJSON(is, proj))
}

func (s *Server) userByID(id int64) *user {
	for _, u := range s.st.users {
		if u.ID == id {
			return u
		}
	}
	return nil
}

func splitLabels(s string) []string {
	var out []string
	for l := range strings.SplitSeq(s, ",") {
		if l = strings.TrimSpace(l); l != "" && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// updateIssue supports the label edits Start work needs: add_labels,
// remove_labels and a full labels replacement.
func (s *Server) updateIssue(w http.ResponseWriter, r *http.Request) {
	p := params(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	is, ok := s.lookupIssue(w, r)
	if !ok {
		return
	}
	if _, has := p["labels"]; has {
		is.Labels = splitLabels(str(p, "labels"))
	}
	for _, l := range splitLabels(str(p, "add_labels")) {
		if !slices.Contains(is.Labels, l) {
			is.Labels = append(is.Labels, l)
		}
	}
	for _, l := range splitLabels(str(p, "remove_labels")) {
		is.Labels = slices.DeleteFunc(is.Labels, func(x string) bool { return x == l })
	}
	if raw, has := p["assignee_ids"]; has {
		ids, _ := raw.([]any)
		var users []*user
		for _, r := range ids {
			f, _ := r.(float64)
			u := s.userByID(int64(f))
			if u == nil {
				badRequest(w, "assignee_ids is invalid")
				return
			}
			users = append(users, u)
		}
		is.Assignees = users
	}
	if _, has := p["milestone_id"]; has {
		if id := int64(intOf(p, "milestone_id")); id == 0 {
			is.Milestone = nil
		} else if i := slices.IndexFunc(s.st.milestones, func(m *milestone) bool { return m.ID == id }); i >= 0 {
			is.Milestone = s.st.milestones[i]
		} else {
			badRequest(w, "milestone_id is invalid")
			return
		}
	}
	is.Updated = s.st.clock
	writeJSON(w, http.StatusOK, s.issueJSON(is, is.Project))
}

func (s *Server) createLink(w http.ResponseWriter, r *http.Request) {
	p := params(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.lookupIssue(w, r)
	if !ok {
		return
	}
	tp := s.st.findProject(str(p, "target_project_id"))
	var b *issue
	if tp != nil {
		b = s.st.findIssue(tp, intOf(p, "target_issue_iid"))
	}
	if b == nil {
		notFound(w)
		return
	}
	typ := str(p, "link_type")
	if typ == "" {
		typ = "relates_to"
	}
	if typ != "relates_to" && s.tier != Premium {
		forbidden(w) // blocks / is_blocked_by are licensed
		return
	}
	for _, l := range s.st.links {
		if (l.A == a && l.B == b) || (l.A == b && l.B == a) {
			writeJSON(w, http.StatusConflict, object{"message": "Issue(s) already assigned"})
			return
		}
	}
	l := &link{ID: s.st.id(), A: a, B: b, Type: typ, Created: s.st.clock}
	s.st.links = append(s.st.links, l)
	a.Updated, b.Updated = s.st.clock, s.st.clock
	writeJSON(w, http.StatusCreated, object{
		"source_issue": s.issueJSON(a, a.Project), "target_issue": s.issueJSON(b, a.Project), "link_type": typ,
	})
}

func (s *Server) listBranches(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.lookupProject(w, r)
	if !ok {
		return
	}
	search := r.URL.Query().Get("search")
	var out []string
	for _, b := range s.repos().branches[p.Path] {
		switch {
		case search == "":
		case strings.HasPrefix(search, "^"):
			if !strings.HasPrefix(b, strings.TrimPrefix(search, "^")) {
				continue
			}
		case !strings.Contains(b, search):
			continue
		}
		out = append(out, b)
	}
	paged(s, w, r, out, func(b string) any { return s.branchJSON(p, b) })
}

func (s *Server) branchJSON(p *project, name string) object {
	return object{
		"name": name, "default": name == s.repos().branches[p.Path][0], "protected": false, "merged": false,
		"web_url": s.webURL(p.Path + "/-/tree/" + name),
		"commit":  object{"id": branchSHA(p.Path, name), "short_id": branchSHA(p.Path, name)[:8]},
	}
}

// branchSHA is a stable 40-hex stand-in for a branch head.
func branchSHA(project, name string) string {
	h := uint64(14695981039346656037)
	for _, c := range []byte(project + "@" + name) {
		h = (h ^ uint64(c)) * 1099511628211
	}
	return strings.Repeat(fmt.Sprintf("%08x", h&0xffffffff), 5)
}

func (s *Server) createBranch(w http.ResponseWriter, r *http.Request) {
	p := params(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	proj, ok := s.lookupProject(w, r)
	if !ok {
		return
	}
	rs := s.repos()
	name, from := str(p, "branch"), str(p, "ref")
	switch {
	case name == "" || from == "":
		badRequest(w, "branch and ref are required")
	case slices.Contains(rs.branches[proj.Path], name):
		writeJSON(w, http.StatusBadRequest, object{"message": "Branch already exists"})
	case !slices.Contains(rs.branches[proj.Path], from):
		writeJSON(w, http.StatusBadRequest, object{"message": "Invalid reference name: " + from})
	default:
		if err := s.createRemoteBranch(proj.Path, name, from); err != nil {
			writeJSON(w, http.StatusInternalServerError, object{"message": err.Error()})
			return
		}
		rs.branches[proj.Path] = append(rs.branches[proj.Path], name)
		if o := rs.overlay[proj.Path+"@"+from]; o != nil {
			rs.overlay[proj.Path+"@"+name] = maps.Clone(o)
		}
		writeJSON(w, http.StatusCreated, s.branchJSON(proj, name))
	}
}

func (s *Server) repoTree(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.lookupProject(w, r)
	if !ok {
		return
	}
	rs := s.repos()
	if ref := r.URL.Query().Get("ref"); ref != "" && !slices.Contains(rs.branches[p.Path], ref) {
		notFound(w)
		return
	}
	dir := strings.Trim(r.URL.Query().Get("path"), "/")
	var out []string
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = rs.branches[p.Path][0]
	}
	for path := range rs.view(p.Path, ref) {
		if d, _ := splitDir(path); d == dir {
			out = append(out, path)
		}
	}
	if len(out) == 0 && dir != "" {
		notFound(w) // GitLab: 404 Tree Not Found
		return
	}
	slices.Sort(out)
	paged(s, w, r, out, func(path string) any {
		_, name := splitDir(path)
		return object{"id": branchSHA(p.Path, path), "name": name, "type": "blob", "path": path, "mode": "100644"}
	})
}

func splitDir(path string) (dir, name string) {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i], path[i+1:]
	}
	return "", path
}

func (s *Server) rawFile(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.lookupProject(w, r)
	if !ok {
		return
	}
	rs := s.repos()
	ref := r.URL.Query().Get("ref")
	content, has := rs.view(p.Path, ref)[r.PathValue("path")]
	if !has || !slices.Contains(rs.branches[p.Path], ref) {
		notFound(w)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(content))
}

func (s *Server) createMR(w http.ResponseWriter, r *http.Request) {
	p := params(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	proj, ok := s.lookupProject(w, r)
	if !ok {
		return
	}
	rs := s.repos()
	source, target, title := str(p, "source_branch"), str(p, "target_branch"), str(p, "title")
	switch {
	case source == "" || target == "" || title == "":
		badRequest(w, "source_branch, target_branch and title are required")
		return
	case !slices.Contains(rs.branches[proj.Path], source) || !slices.Contains(rs.branches[proj.Path], target):
		badRequest(w, "source or target branch does not exist")
		return
	}
	for _, o := range s.st.mrs {
		if o.Project == proj && o.State == "opened" && o.Source == source && o.Target == target {
			writeJSON(w, http.StatusConflict, object{"message": []string{
				fmt.Sprintf("Another open merge request already exists for this source branch: !%d", o.IID),
			}})
			return
		}
	}
	iid := 0
	for _, o := range s.st.mrs {
		if o.Project == proj {
			iid = max(iid, o.IID)
		}
	}
	now := s.st.clock
	m := &mr{
		ID: s.st.id(), IID: iid + 1, Project: proj, Title: title, State: "opened", Draft: hasDraftPrefix(title),
		Source: source, Target: target, SHA: branchSHA(proj.Path, source), Author: s.st.me,
		Assignees: nil, Created: now, Updated: now,
	}
	if l := str(p, "labels"); l != "" {
		m.Labels = splitLabels(l)
	}
	if ids, _ := p["reviewer_ids"].([]any); len(ids) > 0 {
		for _, raw := range ids {
			f, _ := raw.(float64)
			u := s.userByID(int64(f))
			if u == nil {
				badRequest(w, "reviewer_ids is invalid")
				return
			}
			m.Reviewers = append(m.Reviewers, u)
		}
	}
	m.Desc = s.runQuickActions(m, str(p, "description"))
	s.linkReferences(m)
	s.st.mrs = append(s.st.mrs, m)
	writeJSON(w, http.StatusCreated, s.mrJSON(m, proj, true))
}

// quickActionRe matches a quick-action line: "/name args".
var quickActionRe = regexp.MustCompile(`^/(\w+)(?:\s+(.*))?$`)

// runQuickActions executes the quick actions GitLab runs when a description is
// saved (label, assign, assign_reviewer/reviewer, unassign are emulated) and
// returns the description without them, as GitLab stores it.
func (s *Server) runQuickActions(m *mr, desc string) string {
	var kept []string
	fenced := false
	for line := range strings.SplitSeq(desc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		mt := quickActionRe.FindStringSubmatch(strings.TrimSpace(line))
		if fenced || mt == nil {
			kept = append(kept, line)
			continue
		}
		args := mt[2]
		switch mt[1] {
		case "label":
			for _, l := range quickActionLabels(args) {
				if !slices.Contains(m.Labels, l) {
					m.Labels = append(m.Labels, l)
				}
			}
		case "assign":
			m.Assignees = append(m.Assignees, s.usersNamed(args)...)
		case "assign_reviewer", "reviewer":
			for _, u := range s.usersNamed(args) {
				if !slices.Contains(m.Reviewers, u) {
					m.Reviewers = append(m.Reviewers, u)
				}
			}
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

var labelArgRe = regexp.MustCompile(`~"([^"]+)"|~(\S+)`)

func quickActionLabels(args string) []string {
	var out []string
	for _, m := range labelArgRe.FindAllStringSubmatch(args, -1) {
		out = append(out, m[1]+m[2])
	}
	return out
}

func (s *Server) usersNamed(args string) []*user {
	var out []*user
	for f := range strings.FieldsSeq(args) {
		if u := s.st.findUser(strings.TrimPrefix(f, "@")); u != nil {
			out = append(out, u)
		}
	}
	return out
}

var (
	closesRe  = regexp.MustCompile(`(?i)\b(?:closes?|fix(?:es)?|resolves?)\s+([\w./-]*)#(\d+)`)
	mentionRe = regexp.MustCompile(`([\w./-]*)#(\d+)`)
)

// linkReferences fills Closes and Mentions from issue references in the
// description; a bare "#N" is the MR's own project.
func (s *Server) linkReferences(m *mr) {
	find := func(path, num string) *issue {
		if path == "" {
			path = m.Project.Path
		}
		iid, _ := strconv.Atoi(num)
		if p := s.st.findProject(path); p != nil {
			return s.st.findIssue(p, iid)
		}
		return nil
	}
	for _, c := range closesRe.FindAllStringSubmatch(m.Desc, -1) {
		if is := find(c[1], c[2]); is != nil && !slices.Contains(m.Closes, is) {
			m.Closes = append(m.Closes, is)
		}
	}
	for _, c := range mentionRe.FindAllStringSubmatch(m.Desc, -1) {
		if is := find(c[1], c[2]); is != nil && !slices.Contains(m.Mentions, is) && !slices.Contains(m.Closes, is) {
			m.Mentions = append(m.Mentions, is)
		}
	}
}

// setIterationMutation answers issueSetIteration. Free accepts the mutation and
// ignores it: no errors, no iteration (verified live on a gitlab.com Free
// group), which is why callers gate on the issue carrying an iteration at all.
func (s *Server) setIterationMutation(w http.ResponseWriter, vars map[string]any) {
	if s.tier != Premium {
		iid := fmt.Sprint(vars["iid"])
		writeJSON(w, http.StatusOK, object{"data": object{"issueSetIteration": object{"errors": []string{}, "issue": object{"iid": iid}}}})
		return
	}
	fail := func(msg string) {
		writeJSON(w, http.StatusOK, object{"data": object{"issueSetIteration": object{"errors": []string{msg}, "issue": nil}}})
	}
	p := s.st.findProject(fmt.Sprint(vars["projectPath"]))
	iid, _ := strconv.Atoi(fmt.Sprint(vars["iid"]))
	if p == nil || s.st.findIssue(p, iid) == nil {
		fail("The resource that you are attempting to access does not exist or you don't have permission to perform this action")
		return
	}
	is := s.st.findIssue(p, iid)
	gid, _ := vars["iterationId"].(string)
	id, _ := strconv.ParseInt(strings.TrimPrefix(gid, "gid://gitlab/Iteration/"), 10, 64)
	i := slices.IndexFunc(s.st.iterations, func(it *iteration) bool { return it.ID == id })
	if i < 0 {
		fail("Iteration not found")
		return
	}
	is.Iteration, is.Updated = s.st.iterations[i], s.st.clock
	writeJSON(w, http.StatusOK, object{"data": object{"issueSetIteration": object{"errors": []string{}, "issue": object{"iid": strconv.Itoa(iid)}}}})
}
