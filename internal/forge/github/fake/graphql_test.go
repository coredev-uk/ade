package fake_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge/github/fake"
)

const (
	qThreads = `query ReviewThreads($owner:String!,$name:String!,$number:Int!,$after:String){ repository(owner:$owner,name:$name){ pullRequest(number:$number){ id reviewThreads(first:100, after:$after){ pageInfo{hasNextPage endCursor} nodes{ id isResolved isOutdated path line originalLine startLine diffSide comments(first:100){ nodes{ id databaseId body createdAt author{login} pullRequestReview{ state } } } } } } } }`

	qIssuePRs = `query IssuePRs($owner:String!,$name:String!,$number:Int!){ repository(owner:$owner,name:$name){ issue(number:$number){ closedByPullRequestsReferences(first:50, includeClosedPrs:true){ nodes{ number repository{ nameWithOwner } } } timelineItems(first:100, itemTypes:[CROSS_REFERENCED_EVENT]){ nodes{ __typename ... on CrossReferencedEvent{ source{ __typename ... on PullRequest{ number repository{ nameWithOwner } } } } } } } } }`

	mResolve   = `mutation ResolveThread($id:ID!){ resolveReviewThread(input:{threadId:$id}){ thread{ id isResolved } } }`
	mUnresolve = `mutation UnresolveThread($id:ID!){ unresolveReviewThread(input:{threadId:$id}){ thread{ id isResolved } } }`
	mAddThread = `mutation AddReviewThread($input:AddPullRequestReviewThreadInput!){ addPullRequestReviewThread(input:$input){ thread{ id comments(first:1){ nodes{ databaseId } } } } }`
	mReply     = `mutation AddReviewThreadReply($input:AddPullRequestReviewThreadReplyInput!){ addPullRequestReviewThreadReply(input:$input){ comment{ id databaseId body createdAt author{login} } } }`
	mReady     = `mutation MarkReady($id:ID!){ markPullRequestReadyForReview(input:{pullRequestId:$id}){ pullRequest{ id isDraft } } }`
	mDraft     = `mutation ConvertToDraft($id:ID!){ convertPullRequestToDraft(input:{pullRequestId:$id}){ pullRequest{ id isDraft } } }`
	mAuto      = `mutation EnableAutoMerge($input:EnablePullRequestAutoMergeInput!){ enablePullRequestAutoMerge(input:$input){ pullRequest{ id autoMergeRequest{ enabledAt } } } }`
)

// gql posts a GraphQL document and returns the whole envelope.
func gql(t *testing.T, s *fake.Server, query string, vars map[string]any) map[string]any {
	t.Helper()
	r := expect(t, call(t, s, "POST", "/api/graphql", map[string]any{"query": query, "variables": vars}), 200)
	return r.obj(t)
}

// gqlOK is gql that also demands the absence of errors.
func gqlOK(t *testing.T, s *fake.Server, query string, vars map[string]any) map[string]any {
	t.Helper()
	out := gql(t, s, query, vars)
	if out["errors"] != nil {
		t.Fatalf("unexpected GraphQL errors: %v", out["errors"])
	}
	return at(t, out, "data").(map[string]any)
}

func threadVars(repo string, n int) map[string]any {
	return map[string]any{"owner": "ade-demo", "name": repo, "number": n}
}

// threads returns the PR's review thread nodes from the first page.
func threads(t *testing.T, s *fake.Server, repo string, n int) []any {
	t.Helper()
	d := gqlOK(t, s, qThreads, threadVars(repo, n))
	return at(t, d, "repository", "pullRequest", "reviewThreads", "nodes").([]any)
}

func threadByPath(t *testing.T, nodes []any, path string, line int) map[string]any {
	t.Helper()
	for _, n := range nodes {
		if str(t, n, "path") == path && num(t, n, "line") == line {
			return n.(map[string]any)
		}
	}
	t.Fatalf("no thread at %s:%d in %v", path, line, nodes)
	return nil
}

func commentTexts(t *testing.T, th any) []string {
	t.Helper()
	return pluck[string](t, at(t, th, "comments", "nodes").([]any), "body")
}

func TestReviewThreadsQuery(t *testing.T) {
	s := newServer(t)
	nodes := threads(t, s, "api", 2)
	if len(nodes) != 3 {
		t.Fatalf("api#2 threads = %d, want 3 (the other user's pending thread stays hidden)", len(nodes))
	}
	pr := at(t, gqlOK(t, s, qThreads, threadVars("api", 2)), "repository", "pullRequest")
	if str(t, pr, "id") != str(t, get(t, s, api+"/pulls/2").obj(t), "node_id") {
		t.Errorf("GraphQL PR id %v != REST node_id", str(t, pr, "id"))
	}

	t1 := threadByPath(t, nodes, "limit/bucket.go", 6)
	if t1["isResolved"] != false || t1["isOutdated"] != false || str(t, t1, "diffSide") != "RIGHT" || num(t, t1, "originalLine") != 6 || t1["startLine"] != nil {
		t.Errorf("T1 = %v", t1)
	}
	texts := commentTexts(t, t1)
	if len(texts) != 2 || texts[0] != "Is a burst of 50 too high for small tenants? Support suggested 20." || !strings.HasPrefix(texts[1], "Good point") {
		t.Errorf("T1 comments = %v", texts)
	}
	authors := pluck[string](t, at(t, t1, "comments", "nodes").([]any), "author", "login")
	if !slices.Equal(authors, []string{"teammate", "demo-user"}) {
		t.Errorf("T1 authors = %v", authors)
	}
	first := at(t, t1, "comments", "nodes", 0)
	if !strings.HasPrefix(str(t, first, "id"), "PRRC_") || num(t, first, "databaseId") == 0 || str(t, first, "createdAt") == "" || str(t, first, "pullRequestReview", "state") != "COMMENTED" {
		t.Errorf("T1 comment = %v", first)
	}
	if !strings.HasPrefix(str(t, t1, "id"), "PRRT_") {
		t.Errorf("thread id = %v", t1["id"])
	}
	t2 := threadByPath(t, nodes, "limit/bucket.go", 3)
	if t2["isResolved"] != false || !strings.HasPrefix(commentTexts(t, t2)[0], "Please add a test for the 429 path") {
		t.Errorf("T2 = %v", t2)
	}
	t3 := threadByPath(t, nodes, "limit/bucket.go", 5)
	if t3["isResolved"] != true || !strings.HasPrefix(commentTexts(t, t3)[0], "Nit: rename Bucket.Rate") {
		t.Errorf("T3 = %v", t3)
	}
	// The databaseId of each comment matches the REST review comment id.
	rest := get(t, s, api+"/pulls/2/comments").arr(t)
	var dbIDs []int
	for _, th := range nodes {
		for _, c := range at(t, th, "comments", "nodes").([]any) {
			dbIDs = append(dbIDs, num(t, c, "databaseId"))
		}
	}
	var restIDs []int
	for _, c := range rest {
		restIDs = append(restIDs, num(t, c, "id"))
	}
	slices.Sort(dbIDs)
	slices.Sort(restIDs)
	if !slices.Equal(dbIDs, restIDs) || len(dbIDs) != 4 {
		t.Errorf("GraphQL comment ids %v != REST ids %v", dbIDs, restIDs)
	}

	if got := threads(t, s, "api", 4); len(got) != 0 {
		t.Errorf("api#4 threads = %v", got)
	}

	t.Run("pagination honours the page size", func(t *testing.T) {
		s := newServer(t, fake.WithPageSize(2))
		d := gqlOK(t, s, qThreads, threadVars("api", 2))
		rt := at(t, d, "repository", "pullRequest", "reviewThreads")
		if len(at(t, rt, "nodes").([]any)) != 2 || at(t, rt, "pageInfo", "hasNextPage") != true || str(t, rt, "pageInfo", "endCursor") == "" {
			t.Fatalf("page 1 = %v", rt)
		}
		vars := threadVars("api", 2)
		vars["after"] = str(t, rt, "pageInfo", "endCursor")
		rt2 := at(t, gqlOK(t, s, qThreads, vars), "repository", "pullRequest", "reviewThreads")
		if len(at(t, rt2, "nodes").([]any)) != 1 || at(t, rt2, "pageInfo", "hasNextPage") != false {
			t.Fatalf("page 2 = %v", rt2)
		}
		all := append(at(t, rt, "nodes").([]any), at(t, rt2, "nodes").([]any)...)
		ids := pluck[string](t, all, "id")
		slices.Sort(ids)
		if len(slices.Compact(ids)) != 3 {
			t.Errorf("threads across pages = %v", ids)
		}
	})

	t.Run("not found", func(t *testing.T) {
		out := gql(t, s, qThreads, threadVars("api", 99))
		if at(t, out, "data", "repository", "pullRequest") != nil {
			t.Errorf("data = %v", out["data"])
		}
		if str(t, out, "errors", 0, "type") != "NOT_FOUND" || str(t, out, "errors", 0, "message") != "Could not resolve to a PullRequest with the number of 99." || str(t, out, "errors", 0, "path", 1) != "pullRequest" {
			t.Errorf("errors = %v", out["errors"])
		}
		// An issue number is not a PR either.
		if out := gql(t, s, qThreads, threadVars("api", 1)); str(t, out, "errors", 0, "type") != "NOT_FOUND" {
			t.Errorf("issue as PR = %v", out)
		}
		out = gql(t, s, qThreads, map[string]any{"owner": "ade-demo", "name": "none", "number": 1})
		if at(t, out, "data", "repository") != nil || str(t, out, "errors", 0, "type") != "NOT_FOUND" {
			t.Errorf("unknown repo = %v", out)
		}
	})
}

func TestGraphQLProtocol(t *testing.T) {
	s := newServer(t)
	out := gql(t, s, `query Nonsense { viewer { login } }`, nil)
	if str(t, out, "errors", 0, "message") != "unsupported operation" {
		t.Errorf("unknown op = %v", out)
	}
	if out := gql(t, s, `{ viewer { login } }`, nil); str(t, out, "errors", 0, "message") != "unsupported operation" {
		t.Errorf("anonymous op = %v", out)
	}
	if r := doAs(t, s, "", "POST", "/api/graphql", map[string]any{"query": qThreads}); r.code != 401 {
		t.Errorf("unauthenticated = %d", r.code)
	}
	expect(t, call(t, s, "POST", "/api/graphql", "{oops"), 400)
	// GraphQL is not under /api/v3 on GHES.
	expect(t, call(t, s, "POST", apiRoot+"/graphql", map[string]any{"query": qThreads}), 404)
	if got := s.Requests(); got[len(got)-1].Status != 404 {
		t.Errorf("request log = %+v", got[len(got)-1])
	}
}

func TestResolveRoundTrip(t *testing.T) {
	s := newServer(t)
	nodes := threads(t, s, "api", 2)
	t1 := threadByPath(t, nodes, "limit/bucket.go", 6)
	id := str(t, t1, "id")
	before := get(t, s, api+"/issues/2").obj(t)

	d := gqlOK(t, s, mResolve, map[string]any{"id": id})
	if str(t, d, "resolveReviewThread", "thread", "id") != id || at(t, d, "resolveReviewThread", "thread", "isResolved") != true {
		t.Fatalf("resolve = %v", d)
	}
	after := threadByPath(t, threads(t, s, "api", 2), "limit/bucket.go", 6)
	if after["isResolved"] != true {
		t.Error("a later read does not reflect the resolution")
	}
	// Others are untouched; resolving twice is idempotent.
	if threadByPath(t, threads(t, s, "api", 2), "limit/bucket.go", 3)["isResolved"] != false {
		t.Error("resolving T1 resolved T2")
	}
	gqlOK(t, s, mResolve, map[string]any{"id": id})

	d = gqlOK(t, s, mUnresolve, map[string]any{"id": id})
	if at(t, d, "unresolveReviewThread", "thread", "isResolved") != false {
		t.Fatalf("unresolve = %v", d)
	}
	if threadByPath(t, threads(t, s, "api", 2), "limit/bucket.go", 6)["isResolved"] != false {
		t.Error("unresolve did not stick")
	}
	// Unresolving a thread that was resolved from the start works too.
	t3 := str(t, threadByPath(t, nodes, "limit/bucket.go", 5), "id")
	gqlOK(t, s, mUnresolve, map[string]any{"id": t3})
	if threadByPath(t, threads(t, s, "api", 2), "limit/bucket.go", 5)["isResolved"] != false {
		t.Error("unresolving the seeded-resolved thread did not stick")
	}
	if str(t, get(t, s, api+"/issues/2").obj(t), "updated_at") <= str(t, before, "updated_at") {
		t.Error("updated_at did not advance on resolve")
	}

	for _, id := range []string{"PRRT_kgDOnope", "garbage", ""} {
		out := gql(t, s, mResolve, map[string]any{"id": id})
		if str(t, out, "errors", 0, "type") != "NOT_FOUND" || at(t, out, "data", "resolveReviewThread") != nil {
			t.Errorf("resolve %q = %v", id, out)
		}
	}
}

func TestAddThreadAndReply(t *testing.T) {
	s := newServer(t)
	head := str(t, get(t, s, api+"/pulls/5").obj(t), "head", "sha")
	rv := expect(t, call(t, s, "POST", api+"/pulls/5/reviews", map[string]any{"commit_id": head}), 200).obj(t)
	rvNode := str(t, rv, "node_id")
	if !strings.HasPrefix(rvNode, "PRR_") || rv["state"] != "PENDING" {
		t.Fatalf("pending review = %v", rv)
	}
	input := func(extra map[string]any) map[string]any {
		in := map[string]any{"pullRequestReviewId": rvNode, "path": "webhook/budget.go", "line": 3, "side": "RIGHT", "body": "Rename?"}
		for k, v := range extra {
			in[k] = v
		}
		return map[string]any{"input": in}
	}

	for name, tc := range map[string]struct {
		extra map[string]any
		want  string
	}{
		"path not in diff":   {map[string]any{"path": "other.go"}, "Path is not part of the diff"},
		"line not in diff":   {map[string]any{"line": 40}, "Line must be part of the diff"},
		"LEFT of added file": {map[string]any{"side": "LEFT", "line": 1}, "Line must be part of the diff"},
		"blank body":         {map[string]any{"body": ""}, "Body can't be blank"},
		"unknown review":     {map[string]any{"pullRequestReviewId": "PRR_kgDOzzz"}, ""},
	} {
		out := gql(t, s, mAddThread, input(tc.extra))
		typ := "UNPROCESSABLE"
		if name == "unknown review" {
			typ = "NOT_FOUND"
		}
		if str(t, out, "errors", 0, "type") != typ || (tc.want != "" && str(t, out, "errors", 0, "message") != tc.want) || at(t, out, "data", "addPullRequestReviewThread") != nil {
			t.Errorf("%s = %v", name, out)
		}
	}
	if n, _ := s.PendingReviewComments(prRef("ade-demo/api", 5)); n != 0 {
		t.Errorf("failed mutations left %d pending comments", n)
	}

	d := gqlOK(t, s, mAddThread, input(nil))
	th := at(t, d, "addPullRequestReviewThread", "thread")
	dbID := num(t, th, "comments", "nodes", 0, "databaseId")
	if !strings.HasPrefix(str(t, th, "id"), "PRRT_") || dbID == 0 {
		t.Fatalf("thread = %v", th)
	}
	if n, _ := s.PendingReviewComments(prRef("ade-demo/api", 5)); n != 1 {
		t.Errorf("PendingReviewComments = %d, want 1", n)
	}
	got := threadByPath(t, threads(t, s, "api", 5), "webhook/budget.go", 3)
	if str(t, got, "id") != str(t, th, "id") || str(t, got, "comments", "nodes", 0, "pullRequestReview", "state") != "PENDING" || num(t, got, "comments", "nodes", 0, "databaseId") != dbID {
		t.Errorf("pending thread read back = %v", got)
	}
	// The same comment is on the REST side, attached to the review.
	rid := strconv.Itoa(num(t, rv, "id"))
	if rest := get(t, s, api+"/pulls/5/reviews/"+rid+"/comments").arr(t); len(rest) != 1 || num(t, rest[0], "id") != dbID || str(t, rest[0], "path") != "webhook/budget.go" || num(t, rest[0], "line") != 3 {
		t.Errorf("REST review comments = %v", rest)
	}

	reply := func(extra map[string]any) map[string]any {
		in := map[string]any{"pullRequestReviewThreadId": str(t, th, "id"), "body": "Replying"}
		for k, v := range extra {
			in[k] = v
		}
		return map[string]any{"input": in}
	}
	// With a review id the reply is pending in that review.
	pend := at(t, gqlOK(t, s, mReply, reply(map[string]any{"pullRequestReviewId": rvNode})), "addPullRequestReviewThreadReply", "comment")
	if str(t, pend, "body") != "Replying" || str(t, pend, "author", "login") != "demo-user" || num(t, pend, "databaseId") == 0 || str(t, pend, "createdAt") == "" || !strings.HasPrefix(str(t, pend, "id"), "PRRC_") {
		t.Errorf("pending reply = %v", pend)
	}
	if n, _ := s.PendingReviewComments(prRef("ade-demo/api", 5)); n != 2 {
		t.Errorf("PendingReviewComments = %d, want 2", n)
	}
	// Without one it is immediate: visible, and not pending.
	imm := at(t, gqlOK(t, s, mReply, reply(nil)), "addPullRequestReviewThreadReply", "comment")
	if n, _ := s.PendingReviewComments(prRef("ade-demo/api", 5)); n != 2 {
		t.Errorf("an immediate reply changed the pending count: %d", n)
	}
	states := pluck[string](t, at(t, threadByPath(t, threads(t, s, "api", 5), "webhook/budget.go", 3), "comments", "nodes").([]any), "pullRequestReview", "state")
	if !slices.Equal(states, []string{"PENDING", "PENDING", "COMMENTED"}) {
		t.Errorf("comment states = %v", states)
	}
	if num(t, imm, "databaseId") == num(t, pend, "databaseId") {
		t.Error("comment ids collide")
	}
	// Replies to the seeded thread are visible to later reads.
	t2 := str(t, threadByPath(t, threads(t, s, "api", 2), "limit/bucket.go", 3), "id")
	gqlOK(t, s, mReply, map[string]any{"input": map[string]any{"pullRequestReviewThreadId": t2, "body": "On it"}})
	if texts := commentTexts(t, threadByPath(t, threads(t, s, "api", 2), "limit/bucket.go", 3)); len(texts) != 2 || texts[1] != "On it" {
		t.Errorf("T2 after reply = %v", texts)
	}

	// A pending review that is submitted turns its comments into visible ones.
	expect(t, call(t, s, "POST", api+"/pulls/5/reviews/"+rid+"/events", map[string]any{"event": "COMMENT"}), 200)
	for _, st := range pluck[string](t, at(t, threadByPath(t, threads(t, s, "api", 5), "webhook/budget.go", 3), "comments", "nodes").([]any), "pullRequestReview", "state") {
		if st == "PENDING" {
			t.Errorf("a comment is still pending after submit")
		}
	}
	// And the review can no longer take new threads.
	out := gql(t, s, mAddThread, input(nil))
	if str(t, out, "errors", 0, "type") != "UNPROCESSABLE" {
		t.Errorf("thread on a submitted review = %v", out)
	}
	bad := gql(t, s, mReply, map[string]any{"input": map[string]any{"pullRequestReviewThreadId": "PRRT_kgDOnope", "body": "x"}})
	if str(t, bad, "errors", 0, "type") != "NOT_FOUND" {
		t.Errorf("reply to unknown thread = %v", bad)
	}
}

func TestDraftToggleAndAutoMerge(t *testing.T) {
	s := newServer(t)
	node := str(t, get(t, s, web+"/pulls/2").obj(t), "node_id")
	ref := prRef("ade-demo/web", 2)

	if out := gql(t, s, mDraft, map[string]any{"id": node}); str(t, out, "errors", 0, "type") != "UNPROCESSABLE" || at(t, out, "data", "convertPullRequestToDraft") != nil {
		t.Errorf("convert an already-draft PR = %v", out)
	}
	// Draft PRs cannot be set to auto-merge.
	auto := map[string]any{"input": map[string]any{"pullRequestId": node, "mergeMethod": "SQUASH"}}
	if out := gql(t, s, mAuto, auto); str(t, out, "errors", 0, "type") != "UNPROCESSABLE" {
		t.Errorf("auto-merge on a draft = %v", out)
	}

	d := gqlOK(t, s, mReady, map[string]any{"id": node})
	if str(t, d, "markPullRequestReadyForReview", "pullRequest", "id") != node || at(t, d, "markPullRequestReadyForReview", "pullRequest", "isDraft") != false {
		t.Fatalf("mark ready = %v", d)
	}
	p := get(t, s, web+"/pulls/2").obj(t)
	if p["draft"] != false {
		t.Errorf("REST draft = %v after GraphQL ready", p["draft"])
	}
	// A ready PR whose checks fail reports unstable.
	if str(t, p, "mergeable_state") != "unstable" {
		t.Errorf("mergeable_state = %v, want unstable (lint failed)", p["mergeable_state"])
	}
	if d, _ := s.IsDraft(ref); d {
		t.Error("IsDraft true after ready")
	}
	if out := gql(t, s, mReady, map[string]any{"id": node}); str(t, out, "errors", 0, "type") != "UNPROCESSABLE" {
		t.Errorf("ready twice = %v", out)
	}
	evs := pluck[string](t, get(t, s, web+"/issues/2/events").arr(t), "event")
	if !slices.Contains(evs, "ready_for_review") {
		t.Errorf("events = %v", evs)
	}

	out := gqlOK(t, s, mAuto, auto)
	enabled := str(t, out, "enablePullRequestAutoMerge", "pullRequest", "autoMergeRequest", "enabledAt")
	if enabled == "" {
		t.Fatalf("auto-merge = %v", out)
	}
	if _, isAuto, params, _ := s.MergeState(ref); !isAuto || params["merge_method"] != "squash" {
		t.Errorf("MergeState auto=%v params=%v", isAuto, params)
	}
	am := at(t, get(t, s, web+"/pulls/2").obj(t), "auto_merge")
	if am == nil || str(t, am, "merge_method") != "squash" {
		t.Errorf("REST auto_merge = %v", am)
	}
	// expectedHeadOid must match the head.
	stale := map[string]any{"input": map[string]any{"pullRequestId": node, "mergeMethod": "MERGE", "expectedHeadOid": strings.Repeat("b", 40)}}
	if out := gql(t, s, mAuto, stale); str(t, out, "errors", 0, "type") != "UNPROCESSABLE" {
		t.Errorf("stale expectedHeadOid = %v", out)
	}
	head := str(t, get(t, s, web+"/pulls/2").obj(t), "head", "sha")
	gqlOK(t, s, mAuto, map[string]any{"input": map[string]any{"pullRequestId": node, "mergeMethod": "REBASE", "expectedHeadOid": head}})
	if _, _, params, _ := s.MergeState(ref); params["merge_method"] != "rebase" || params["sha"] != head {
		t.Errorf("params = %v", params)
	}

	d = gqlOK(t, s, mDraft, map[string]any{"id": node})
	if at(t, d, "convertPullRequestToDraft", "pullRequest", "isDraft") != true {
		t.Fatalf("convert = %v", d)
	}
	if get(t, s, web+"/pulls/2").obj(t)["draft"] != true {
		t.Error("REST draft false after GraphQL convert")
	}

	// Unknown and merged PRs.
	if out := gql(t, s, mReady, map[string]any{"id": "PR_kgDOnope"}); str(t, out, "errors", 0, "type") != "NOT_FOUND" {
		t.Errorf("unknown PR = %v", out)
	}
	merged := str(t, get(t, s, web+"/pulls/4").obj(t), "node_id")
	if out := gql(t, s, mAuto, map[string]any{"input": map[string]any{"pullRequestId": merged, "mergeMethod": "MERGE"}}); str(t, out, "errors", 0, "type") != "UNPROCESSABLE" {
		t.Errorf("auto-merge on a merged PR = %v", out)
	}
	// A merge clears auto-merge and the REST merge state agrees.
	node5 := str(t, get(t, s, api+"/pulls/5").obj(t), "node_id")
	gqlOK(t, s, mAuto, map[string]any{"input": map[string]any{"pullRequestId": node5, "mergeMethod": "MERGE"}})
	expect(t, call(t, s, "PUT", api+"/pulls/5/merge", map[string]any{}), 200)
	if merged, isAuto, _, _ := s.MergeState(prRef("ade-demo/api", 5)); !merged || isAuto {
		t.Errorf("after merge: merged=%v auto=%v", merged, isAuto)
	}
}

func TestIssuePRsQuery(t *testing.T) {
	s := newServer(t)
	type node struct {
		number int
		repo   string
	}
	read := func(repo string, n int) (closedBy, cross []node, crossTypes []string) {
		d := gqlOK(t, s, qIssuePRs, threadVars(repo, n))
		iss := at(t, d, "repository", "issue")
		for _, c := range at(t, iss, "closedByPullRequestsReferences", "nodes").([]any) {
			closedBy = append(closedBy, node{num(t, c, "number"), str(t, c, "repository", "nameWithOwner")})
		}
		for _, c := range at(t, iss, "timelineItems", "nodes").([]any) {
			if str(t, c, "__typename") != "CrossReferencedEvent" {
				t.Errorf("timeline node = %v", c)
			}
			crossTypes = append(crossTypes, str(t, c, "source", "__typename"))
			if str(t, c, "source", "__typename") == "PullRequest" {
				cross = append(cross, node{num(t, c, "source", "number"), str(t, c, "source", "repository", "nameWithOwner")})
			}
		}
		return closedBy, cross, crossTypes
	}

	closedBy, cross, _ := read("api", 1)
	if !slices.Equal(closedBy, []node{{2, "ade-demo/api"}}) || !slices.Equal(cross, []node{{2, "ade-demo/api"}}) {
		t.Errorf("api#1: closedBy %v cross %v", closedBy, cross)
	}
	// "Part of ade-demo/tracker#1" and "Parent: ade-demo/tracker#1" reference
	// tracker#1 without closing it.
	closedBy, cross, types := read("tracker", 1)
	slices.Sort(types)
	if len(closedBy) != 0 || !slices.Equal(cross, []node{{2, "ade-demo/api"}}) || !slices.Equal(types, []string{"Issue", "PullRequest"}) {
		t.Errorf("tracker#1: closedBy %v cross %v types %v", closedBy, cross, types)
	}
	// Merged PRs count: web#4 closes web#3.
	if closedBy, _, _ := read("web", 3); !slices.Equal(closedBy, []node{{4, "ade-demo/web"}}) {
		t.Errorf("web#3 closedBy = %v", closedBy)
	}
	if closedBy, cross, _ := read("tracker", 3); len(closedBy)+len(cross) != 0 {
		t.Errorf("tracker#3: %v %v", closedBy, cross)
	}
	out := gql(t, s, qIssuePRs, threadVars("api", 2)) // a PR is not an issue here
	if str(t, out, "errors", 0, "type") != "NOT_FOUND" || str(t, out, "errors", 0, "message") != "Could not resolve to an Issue with the number of 2." {
		t.Errorf("PR as issue = %v", out)
	}
	if out := gql(t, s, qIssuePRs, threadVars("api", 99)); str(t, out, "errors", 0, "type") != "NOT_FOUND" {
		t.Errorf("missing issue = %v", out)
	}
}
