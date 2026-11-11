package fake

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The GraphQL endpoint is not a general engine: it dispatches on the
// operation name of the documents ADE sends and answers with the same
// nesting and field names GitHub's schema gives them.

var (
	opRE    = regexp.MustCompile(`(?:query|mutation)\s+(\w+)`)
	firstRE = regexp.MustCompile(`reviewThreads\(\s*first:\s*(\d+)`)
)

// gqlResult is one operation's outcome: a data object and/or errors.
type gqlResult struct {
	data   map[string]any
	errors []map[string]any
}

func gqlError(typ string, path []any, msg string) map[string]any {
	return map[string]any{"type": typ, "path": path, "message": msg}
}

func (s *Server) graphql(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if !decode(w, r, &req) {
		return
	}
	ops := map[string]func(*http.Request, string, map[string]any) gqlResult{
		"ReviewThreads":        s.opReviewThreads,
		"IssuePRs":             s.opIssuePRs,
		"ResolveThread":        s.opResolveThread,
		"UnresolveThread":      s.opResolveThread,
		"AddReviewThread":      s.opAddReviewThread,
		"AddReviewThreadReply": s.opAddReviewThreadReply,
		"MarkReady":            s.opSetDraft,
		"ConvertToDraft":       s.opSetDraft,
		"EnableAutoMerge":      s.opEnableAutoMerge,
	}
	m := opRE.FindStringSubmatch(req.Query)
	var op func(*http.Request, string, map[string]any) gqlResult
	if m != nil {
		op = ops[m[1]]
	}
	if op == nil {
		writeJSON(w, http.StatusOK, map[string]any{"errors": []any{map[string]any{"message": "unsupported operation"}}})
		return
	}
	s.mu.Lock()
	res := op(r, req.Query, req.Variables)
	s.mu.Unlock()
	out := map[string]any{"data": res.data}
	if len(res.errors) > 0 {
		out["errors"] = res.errors
	}
	writeJSON(w, http.StatusOK, out)
}

func varStr(v map[string]any, k string) string {
	s, _ := v[k].(string)
	return s
}

func varInt(v map[string]any, k string) int {
	n, _ := v[k].(float64)
	return int(n)
}

func varInput(v map[string]any) map[string]any {
	in, _ := v["input"].(map[string]any)
	return in
}

func encodeCursor(i int) string {
	return base64.StdEncoding.EncodeToString([]byte("cursor:" + strconv.Itoa(i)))
}

func decodeCursor(c string) int {
	b, err := base64.StdEncoding.DecodeString(c)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimPrefix(string(b), "cursor:"))
	if err != nil {
		return -1
	}
	return n
}

func (s *Server) gqlRepo(vars map[string]any) (*repo, *gqlResult) {
	owner, name := varStr(vars, "owner"), varStr(vars, "name")
	rp := s.st.repo(owner, name)
	if rp != nil {
		return rp, nil
	}
	return nil, &gqlResult{
		data:   map[string]any{"repository": nil},
		errors: []map[string]any{gqlError("NOT_FOUND", []any{"repository"}, fmt.Sprintf("Could not resolve to a Repository with the name '%s/%s'.", owner, name))},
	}
}

func (s *Server) gqlAuthor(login string) any {
	if login == "" {
		return nil
	}
	return map[string]any{"login": login}
}

func (s *Server) gqlComment(c *reviewComment) map[string]any {
	return map[string]any{
		"id": nodeID("PRRC", c.ID), "databaseId": c.ID, "body": c.Body, "createdAt": iso(c.Created),
		"author": s.gqlAuthor(c.User), "pullRequestReview": map[string]any{"state": c.Review.State},
	}
}

func (s *Server) gqlThread(t *thread) map[string]any {
	nodes := make([]any, 0, len(t.Comments))
	for _, c := range t.visible() {
		nodes = append(nodes, s.gqlComment(c))
	}
	var start any
	if t.StartLn > 0 {
		start = t.StartLn
	}
	return map[string]any{
		"id": nodeID("PRRT", t.ID), "isResolved": t.Resolved, "isOutdated": false, "path": t.Path,
		"line": t.Line, "originalLine": t.Line, "startLine": start, "diffSide": t.Side,
		"comments": map[string]any{"nodes": nodes},
	}
}

func (s *Server) opReviewThreads(_ *http.Request, query string, vars map[string]any) gqlResult {
	rp, fail := s.gqlRepo(vars)
	if fail != nil {
		return *fail
	}
	n := varInt(vars, "number")
	it := rp.item(n)
	if it == nil || it.PR == nil {
		return gqlResult{
			data:   map[string]any{"repository": map[string]any{"pullRequest": nil}},
			errors: []map[string]any{gqlError("NOT_FOUND", []any{"repository", "pullRequest"}, fmt.Sprintf("Could not resolve to a PullRequest with the number of %d.", n))},
		}
	}
	var threads []*thread
	for _, t := range it.PR.Threads {
		if len(t.visible()) > 0 {
			threads = append(threads, t)
		}
	}
	first := 100
	if m := firstRE.FindStringSubmatch(query); m != nil {
		first, _ = strconv.Atoi(m[1])
	}
	first = max(1, min(first, 100, s.opts.pageSize))
	start := 0
	if after := varStr(vars, "after"); after != "" {
		start = decodeCursor(after) + 1
	}
	start = max(0, min(start, len(threads)))
	end := min(start+first, len(threads))
	nodes := make([]any, 0, end-start)
	for _, t := range threads[start:end] {
		nodes = append(nodes, s.gqlThread(t))
	}
	var endCursor any
	if end > start {
		endCursor = encodeCursor(end - 1)
	}
	return gqlResult{data: map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
		"id": nodeID("PR", it.ID),
		"reviewThreads": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": end < len(threads), "endCursor": endCursor},
			"nodes":    nodes,
		},
	}}}}
}

func (s *Server) opIssuePRs(_ *http.Request, _ string, vars map[string]any) gqlResult {
	rp, fail := s.gqlRepo(vars)
	if fail != nil {
		return *fail
	}
	n := varInt(vars, "number")
	it := rp.item(n)
	if it == nil || it.PR != nil {
		return gqlResult{
			data:   map[string]any{"repository": map[string]any{"issue": nil}},
			errors: []map[string]any{gqlError("NOT_FOUND", []any{"repository", "issue"}, fmt.Sprintf("Could not resolve to an Issue with the number of %d.", n))},
		}
	}
	target := itemKey{rp.Name, n}
	closedBy, cross := []any{}, []any{}
	for _, other := range s.st.repos {
		repoRef := map[string]any{"nameWithOwner": other.Owner + "/" + other.Name}
		for _, o := range other.Items {
			if (itemKey{other.Name, o.Number}) == target {
				continue
			}
			if o.PR != nil && slices.Contains(s.st.refsIn(closingRE, other, o.Body), target) {
				closedBy = append(closedBy, map[string]any{"number": o.Number, "repository": repoRef})
			}
			if !slices.Contains(s.st.refsIn(mentionRE, other, o.Body), target) {
				continue
			}
			src := map[string]any{"__typename": "Issue"}
			if o.PR != nil {
				src = map[string]any{"__typename": "PullRequest", "number": o.Number, "repository": repoRef}
			}
			cross = append(cross, map[string]any{"__typename": "CrossReferencedEvent", "source": src})
		}
	}
	return gqlResult{data: map[string]any{"repository": map[string]any{"issue": map[string]any{
		"closedByPullRequestsReferences": map[string]any{"nodes": closedBy},
		"timelineItems":                  map[string]any{"nodes": cross},
	}}}}
}

// gqlFail is a mutation that returned null data plus one error.
func gqlFail(field, typ, msg string) gqlResult {
	return gqlResult{
		data:   map[string]any{field: nil},
		errors: []map[string]any{gqlError(typ, []any{field}, msg)},
	}
}

func notResolved(field, kind, id string) gqlResult {
	return gqlFail(field, "NOT_FOUND", fmt.Sprintf("Could not resolve to a %s with the id of '%s'.", kind, id))
}

// findThread locates a visible thread by node id across the owner's PRs.
func (s *Server) findThread(id string) (*repo, *item, *thread) {
	tid, ok := parseNode("PRRT", id)
	if !ok {
		return nil, nil, nil
	}
	for _, rp := range s.st.repos {
		for _, it := range rp.Items {
			if it.PR != nil {
				if t := it.PR.thread(tid); t != nil {
					return rp, it, t
				}
			}
		}
	}
	return nil, nil, nil
}

// findPendingReview locates the viewer's pending review by node id.
func (s *Server) findPendingReview(id string) (*item, *review) {
	rid, ok := parseNode("PRR", id)
	if !ok {
		return nil, nil
	}
	for _, rp := range s.st.repos {
		for _, it := range rp.Items {
			if it.PR == nil {
				continue
			}
			if rv := it.PR.review(rid); rv != nil {
				return it, rv
			}
		}
	}
	return nil, nil
}

func (s *Server) opResolveThread(_ *http.Request, query string, vars map[string]any) gqlResult {
	field, resolved := "resolveReviewThread", true
	if strings.Contains(query, "unresolveReviewThread") {
		field, resolved = "unresolveReviewThread", false
	}
	id := varStr(vars, "id")
	_, it, t := s.findThread(id)
	if t == nil {
		return notResolved(field, "PullRequestReviewThread", id)
	}
	t.Resolved = resolved
	it.Updated = s.st.now()
	return gqlResult{data: map[string]any{field: map[string]any{"thread": map[string]any{"id": id, "isResolved": t.Resolved}}}}
}

func (s *Server) opAddReviewThread(_ *http.Request, _ string, vars map[string]any) gqlResult {
	const field = "addPullRequestReviewThread"
	in := varInput(vars)
	rvID := varStr(in, "pullRequestReviewId")
	it, rv := s.findPendingReview(rvID)
	if rv == nil {
		return notResolved(field, "PullRequestReview", rvID)
	}
	side := varStr(in, "side")
	if side == "" {
		side = "RIGHT"
	}
	line := varInt(in, "line")
	switch {
	case rv.State != statePending || rv.User != viewer:
		return gqlFail(field, "UNPROCESSABLE", "Review must be a pending review of the viewer")
	case side != "LEFT" && side != "RIGHT":
		return gqlFail(field, "UNPROCESSABLE", "Side must be LEFT or RIGHT")
	case varStr(in, "body") == "":
		return gqlFail(field, "UNPROCESSABLE", "Body can't be blank")
	}
	switch it.PR.anchorProblem(varStr(in, "path"), line, side) {
	case "path":
		return gqlFail(field, "UNPROCESSABLE", "Path is not part of the diff")
	case "line":
		return gqlFail(field, "UNPROCESSABLE", "Line must be part of the diff")
	}
	at := s.st.now()
	c := s.st.newThread(it.PR, rv, varStr(in, "path"), line, varInt(in, "startLine"), side, varStr(in, "body"), at)
	t := it.PR.Threads[len(it.PR.Threads)-1]
	it.Updated = at
	return gqlResult{data: map[string]any{field: map[string]any{"thread": map[string]any{
		"id":       nodeID("PRRT", t.ID),
		"comments": map[string]any{"nodes": []any{map[string]any{"databaseId": c.ID}}},
	}}}}
}

func (s *Server) opAddReviewThreadReply(_ *http.Request, _ string, vars map[string]any) gqlResult {
	const field = "addPullRequestReviewThreadReply"
	in := varInput(vars)
	threadID := varStr(in, "pullRequestReviewThreadId")
	_, it, t := s.findThread(threadID)
	if t == nil {
		return notResolved(field, "PullRequestReviewThread", threadID)
	}
	if varStr(in, "body") == "" {
		return gqlFail(field, "UNPROCESSABLE", "Body can't be blank")
	}
	at := s.st.now()
	var rv *review
	if rvID := varStr(in, "pullRequestReviewId"); rvID != "" {
		rvItem, found := s.findPendingReview(rvID)
		switch {
		case found == nil:
			return notResolved(field, "PullRequestReview", rvID)
		case found.State != statePending || found.User != viewer || rvItem != it:
			return gqlFail(field, "UNPROCESSABLE", "Review must be a pending review of this pull request")
		}
		rv = found
	} else {
		rv = s.st.addReview(it.PR, viewer, stateCommented, "", at)
	}
	c := &reviewComment{User: viewer, Body: varStr(in, "body"), Review: rv, Created: at, Updated: at}
	s.st.appendToThread(t, c)
	it.Updated = at
	return gqlResult{data: map[string]any{field: map[string]any{"comment": map[string]any{
		"id": nodeID("PRRC", c.ID), "databaseId": c.ID, "body": c.Body, "createdAt": iso(c.Created),
		"author": s.gqlAuthor(c.User),
	}}}}
}

// findPull locates a PR by node id.
func (s *Server) findPull(id string) (*repo, *item) {
	pid, ok := parseNode("PR", id)
	if !ok {
		return nil, nil
	}
	rp, it := s.st.itemByID(pid)
	if it == nil || it.PR == nil {
		return nil, nil
	}
	return rp, it
}

func (s *Server) opSetDraft(_ *http.Request, query string, vars map[string]any) gqlResult {
	field, toDraft := "markPullRequestReadyForReview", false
	if strings.Contains(query, "convertPullRequestToDraft") {
		field, toDraft = "convertPullRequestToDraft", true
	}
	id := varStr(vars, "id")
	_, it := s.findPull(id)
	switch {
	case it == nil:
		return notResolved(field, "PullRequest", id)
	case it.State != "open":
		return gqlFail(field, "UNPROCESSABLE", "Pull request is closed")
	case it.PR.Draft == toDraft && toDraft:
		return gqlFail(field, "UNPROCESSABLE", "Pull request is already a draft")
	case it.PR.Draft == toDraft:
		return gqlFail(field, "UNPROCESSABLE", "Pull request is not a draft")
	}
	at := s.st.now()
	it.PR.Draft = toDraft
	it.Updated = at
	event := "ready_for_review"
	if toDraft {
		event = "convert_to_draft"
	}
	s.st.addEvent(it, event, viewer, at)
	return gqlResult{data: map[string]any{field: map[string]any{"pullRequest": map[string]any{"id": id, "isDraft": it.PR.Draft}}}}
}

func (s *Server) opEnableAutoMerge(_ *http.Request, _ string, vars map[string]any) gqlResult {
	const field = "enablePullRequestAutoMerge"
	in := varInput(vars)
	id := varStr(in, "pullRequestId")
	_, it := s.findPull(id)
	method := varStr(in, "mergeMethod")
	if method == "" {
		method = "MERGE"
	}
	oid := varStr(in, "expectedHeadOid")
	switch {
	case it == nil:
		return notResolved(field, "PullRequest", id)
	case !slices.Contains([]string{"MERGE", "SQUASH", "REBASE"}, method):
		return gqlFail(field, "UNPROCESSABLE", "Merge method must be MERGE, SQUASH or REBASE")
	case it.State != "open":
		return gqlFail(field, "UNPROCESSABLE", "Pull request is closed")
	case it.PR.Draft:
		return gqlFail(field, "UNPROCESSABLE", "Pull request is still a draft")
	case oid != "" && oid != it.PR.HeadSHA:
		return gqlFail(field, "UNPROCESSABLE", "Expected head OID does not match the pull request head")
	}
	at := s.st.now()
	it.PR.AutoMerge = &autoMerge{Method: method, Enabled: at}
	params := map[string]any{"merge_method": strings.ToLower(method)}
	if oid != "" {
		params["sha"] = oid
	}
	it.PR.MergeParams = params
	it.Updated = at
	return gqlResult{data: map[string]any{field: map[string]any{"pullRequest": map[string]any{
		"id": id, "autoMergeRequest": map[string]any{"enabledAt": iso(at)},
	}}}}
}
