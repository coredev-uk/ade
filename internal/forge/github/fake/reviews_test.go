package fake_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge/github/fake"
)

func reviewStates(t *testing.T, s *fake.Server, repo string, n int) []string {
	t.Helper()
	revs := get(t, s, repo+"/pulls/"+strconv.Itoa(n)+"/reviews").arr(t)
	out := make([]string, len(revs))
	for i, r := range revs {
		out[i] = str(t, r, "user", "login") + ":" + str(t, r, "state")
	}
	return out
}

func TestSeededReviewsAndVisibility(t *testing.T) {
	s := newServer(t)
	revs := get(t, s, api+"/pulls/2/reviews").arr(t)
	if got := reviewStates(t, s, api, 2); !slices.Equal(got, []string{"teammate:COMMENTED", "demo-user:COMMENTED"}) {
		t.Fatalf("api#2 reviews = %v (another user's PENDING review must be invisible)", got)
	}
	for _, r := range revs {
		if str(t, r, "submitted_at") == "" || len(str(t, r, "commit_id")) != 40 || !strings.HasPrefix(str(t, r, "node_id"), "PRR_") {
			t.Errorf("review = %v", r)
		}
	}
	// The hidden review's comment is not in any comment listing either.
	for _, c := range get(t, s, api+"/pulls/2/comments").arr(t) {
		if str(t, c, "user", "login") == "reviewer-bot" {
			t.Errorf("another user's pending comment leaked: %v", c)
		}
	}
	if n, _ := s.PendingReviewComments(prRef("ade-demo/api", 2)); n != 0 {
		t.Errorf("PendingReviewComments counted another user's pending review: %d", n)
	}
	// The visible review's comments are retrievable; the root has no in_reply_to_id.
	first := num(t, revs[0], "id")
	cs := get(t, s, api+"/pulls/2/reviews/"+strconv.Itoa(first)+"/comments").arr(t)
	if len(cs) != 3 {
		t.Fatalf("teammate review comments = %d, want 3", len(cs))
	}
	for _, c := range cs {
		if has(c, "in_reply_to_id") || str(t, c, "path") != "limit/bucket.go" || str(t, c, "side") != "RIGHT" || num(t, c, "pull_request_review_id") != first || has(c, "start_line") && c.(map[string]any)["start_line"] != nil {
			t.Errorf("review comment = %v", c)
		}
	}
	all := get(t, s, api+"/pulls/2/comments").arr(t)
	var reply map[string]any
	for _, c := range all {
		if has(c, "in_reply_to_id") {
			reply = c.(map[string]any)
		}
	}
	if len(all) != 4 || reply == nil || str(t, reply, "user", "login") != "demo-user" || num(t, reply, "in_reply_to_id") != num(t, cs[0], "id") && num(t, reply, "in_reply_to_id") != num(t, cs[1], "id") && num(t, reply, "in_reply_to_id") != num(t, cs[2], "id") {
		t.Errorf("comments = %d, reply = %v", len(all), reply)
	}
	if got := reviewStates(t, s, api, 4); len(got) != 0 {
		t.Errorf("api#4 reviews = %v", got)
	}
	expect(t, get(t, s, api+"/pulls/2/reviews/424242"), 404)
	expect(t, get(t, s, api+"/pulls/2/reviews/424242/comments"), 404)
	expect(t, get(t, s, api+"/pulls/1/reviews"), 404)
}

func TestReviewLifecycle(t *testing.T) {
	s := newServer(t)
	five := prRef("ade-demo/api", 5)
	head := str(t, get(t, s, api+"/pulls/5").obj(t), "head", "sha")
	post := func(body map[string]any) resp { return call(t, s, "POST", api+"/pulls/5/reviews", body) }

	// Anchors are validated against the diff, before anything is created.
	for name, c := range map[string]map[string]any{
		"path not in diff": {"path": "nope.go", "line": 1, "side": "RIGHT", "body": "x"},
		"line outside":     {"path": "webhook/budget.go", "line": 50, "side": "RIGHT", "body": "x"},
		"left of new file": {"path": "webhook/budget.go", "line": 1, "side": "LEFT", "body": "x"},
		"no body":          {"path": "webhook/budget.go", "line": 1, "side": "RIGHT"},
	} {
		e := expect(t, post(map[string]any{"commit_id": head, "comments": []any{c}}), 422).obj(t)
		if str(t, e, "message") != "Validation Failed" {
			t.Errorf("%s: %v", name, e)
		}
	}
	e := expect(t, post(map[string]any{"commit_id": head, "comments": []any{map[string]any{"path": "nope.go", "line": 1, "body": "x"}}}), 422).obj(t)
	if str(t, e, "errors", 0, "field") != "pull_request_review_thread.path" {
		t.Errorf("path error = %v", e)
	}
	e = expect(t, post(map[string]any{"commit_id": head, "comments": []any{map[string]any{"path": "webhook/budget.go", "line": 50, "body": "x"}}}), 422).obj(t)
	if str(t, e, "errors", 0, "message") != "pull_request_review_thread.line must be part of the diff" {
		t.Errorf("line error = %v", e)
	}
	expect(t, post(map[string]any{"commit_id": strings.Repeat("c", 40)}), 422)
	expect(t, post(map[string]any{"commit_id": head, "event": "APPROVED"}), 422)
	if got := reviewStates(t, s, api, 5); len(got) != 0 {
		t.Fatalf("rejected reviews left %v", got)
	}

	// Pending review: invisible to others, visible to its author.
	rv := expect(t, post(map[string]any{"commit_id": head, "body": "Mostly fine", "comments": []any{
		map[string]any{"path": "webhook/budget.go", "line": 3, "side": "RIGHT", "body": "Name this MaxRetries"},
		map[string]any{"path": "webhook/budget.go", "line": 4, "body": "Doc the zero value"},
	}}), 200).obj(t)
	id := strconv.Itoa(num(t, rv, "id"))
	if rv["state"] != "PENDING" || has(rv, "submitted_at") || str(t, rv, "user", "login") != "demo-user" || str(t, rv, "commit_id") != head || str(t, rv, "body") != "Mostly fine" {
		t.Fatalf("pending review = %v", rv)
	}
	if got := reviewStates(t, s, api, 5); !slices.Equal(got, []string{"demo-user:PENDING"}) {
		t.Errorf("reviews = %v", got)
	}
	if n, _ := s.PendingReviewComments(five); n != 2 {
		t.Errorf("PendingReviewComments = %d", n)
	}
	if n, _ := s.Publishes(five); n != 0 {
		t.Errorf("Publishes = %d before submit", n)
	}
	one := expect(t, post(map[string]any{"commit_id": head}), 422).obj(t)
	if !strings.Contains(string(mustJSON(t, one)), "User can only have one pending review per pull request") {
		t.Errorf("second pending = %v", one)
	}
	// Pending comments show up as pending thread comments for the author only.
	gt := threadByPath(t, threads(t, s, "api", 5), "webhook/budget.go", 3)
	if str(t, gt, "comments", "nodes", 0, "pullRequestReview", "state") != "PENDING" || num(t, gt, "line") != 3 {
		t.Errorf("pending thread = %v", gt)
	}
	if rc := get(t, s, api+"/pulls/5/reviews/"+id+"/comments").arr(t); len(rc) != 2 || str(t, rc[1], "path") != "webhook/budget.go" || str(t, rc[1], "side") != "RIGHT" {
		t.Errorf("pending review comments = %v", rc)
	}

	upd := expect(t, call(t, s, "PUT", api+"/pulls/5/reviews/"+id, map[string]any{"body": "Updated summary"}), 200).obj(t)
	if str(t, upd, "body") != "Updated summary" || upd["state"] != "PENDING" {
		t.Errorf("updated = %v", upd)
	}
	expect(t, call(t, s, "PUT", api+"/pulls/5/reviews/"+id, map[string]any{"body": ""}), 422)

	// Submitting publishes: state, timestamp, counter, thread visibility.
	sub := expect(t, call(t, s, "POST", api+"/pulls/5/reviews/"+id+"/events", map[string]any{"event": "REQUEST_CHANGES"}), 200).obj(t)
	if sub["state"] != "CHANGES_REQUESTED" || str(t, sub, "submitted_at") == "" || str(t, sub, "body") != "Updated summary" {
		t.Fatalf("submitted = %v", sub)
	}
	if n, _ := s.Publishes(five); n != 1 {
		t.Errorf("Publishes = %d, want 1", n)
	}
	if n, _ := s.PendingReviewComments(five); n != 0 {
		t.Errorf("PendingReviewComments = %d after submit", n)
	}
	gt = threadByPath(t, threads(t, s, "api", 5), "webhook/budget.go", 3)
	if str(t, gt, "comments", "nodes", 0, "pullRequestReview", "state") != "CHANGES_REQUESTED" {
		t.Errorf("thread after submit = %v", gt)
	}
	if a, _ := s.Approvers(five); len(a) != 0 {
		t.Errorf("Approvers = %v after request changes", a)
	}
	for name, r := range map[string]resp{
		"submit again": call(t, s, "POST", api+"/pulls/5/reviews/"+id+"/events", map[string]any{"event": "COMMENT"}),
		"update":       call(t, s, "PUT", api+"/pulls/5/reviews/"+id, map[string]any{"body": "x"}),
		"delete":       call(t, s, "DELETE", api+"/pulls/5/reviews/"+id, nil),
	} {
		e := expect(t, r, 422).obj(t)
		if str(t, e, "message") == "" {
			t.Errorf("%s: %v", name, e)
		}
	}
	if n, _ := s.Publishes(five); n != 1 {
		t.Errorf("rejected submits changed Publishes to %d", n)
	}

	// Approving after requesting changes: the latest verdict wins.
	ap := expect(t, post(map[string]any{"commit_id": head, "event": "APPROVE"}), 200).obj(t)
	if ap["state"] != "APPROVED" || str(t, ap, "submitted_at") == "" {
		t.Errorf("approve = %v", ap)
	}
	if a, _ := s.Approvers(five); !slices.Equal(a, []string{"demo-user"}) {
		t.Errorf("Approvers = %v", a)
	}
	if n, _ := s.Publishes(five); n != 2 {
		t.Errorf("Publishes = %d, want 2 (a review created with an event is a submit)", n)
	}

	// Dismissal turns an approval into DISMISSED, and only those.
	apID := strconv.Itoa(num(t, ap, "id"))
	expect(t, call(t, s, "PUT", api+"/pulls/5/reviews/"+apID+"/dismissals", map[string]any{}), 422)
	d := expect(t, call(t, s, "PUT", api+"/pulls/5/reviews/"+apID+"/dismissals", map[string]any{"message": "stale"}), 200).obj(t)
	if d["state"] != "DISMISSED" {
		t.Errorf("dismissed = %v", d)
	}
	if a, _ := s.Approvers(five); len(a) != 0 {
		t.Errorf("Approvers = %v after dismissal", a)
	}
	expect(t, call(t, s, "PUT", api+"/pulls/5/reviews/"+apID+"/dismissals", map[string]any{"message": "again"}), 422)
	pend := expect(t, post(map[string]any{"commit_id": head}), 200).obj(t)
	expect(t, call(t, s, "PUT", api+"/pulls/5/reviews/"+strconv.Itoa(num(t, pend, "id"))+"/dismissals", map[string]any{"message": "x"}), 422)

	// Deleting a pending review removes it and its comments.
	pid := strconv.Itoa(num(t, pend, "id"))
	expect(t, call(t, s, "POST", api+"/pulls/5/comments", map[string]any{"body": "side comment", "commit_id": head, "path": "webhook/budget.go", "line": 2}), 201)
	del := expect(t, call(t, s, "DELETE", api+"/pulls/5/reviews/"+pid, nil), 200).obj(t)
	if str(t, del, "state") != "PENDING" {
		t.Errorf("deleted review = %v", del)
	}
	for _, st := range reviewStates(t, s, api, 5) {
		if strings.HasSuffix(st, ":PENDING") {
			t.Errorf("review survived deletion: %v", reviewStates(t, s, api, 5))
		}
	}
	expect(t, call(t, s, "DELETE", api+"/pulls/5/reviews/"+pid, nil), 404)
	expect(t, call(t, s, "PUT", api+"/pulls/5/reviews/"+pid, map[string]any{"body": "x"}), 404)
}

func TestOwnPullRequestVerdicts(t *testing.T) {
	s := newServer(t)
	head := str(t, get(t, s, api+"/pulls/2").obj(t), "head", "sha")
	post := func(body map[string]any) resp {
		body["commit_id"] = head
		return call(t, s, "POST", api+"/pulls/2/reviews", body)
	}
	e := expect(t, post(map[string]any{"event": "APPROVE"}), 422).obj(t)
	if !strings.Contains(string(mustJSON(t, e)), "Can not approve your own pull request") {
		t.Errorf("approve own PR = %v", e)
	}
	e = expect(t, post(map[string]any{"event": "REQUEST_CHANGES", "body": "no"}), 422).obj(t)
	if !strings.Contains(string(mustJSON(t, e)), "Can not request changes on your own pull request") {
		t.Errorf("request changes on own PR = %v", e)
	}
	expect(t, post(map[string]any{"event": "COMMENT"}), 422) // nothing to say
	ok := expect(t, post(map[string]any{"event": "COMMENT", "body": "Self-review note"}), 200).obj(t)
	if ok["state"] != "COMMENTED" {
		t.Errorf("comment on own PR = %v", ok)
	}
	if a, _ := s.Approvers(prRef("ade-demo/api", 2)); len(a) != 0 {
		t.Errorf("Approvers = %v", a)
	}

	// A pending review can't be approved at submit time either.
	pend := expect(t, post(map[string]any{}), 200).obj(t)
	e = expect(t, call(t, s, "POST", api+"/pulls/2/reviews/"+strconv.Itoa(num(t, pend, "id"))+"/events", map[string]any{"event": "APPROVE"}), 422).obj(t)
	if !strings.Contains(string(mustJSON(t, e)), "Can not approve your own pull request") {
		t.Errorf("submit approve on own PR = %v", e)
	}
	if n, _ := s.Publishes(prRef("ade-demo/api", 2)); n != 1 {
		t.Errorf("Publishes = %d, want only the COMMENT review", n)
	}
	expect(t, call(t, s, "POST", api+"/pulls/2/reviews/"+strconv.Itoa(num(t, pend, "id"))+"/events", map[string]any{"event": "BOGUS"}), 422)
}

func TestImmediateComments(t *testing.T) {
	s := newServer(t)
	head := str(t, get(t, s, api+"/pulls/2").obj(t), "head", "sha")
	all := get(t, s, api+"/pulls/2/comments").arr(t)
	var t2 map[string]any
	for _, c := range all {
		if strings.HasPrefix(str(t, c, "body"), "Please add a test") {
			t2 = c.(map[string]any)
		}
	}
	post := func(body map[string]any) resp {
		if _, ok := body["commit_id"]; !ok {
			body["commit_id"] = head
		}
		return call(t, s, "POST", api+"/pulls/2/comments", body)
	}

	// Replies join the thread.
	reply := expect(t, post(map[string]any{"body": "Added in the next push", "in_reply_to": t2["id"]}), 201).obj(t)
	if num(t, reply, "in_reply_to_id") != num(t, t2, "id") || str(t, reply, "path") != "limit/bucket.go" || num(t, reply, "line") != 3 || str(t, reply, "user", "login") != "demo-user" || num(t, reply, "pull_request_review_id") == 0 {
		t.Errorf("reply = %v", reply)
	}
	th := threadByPath(t, threads(t, s, "api", 2), "limit/bucket.go", 3)
	if got := commentTexts(t, th); len(got) != 2 || got[1] != "Added in the next push" {
		t.Errorf("thread after reply = %v", got)
	}
	// A new line comment opens a thread.
	c := expect(t, post(map[string]any{"body": "Why a pointer?", "path": "limit/bucket.go", "line": 4, "side": "RIGHT"}), 201).obj(t)
	if has(c, "in_reply_to_id") || num(t, c, "line") != 4 || str(t, c, "side") != "RIGHT" || str(t, c, "commit_id") != head {
		t.Errorf("new thread comment = %v", c)
	}
	nodes := threads(t, s, "api", 2)
	if len(nodes) != 4 {
		t.Errorf("threads = %d, want 4", len(nodes))
	}
	if st := str(t, threadByPath(t, nodes, "limit/bucket.go", 4), "comments", "nodes", 0, "pullRequestReview", "state"); st != "COMMENTED" {
		t.Errorf("immediate comment is in a %s review", st)
	}
	if n, _ := s.PendingReviewComments(prRef("ade-demo/api", 2)); n != 0 {
		t.Errorf("immediate comments counted as pending: %d", n)
	}
	if n, _ := s.Publishes(prRef("ade-demo/api", 2)); n != 0 {
		t.Errorf("immediate comments counted as publishes: %d", n)
	}

	for name, body := range map[string]map[string]any{
		"unknown path":       {"body": "x", "path": "nope.go", "line": 1},
		"line outside diff":  {"body": "x", "path": "limit/bucket.go", "line": 20},
		"left of added file": {"body": "x", "path": "limit/bucket.go", "line": 1, "side": "LEFT"},
		"stale commit":       {"body": "x", "path": "limit/bucket.go", "line": 1, "commit_id": strings.Repeat("d", 40)},
		"no body":            {"path": "limit/bucket.go", "line": 1},
		"missing path":       {"body": "x", "line": 1},
		"unknown reply":      {"body": "x", "in_reply_to": 4242},
	} {
		if r := post(body); r.code != 422 {
			t.Errorf("%s = %d %s, want 422", name, r.code, r.body)
		}
	}
	if len(threads(t, s, "api", 2)) != 4 {
		t.Error("rejected comments created threads")
	}

	// Deleting: own comments only; gone afterwards.
	id := num(t, c, "id")
	expect(t, call(t, s, "DELETE", api+"/pulls/comments/"+strconv.Itoa(id), nil), 204)
	expect(t, call(t, s, "DELETE", api+"/pulls/comments/"+strconv.Itoa(id), nil), 404)
	expect(t, call(t, s, "DELETE", api+"/pulls/comments/424242", nil), 404)
	if len(threads(t, s, "api", 2)) != 3 {
		t.Error("deleting the only comment left an empty thread")
	}
	expect(t, call(t, s, "DELETE", api+"/pulls/comments/"+strconv.Itoa(num(t, t2, "id")), nil), 403) // teammate's
}
