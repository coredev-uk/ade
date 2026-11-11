package fake_test

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestListIssues(t *testing.T) {
	s := newServer(t)
	for _, tc := range []struct {
		name, path string
		want       []int // numbers, sorted
	}{
		{"default is open issues", tracker + "/issues", []int{1, 2, 3, 4, 5, 6, 7, 8, 11}},
		{"closed", tracker + "/issues?state=closed", []int{9, 10}},
		{"all", tracker + "/issues?state=all", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}},
		{"list includes pull requests", api + "/issues?state=open", []int{1, 2, 4, 5}},
		{"assignee", tracker + "/issues?assignee=demo-user", []int{1, 2, 3, 4, 5, 8, 11}},
		{"assignee none", tracker + "/issues?assignee=none", []int{6, 7}},
		{"assignee any", tracker + "/issues?assignee=*&state=closed", []int{9, 10}},
		{"milestone number", tracker + "/issues?milestone=3", []int{7, 8}},
		{"milestone any", tracker + "/issues?milestone=*&state=all&per_page=100", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}},
		{"milestone none", tracker + "/issues?milestone=none&state=all", nil},
		{"labels are ANDed", tracker + "/issues?state=all&labels=type:bug,app:web", []int{2, 9}},
		{"labels with space", tracker + "/issues?labels=workflow:in+review", []int{1, 2}},
		{"unlabelled issue", tracker + "/issues?labels=type:story,type:bug", nil},
		{"since keeps recently updated", tracker + "/issues?state=all&since=2026-11-09T12:00:00Z", []int{1}},
		{"creator", api + "/issues?state=all&creator=teammate", []int{5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := numbers(t, get(t, s, tc.path).arr(t))
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("%s = %v, want %v", tc.path, got, tc.want)
			}
		})
	}

	t.Run("sorting", func(t *testing.T) {
		upd := numbers(t, get(t, s, tracker+"/issues?state=all&sort=updated&direction=desc").arr(t))
		if upd[0] != 1 {
			t.Errorf("most recently updated first = %v, want #1", upd)
		}
		asc := numbers(t, get(t, s, tracker+"/issues?state=all&sort=created&direction=asc&per_page=100").arr(t))
		if asc[0] != 9 {
			t.Errorf("oldest created first = %v, want #9", asc)
		}
	})
}

func TestIssueShape(t *testing.T) {
	s := newServer(t)
	it := get(t, s, tracker+"/issues/1").obj(t)
	if str(t, it, "title") != "Rate-limit the export endpoint" || str(t, it, "state") != "open" || it["state_reason"] != nil || it["closed_at"] != nil {
		t.Errorf("issue = %v", it)
	}
	labels := pluck[string](t, at(t, it, "labels").([]any), "name")
	slices.Sort(labels)
	if want := []string{"app:api", "type:story", "weight:5", "workflow:in review"}; !slices.Equal(labels, want) {
		t.Errorf("labels = %v, want %v", labels, want)
	}
	if str(t, it, "milestone", "title") != "Sprint 42" || num(t, it, "milestone", "number") != 2 || str(t, it, "milestone", "due_on") != "2026-11-13T12:00:00Z" {
		t.Errorf("milestone = %v", it["milestone"])
	}
	if str(t, it, "assignees", 0, "login") != "demo-user" || str(t, it, "user", "login") != "demo-user" {
		t.Errorf("people = %v %v", it["assignees"], it["user"])
	}
	if str(t, it, "repository_url") != s.URL+tracker || !strings.HasPrefix(str(t, it, "html_url"), s.URL+"/ade-demo/tracker/issues/1") {
		t.Errorf("urls = %v %v", it["repository_url"], it["html_url"])
	}
	body := str(t, it, "body")
	if strings.Count(body, "- [x]") != 1 || strings.Count(body, "- [ ]") != 3 {
		t.Errorf("checklist in body: %q", body)
	}
	if has(it, "pull_request") {
		t.Error("an issue carries a pull_request key")
	}
	if str(t, it, "node_id") == "" || num(t, it, "id") == 0 || str(t, it, "created_at") != "2026-11-01T12:00:00Z" || str(t, it, "updated_at") != "2026-11-09T12:00:00Z" {
		t.Errorf("ids/times = %v", it)
	}
	// Free tier: no weight or iteration fields.
	for _, k := range []string{"weight", "iteration", "issue_field_values"} {
		if has(it, k) {
			t.Errorf("issue has Free-tier-absent key %q", k)
		}
	}

	done := get(t, s, tracker+"/issues/10").obj(t)
	if str(t, done, "state") != "closed" || str(t, done, "state_reason") != "completed" || str(t, done, "closed_at") != "2026-11-06T12:00:00Z" {
		t.Errorf("closed issue = %v", done)
	}

	// A PR number answers on the issues API with a pull_request key.
	pr := get(t, s, api+"/issues/2").obj(t)
	if !has(pr, "pull_request") || str(t, pr, "pull_request", "url") != s.URL+api+"/pulls/2" || str(t, pr, "node_id") == str(t, get(t, s, api+"/issues/1").obj(t), "node_id") {
		t.Errorf("PR as issue = %v", pr)
	}
	expect(t, get(t, s, tracker+"/issues/999"), 404)
	expect(t, get(t, s, tracker+"/issues/abc"), 404)
}

func TestCreateIssue(t *testing.T) {
	s := newServer(t)
	before := get(t, s, tracker+"/issues/11").obj(t)
	if n := num(t, get(t, s, tracker+"/milestones").arr(t), 0, "open_issues"); n != 7 {
		t.Fatalf("Sprint 42 open_issues = %d", n)
	}
	r := expect(t, call(t, s, "POST", tracker+"/issues", map[string]any{
		"title": "New thing", "body": "Details", "labels": []string{"type:story", "brand-new"},
		"assignees": []string{"demo-user", "ghost"}, "milestone": 2,
	}), 201)
	it := r.obj(t)
	if num(t, it, "number") != 12 || str(t, it, "state") != "open" || str(t, it, "user", "login") != "demo-user" {
		t.Fatalf("created = %v", it)
	}
	if got := pluck[string](t, at(t, it, "assignees").([]any), "login"); !slices.Equal(got, []string{"demo-user"}) {
		t.Errorf("assignees = %v (unknown logins are dropped)", got)
	}
	labels, err := s.Labels(issueRef("ade-demo/tracker", 12))
	if err != nil || !slices.Equal(labels, []string{"type:story", "brand-new"}) {
		t.Errorf("Labels = %v, %v", labels, err)
	}
	if str(t, it, "labels", 1, "name") != "brand-new" || str(t, it, "labels", 1, "color") == "" {
		t.Errorf("unknown label not created: %v", it["labels"])
	}
	events := get(t, s, tracker+"/issues/12/events").arr(t)
	got := pluck[string](t, events, "event")
	slices.Sort(got)
	if want := []string{"assigned", "labeled", "labeled", "milestoned"}; !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	if n := num(t, get(t, s, tracker+"/milestones").arr(t), 0, "open_issues"); n != 8 {
		t.Errorf("Sprint 42 open_issues after create = %d, want 8", n)
	}
	if got := numbers(t, get(t, s, tracker+"/issues?labels=brand-new").arr(t)); !slices.Equal(got, []int{12}) {
		t.Errorf("filter by new label = %v", got)
	}

	// Timestamps are monotonic: a later write never carries an earlier time.
	c := expect(t, call(t, s, "POST", tracker+"/issues/11/comments", map[string]any{"body": "x"}), 201).obj(t)
	after := get(t, s, tracker+"/issues/11").obj(t)
	if str(t, after, "updated_at") <= str(t, before, "updated_at") || str(t, after, "updated_at") != str(t, c, "created_at") {
		t.Errorf("updated_at %v -> %v, comment at %v", before["updated_at"], after["updated_at"], c["created_at"])
	}
	if str(t, it, "created_at") <= "2026-11-11T12:00:00Z" {
		t.Errorf("created_at %v is not after the fake clock", it["created_at"])
	}

	for name, body := range map[string]any{
		"missing title":   map[string]any{"body": "x"},
		"blank title":     map[string]any{"title": "  "},
		"unknown mstone":  map[string]any{"title": "x", "milestone": 99},
		"title wrong typ": map[string]any{"title": 5},
	} {
		code := 422
		if name == "title wrong typ" {
			code = 400
		}
		if r := call(t, s, "POST", tracker+"/issues", body); r.code != code {
			t.Errorf("%s = %d %s, want %d", name, r.code, r.body, code)
		}
	}
	if e := call(t, s, "POST", tracker+"/issues", map[string]any{"title": ""}).obj(t); str(t, e, "message") != "Validation Failed" || str(t, e, "errors", 0, "code") != "missing_field" {
		t.Errorf("422 shape = %v", e)
	}
	expect(t, call(t, s, "POST", apiRoot+"/repos/ade-demo/none/issues", map[string]any{"title": "x"}), 404)
}

func TestLabelsAndEvents(t *testing.T) {
	s := newServer(t)

	// The seeded history of tracker#1, oldest first.
	ev := get(t, s, tracker+"/issues/1/events").arr(t)
	type step struct{ event, what string }
	var got []step
	for _, e := range ev {
		what := str(t, e, "label", "name")
		if what == "" {
			what = str(t, e, "milestone", "title")
		}
		got = append(got, step{str(t, e, "event"), what})
		if str(t, e, "actor", "login") != "demo-user" || str(t, e, "created_at") == "" {
			t.Errorf("event = %v", e)
		}
	}
	want := []step{
		{"milestoned", "Sprint 42"},
		{"labeled", "workflow:in progress"},
		{"unlabeled", "workflow:in progress"},
		{"labeled", "workflow:in review"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	if ts := pluck[string](t, ev, "created_at"); !slices.IsSorted(ts) || ts[0] != "2026-11-02T12:00:00Z" || ts[1] != "2026-11-03T12:00:00Z" {
		t.Errorf("event times = %v", ts)
	}
	closed := get(t, s, tracker+"/issues/10/events").arr(t)
	if len(closed) != 1 || str(t, closed[0], "event") != "closed" || str(t, closed[0], "created_at") != "2026-11-06T12:00:00Z" {
		t.Errorf("tracker#10 events = %v", closed)
	}

	t.Run("add", func(t *testing.T) {
		r := expect(t, call(t, s, "POST", tracker+"/issues/3/labels", map[string]any{"labels": []string{"workflow:in progress", "type:story", "novel"}}), 200)
		all := pluck[string](t, r.arr(t), "name")
		if want := []string{"type:story", "app:api", "weight:5", "workflow:in progress", "novel"}; !slices.Equal(all, want) {
			t.Errorf("labels after add = %v, want %v (existing kept, order stable)", all, want)
		}
		evs := get(t, s, tracker+"/issues/3/events").arr(t)
		if n := len(evs); n != 2 {
			t.Errorf("events = %d, want 2 (type:story was already present)", n)
		}
		// A bare array body is accepted as on GitHub.
		expect(t, call(t, s, "POST", tracker+"/issues/3/labels", []string{"weight:8"}), 200)
		if l, _ := s.Labels(issueRef("ade-demo/tracker", 3)); !slices.Contains(l, "weight:8") {
			t.Errorf("labels = %v", l)
		}
		expect(t, call(t, s, "POST", tracker+"/issues/3/labels", map[string]any{"labels": []string{}}), 422)
		expect(t, call(t, s, "POST", tracker+"/issues/999/labels", map[string]any{"labels": []string{"x"}}), 404)
	})

	t.Run("remove", func(t *testing.T) {
		r := expect(t, call(t, s, "DELETE", tracker+"/issues/1/labels/workflow%3Ain%20review", nil), 200)
		if got := pluck[string](t, r.arr(t), "name"); slices.Contains(got, "workflow:in review") || len(got) != 3 {
			t.Errorf("remaining = %v", got)
		}
		evs := get(t, s, tracker+"/issues/1/events").arr(t)
		if last := evs[len(evs)-1]; str(t, last, "event") != "unlabeled" || str(t, last, "label", "name") != "workflow:in review" {
			t.Errorf("last event = %v", last)
		}
		gone := expect(t, call(t, s, "DELETE", tracker+"/issues/1/labels/workflow%3Ain%20review", nil), 404).obj(t)
		if str(t, gone, "message") != "Label does not exist" {
			t.Errorf("second delete = %v", gone)
		}
		expect(t, call(t, s, "DELETE", tracker+"/issues/999/labels/x", nil), 404)
		l, _ := s.Labels(issueRef("ade-demo/tracker", 1))
		if slices.Contains(l, "workflow:in review") {
			t.Errorf("Labels() still has the removed label: %v", l)
		}
	})
}

func TestSubIssues(t *testing.T) {
	s := newServer(t)

	subs := get(t, s, tracker+"/issues/1/sub_issues").arr(t)
	if len(subs) != 1 || num(t, subs[0], "number") != 1 || str(t, subs[0], "repository_url") != s.URL+api || str(t, subs[0], "title") != "Rate-limit the export endpoint" {
		t.Fatalf("sub-issues of tracker#1 = %v", subs)
	}
	parent := get(t, s, api+"/issues/1/parent").obj(t)
	if num(t, parent, "number") != 1 || str(t, parent, "repository_url") != s.URL+tracker {
		t.Errorf("parent of api#1 = %v", parent)
	}
	expect(t, get(t, s, tracker+"/issues/1/parent"), 404)
	if got, err := s.SubIssues(issueRef("ade-demo/tracker", 10)); err != nil || len(got) != 1 || got[0].String() != "ade-demo/api#3" {
		t.Errorf("SubIssues(tracker#10) = %v, %v", got, err)
	}
	if len(get(t, s, tracker+"/issues/3/sub_issues").arr(t)) != 0 {
		t.Error("tracker#3 has sub-issues")
	}

	idOf := func(path string) int { return num(t, get(t, s, path).obj(t), "id") }
	sub := idOf(api + "/issues/1")
	add := func(parentPath string, id int, extra map[string]any) resp {
		body := map[string]any{"sub_issue_id": id}
		for k, v := range extra {
			body[k] = v
		}
		return call(t, s, "POST", parentPath+"/sub_issues", body)
	}

	// api#1 already has tracker#1 as its parent, and may not be added twice.
	for name, r := range map[string]resp{
		"other parent":    add(tracker+"/issues/3", sub, nil),
		"already a child": add(tracker+"/issues/1", sub, nil),
		"self":            add(tracker+"/issues/3", idOf(tracker+"/issues/3"), nil),
		"pull request":    add(tracker+"/issues/3", idOf(api+"/issues/2"), nil),
	} {
		e := expect(t, r, 422).obj(t)
		if str(t, e, "message") != "Validation Failed" || len(at(t, e, "errors").([]any)) != 1 {
			t.Errorf("%s: %v", name, e)
		}
	}
	expect(t, add(tracker+"/issues/3", 424242, nil), 404)

	// A fresh child attaches, across repos, and the parent comes back.
	web5 := expect(t, call(t, s, "POST", web+"/issues", map[string]any{"title": "Child"}), 201).obj(t)
	r := expect(t, add(tracker+"/issues/3", num(t, web5, "id"), nil), 201).obj(t)
	if num(t, r, "number") != 3 || str(t, r, "repository_url") != s.URL+tracker {
		t.Errorf("POST sub_issues returned %v, want the parent issue", r)
	}
	if got := get(t, s, tracker+"/issues/3/sub_issues").arr(t); len(got) != 1 || str(t, got[0], "repository_url") != s.URL+web {
		t.Errorf("sub-issues = %v", got)
	}
	if got, _ := s.SubIssues(issueRef("ade-demo/tracker", 3)); len(got) != 1 || got[0].Project != "ade-demo/web" || got[0].IID != 5 {
		t.Errorf("SubIssues = %v", got)
	}
	if sm := at(t, get(t, s, tracker+"/issues/3").obj(t), "sub_issues_summary"); num(t, sm, "total") != 1 || num(t, sm, "completed") != 0 {
		t.Errorf("sub_issues_summary = %v", sm)
	}
	// replace_parent moves it.
	moved := expect(t, add(tracker+"/issues/4", num(t, web5, "id"), map[string]any{"replace_parent": true}), 201).obj(t)
	if num(t, moved, "number") != 4 {
		t.Errorf("moved = %v", moved)
	}
	if len(get(t, s, tracker+"/issues/3/sub_issues").arr(t)) != 0 || len(get(t, s, tracker+"/issues/4/sub_issues").arr(t)) != 1 {
		t.Error("replace_parent did not move the child")
	}
}

func TestCommentsAndFeed(t *testing.T) {
	s := newServer(t)

	cs := get(t, s, api+"/issues/2/comments").arr(t)
	if len(cs) != 1 || str(t, cs[0], "user", "login") != "teammate" || !strings.Contains(str(t, cs[0], "body"), "burst") {
		t.Fatalf("PR conversation = %v", cs)
	}
	for _, k := range []string{"id", "node_id", "html_url", "created_at", "updated_at"} {
		if at(t, cs[0], k) == nil {
			t.Errorf("comment lacks %s", k)
		}
	}
	c := expect(t, call(t, s, "POST", api+"/issues/2/comments", map[string]any{"body": "Ship it"}), 201).obj(t)
	if str(t, c, "user", "login") != "demo-user" || str(t, c, "body") != "Ship it" || num(t, c, "id") == num(t, cs[0], "id") {
		t.Errorf("created comment = %v", c)
	}
	if n := len(get(t, s, api+"/issues/2/comments").arr(t)); n != 2 {
		t.Errorf("comments = %d, want 2", n)
	}
	if n := num(t, get(t, s, api+"/issues/2").obj(t), "comments"); n != 2 {
		t.Errorf("comments count = %d", n)
	}
	expect(t, call(t, s, "POST", api+"/issues/2/comments", map[string]any{"body": ""}), 422)
	expect(t, call(t, s, "POST", api+"/issues/99/comments", map[string]any{"body": "x"}), 404)

	t.Run("activity feed", func(t *testing.T) {
		feed := get(t, s, api+"/events").arr(t)
		times := pluck[string](t, feed, "created_at")
		for i := 1; i < len(times); i++ {
			if times[i] > times[i-1] {
				t.Fatalf("feed not ordered newest first: %v", times)
			}
		}
		if feed[0].(map[string]any)["type"] != "IssueCommentEvent" || str(t, feed[0], "payload", "comment", "body") != "Ship it" {
			t.Errorf("newest event = %v, want the comment just posted", feed[0])
		}
		kinds := map[string]bool{}
		var merged, pushed bool
		for _, e := range feed {
			typ := str(t, e, "type")
			kinds[typ] = true
			if str(t, e, "actor", "login") == "" || str(t, e, "id") == "" {
				t.Errorf("event lacks actor/id: %v", e)
			}
			if typ == "PullRequestEvent" && str(t, e, "payload", "action") == "closed" && at(t, e, "payload", "pull_request", "merged") == true {
				merged = true
			}
			if typ == "PushEvent" && strings.HasPrefix(str(t, e, "payload", "ref"), "refs/heads/") && len(at(t, e, "payload", "commits").([]any)) > 0 {
				pushed = true
			}
		}
		for _, k := range []string{"IssuesEvent", "PullRequestEvent", "PushEvent", "IssueCommentEvent"} {
			if !kinds[k] {
				t.Errorf("api feed has no %s", k)
			}
		}
		if !merged || !pushed {
			t.Errorf("feed lacks a merged PullRequestEvent (%v) or a PushEvent with ref+commits (%v)", merged, pushed)
		}
		// Issue closed in api#3 shows as an IssuesEvent closed.
		var closedIssue bool
		for _, e := range feed {
			if str(t, e, "type") == "IssuesEvent" && str(t, e, "payload", "action") == "closed" && num(t, e, "payload", "issue", "number") == 3 {
				closedIssue = true
			}
		}
		if !closedIssue {
			t.Error("no IssuesEvent closed for api#3")
		}
		// Writes append: a new issue opens an event with the issue title.
		call(t, s, "POST", api+"/issues", map[string]any{"title": "Fresh"})
		top := get(t, s, api+"/events").arr(t)[0]
		if str(t, top, "type") != "IssuesEvent" || str(t, top, "payload", "action") != "opened" || str(t, top, "payload", "issue", "title") != "Fresh" {
			t.Errorf("newest event after create = %v", top)
		}
		oldest, _ := time.Parse(time.RFC3339, times[len(times)-1])
		if clock.Sub(oldest) > 30*24*time.Hour {
			t.Errorf("oldest feed event %s is not recent", oldest)
		}
	})
}

func TestInspectorsRejectUnknownRefs(t *testing.T) {
	s := newServer(t)
	bad := []struct {
		name string
		err  error
	}{
		{"labels project", func() error { _, err := s.Labels(issueRef("ade-demo/none", 1)); return err }()},
		{"labels number", func() error { _, err := s.Labels(issueRef("ade-demo/api", 99)); return err }()},
		{"issue as PR", func() error { _, err := s.IsDraft(prRef("ade-demo/api", 1)); return err }()},
		{"publishes", func() error { _, err := s.Publishes(prRef("ade-demo/api", 99)); return err }()},
	}
	for _, b := range bad {
		if b.err == nil {
			t.Errorf("%s: want an error", b.name)
		}
	}
	if s.Branches("ade-demo/none") != nil || s.Milestones("nonsense") != nil || s.Reruns("ade-demo/web", 1) != 0 {
		t.Error("unknown project/run inspectors must be empty")
	}
}
