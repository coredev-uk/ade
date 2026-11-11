package github

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

func TestCheckStatus(t *testing.T) {
	tests := []struct {
		status, conclusion, want string
	}{
		{"completed", "success", "success"},
		{"completed", "neutral", "success"},
		{"completed", "skipped", "skipped"},
		{"completed", "failure", "failed"},
		{"completed", "timed_out", "failed"},
		{"completed", "startup_failure", "failed"},
		{"completed", "cancelled", "canceled"},
		{"completed", "action_required", "manual"},
		{"completed", "", "pending"},
		{"in_progress", "", "running"},
		{"queued", "", "pending"},
		{"waiting", "", "pending"},
	}
	for _, tc := range tests {
		if got := checkStatus(tc.status, tc.conclusion); got != tc.want {
			t.Errorf("checkStatus(%q, %q) = %q, want %q", tc.status, tc.conclusion, got, tc.want)
		}
	}
	for state, want := range map[string]string{"success": "success", "pending": "pending", "failure": "failed", "error": "failed"} {
		if got := statusState(state); got != want {
			t.Errorf("statusState(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestWorstStatus(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"success", "success"}, "success"},
		{[]string{"success", "skipped"}, "success"},
		{[]string{"skipped", "skipped"}, "skipped"},
		{[]string{"success", "failed", "running"}, "failed"},
		{[]string{"success", "running", "pending"}, "running"},
		{[]string{"success", "pending"}, "pending"},
		{[]string{"success", "canceled"}, "canceled"},
		{[]string{"canceled", "manual"}, "manual"},
	}
	for _, tc := range tests {
		if got := worst(tc.in); got != tc.want {
			t.Errorf("worst(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMilestoneStartFromDescription(t *testing.T) {
	due := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name, desc, wantStart string
	}{
		{"first line", "Start: 2026-11-02\n\nSprint 42", "2026-11-02"},
		{"lower case, equals sign", "start = 2026-11-02", "2026-11-02"},
		{"start date wording", "Sprint 42\nStart date: 2026-11-02\n", "2026-11-02"},
		{"indented", "  Start: 2026-11-02", "2026-11-02"},
		{"CRLF", "Start: 2026-11-02\r\nrest", "2026-11-02"},
		{"first one wins", "Start: 2026-11-02\nStart: 2026-12-01", "2026-11-02"},
		{"mid-sentence is prose", "We start: 2026-11-02 maybe", ""},
		{"no date", "Sprint 42", ""},
		{"invalid date", "Start: 2026-13-45", ""},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		m := wireMilestone{ID: 7, Title: "Sprint 42", Description: tc.desc, State: "open", DueOn: &due}.milestone()
		if m.Start.String() != tc.wantStart || m.Due.String() != "2026-11-13" || m.State != "active" || m.ID != 7 {
			t.Errorf("%s: milestone = %+v, want start %q", tc.name, m, tc.wantStart)
		}
	}
	if m := (wireMilestone{State: "closed"}).milestone(); m.State != "closed" || !m.Due.IsZero() {
		t.Errorf("closed milestone without a due date = %+v", m)
	}
	// A due date late in the evening of a western time zone is the same UTC day.
	late := time.Date(2026, 11, 13, 23, 0, 0, 0, time.FixedZone("x", -8*3600))
	if m := (wireMilestone{DueOn: &late}).milestone(); m.Due.String() != "2026-11-14" {
		t.Errorf("due = %s: GitHub dates are read in UTC", m.Due)
	}
}

func TestTaskCounts(t *testing.T) {
	body := "- [x] done\n- [ ] open\n* [X] star done\n1. [ ] numbered\n  - [x] nested\nnot a task [ ]\n- [] malformed\n"
	if done, total := taskCounts(body); done != 3 || total != 5 {
		t.Errorf("taskCounts = %d/%d, want 3/5", done, total)
	}
	if done, total := taskCounts(""); done != 0 || total != 0 {
		t.Errorf("empty body = %d/%d", done, total)
	}
}

func TestAnchor(t *testing.T) {
	tests := []struct {
		name     string
		p        forge.DiffPosition
		wantPath string
		wantLine int
		wantSide string
		wantErr  bool
	}{
		{"added line", forge.DiffPosition{NewPath: "b.go", OldPath: "a.go", NewLine: 7}, "b.go", 7, "RIGHT", false},
		{"removed line", forge.DiffPosition{NewPath: "b.go", OldPath: "a.go", OldLine: 3}, "b.go", 3, "LEFT", false},
		{"unchanged line is anchored on the new side", forge.DiffPosition{NewPath: "b.go", OldPath: "a.go", OldLine: 3, NewLine: 5}, "b.go", 5, "RIGHT", false},
		{"deleted file keeps its path", forge.DiffPosition{OldPath: "a.go", OldLine: 2}, "a.go", 2, "LEFT", false},
		{"no path", forge.DiffPosition{NewLine: 1}, "", 0, "", true},
		{"no line", forge.DiffPosition{NewPath: "b.go"}, "", 0, "", true},
	}
	for _, tc := range tests {
		path, line, side, err := anchor(&tc.p)
		if (err != nil) != tc.wantErr || path != tc.wantPath || line != tc.wantLine || side != tc.wantSide {
			t.Errorf("%s: anchor = %q %d %s, %v", tc.name, path, line, side, err)
		}
	}
}

func TestErrorMessage(t *testing.T) {
	tests := []struct {
		name, raw, want string
	}{
		{"plain", `{"message":"Not Found"}`, "Not Found"},
		{
			"validation object", `{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"A pull request already exists for a:b."}]}`,
			"Validation Failed: A pull request already exists for a:b.",
		},
		{
			"string errors", `{"message":"Unprocessable Entity","errors":["Can not approve your own pull request"]}`,
			"Unprocessable Entity: Can not approve your own pull request",
		},
		{
			"field and code", `{"message":"Validation Failed","errors":[{"resource":"Issue","field":"title","code":"missing_field"}]}`,
			"Validation Failed: title missing_field",
		},
		{"not json", "  Bad gateway \n", "Bad gateway"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		if got := errorMessage([]byte(tc.raw)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRateLimitWait(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	hdr := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	tests := []struct {
		name    string
		status  int
		h       http.Header
		attempt int
		want    time.Duration
		limited bool
	}{
		{"429 with seconds", 429, hdr("Retry-After", "7"), 0, 7 * time.Second, true},
		{"403 with Retry-After is a secondary limit", 403, hdr("Retry-After", "30"), 0, 30 * time.Second, true},
		{"429 with an HTTP date", 429, hdr("Retry-After", now.Add(20*time.Second).Format(http.TimeFormat)), 0, 20 * time.Second, true},
		{"429 without a hint backs off exponentially", 429, hdr(), 2, 4 * time.Second, true},
		{"backoff is capped", 429, hdr(), 30, 64 * time.Second, true},
		{"primary limit waits for the reset", 403, hdr("X-RateLimit-Remaining", "0", "X-RateLimit-Reset", "1794398460"), 0, time.Unix(1794398460, 0).Sub(now), true},
		{"a reset in the past waits nothing", 403, hdr("X-RateLimit-Remaining", "0", "X-RateLimit-Reset", "1"), 0, 0, true},
		{"403 with quota left is a permission error", 403, hdr("X-RateLimit-Remaining", "12"), 0, 0, false},
		{"404 is never a limit", 404, hdr("Retry-After", "5"), 0, 0, false},
		{"500 is not retried", 500, hdr(), 0, 0, false},
	}
	for _, tc := range tests {
		got, limited := rateLimitWait(&http.Response{StatusCode: tc.status, Header: tc.h}, now, tc.attempt)
		if limited != tc.limited || (limited && got != tc.want) {
			t.Errorf("%s: wait = %v, limited = %v; want %v, %v", tc.name, got, limited, tc.want, tc.limited)
		}
	}
}

func TestReadTail(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"whole log", "abcdef", 0, "abcdef"},
		{"tail", "abcdef", 3, "def"},
		{"shorter than the tail", "abc", 10, "abc"},
		{"never starts inside a character", "aé€b", 3, "b"},
		{"a whole character at the cut is kept", "aé€b", 4, "€b"},
		{"empty", "", 5, ""},
	}
	for _, tc := range tests {
		got, err := readTail(strings.NewReader(tc.in), tc.n)
		if err != nil || got != tc.want {
			t.Errorf("%s: readTail = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	big := strings.Repeat("x", 100_000) + "END"
	if got, _ := readTail(strings.NewReader(big), 5); got != "xxEND" {
		t.Errorf("tail of a long stream = %q", got)
	}
}

func TestNextPage(t *testing.T) {
	tests := []struct {
		link string
		want int
	}{
		{`<https://api.github.com/repositories/1/issues?per_page=100&page=2>; rel="next", <https://api.github.com/repositories/1/issues?page=9>; rel="last"`, 2},
		{`<https://api.github.com/x?page=1>; rel="prev", <https://api.github.com/x?page=4>; rel="next"`, 4},
		{`<https://api.github.com/x?page=9>; rel="last"`, 0},
		{``, 0},
		{`garbage`, 0},
	}
	for _, tc := range tests {
		h := http.Header{}
		if tc.link != "" {
			h.Set("Link", tc.link)
		}
		if got := nextPage(h); got != tc.want {
			t.Errorf("nextPage(%q) = %d, want %d", tc.link, got, tc.want)
		}
	}
}

func TestPathsAndRepos(t *testing.T) {
	if got := repoPath("ade-demo/api"); got != "/repos/ade-demo/api" {
		t.Errorf("repoPath = %q", got)
	}
	if got := escapeRefPath("feature/a b#c"); got != "feature/a%20b%23c" {
		t.Errorf("escapeRefPath = %q", got)
	}
	repo := map[string]string{
		"https://api.github.com/repos/o/r":              "o/r",
		"https://ghe.example/api/v3/repos/o/r":          "o/r",
		"https://api.github.com/repos/o/r/extra":        "fallback",
		"":                                              "fallback",
		"https://api.github.com/orgs/o":                 "fallback",
		"http://127.0.0.1:1234/api/v3/repos/team/thing": "team/thing",
	}
	for in, want := range repo {
		if got := repoOf(in, "fallback"); got != want {
			t.Errorf("repoOf(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []forge.Ref{{}, {Project: "a", IID: 1}, {Project: "a/b/c", IID: 1}, {Project: "a/b", IID: 0}, {Project: "/b", IID: 1}} {
		if wantKind(bad, bad.Kind) == nil {
			t.Errorf("wantKind(%+v) accepted", bad)
		}
	}
	if wantKind(forge.Ref{Project: "a/b", IID: 3, Kind: forge.KindMR}, forge.KindIssue) == nil {
		t.Error("a kind mismatch was accepted")
	}
}

func TestEndpoints(t *testing.T) {
	tests := []struct {
		base, rest, gql, web string
	}{
		{"", "https://api.github.com", "https://api.github.com/graphql", "https://github.com"},
		{"https://github.com/", "https://api.github.com", "https://api.github.com/graphql", "https://github.com"},
		{"https://ghe.example.com", "https://ghe.example.com/api/v3", "https://ghe.example.com/api/graphql", "https://ghe.example.com"},
	}
	for _, tc := range tests {
		c := New(tc.base, "t")
		if c.rest != tc.rest || c.gql != tc.gql || c.webBase() != tc.web {
			t.Errorf("New(%q): rest=%s gql=%s web=%s", tc.base, c.rest, c.gql, c.webBase())
		}
	}
}

func TestGraphQLErrorsMapToSentinels(t *testing.T) {
	tests := []struct {
		typ  string
		want error
	}{
		{"NOT_FOUND", forge.ErrNotFound},
		{"FORBIDDEN", forge.ErrAuth},
		{"INSUFFICIENT_SCOPES", forge.ErrAuth},
	}
	for _, tc := range tests {
		err := graphqlError(GraphQLError{Type: tc.typ, Message: "m"})
		if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), "m") {
			t.Errorf("%s: %v does not unwrap to %v", tc.typ, err, tc.want)
		}
	}
	if err := graphqlError(GraphQLError{Type: "UNPROCESSABLE", Message: "m"}); errors.Is(err, forge.ErrNotFound) || errors.Is(err, forge.ErrAuth) {
		t.Errorf("an unprocessable error must not look like a missing thing: %v", err)
	}
	if got := operationName("mutation ResolveThread($id: ID!) {"); got != "ResolveThread" {
		t.Errorf("operationName = %q", got)
	}
	if got := operationName("query IssuePRs {"); got != "IssuePRs" {
		t.Errorf("operationName = %q", got)
	}
}

func TestEventMapping(t *testing.T) {
	mk := func(typ, action string, mut func(*wireEvent)) wireEvent {
		w := wireEvent{ID: "42", Type: typ, CreatedAt: time.Now()}
		w.Payload.Action = action
		if mut != nil {
			mut(&w)
		}
		return w
	}
	issue := func(w *wireEvent) {
		w.Payload.Issue = &struct {
			Number      int       `json:"number"`
			Title       string    `json:"title"`
			PullRequest *struct{} `json:"pull_request"`
		}{Number: 4, Title: "T"}
	}
	tests := []struct {
		name       string
		w          wireEvent
		action, tt string
		target     forge.Ref
		ok         bool
	}{
		{"issue opened", mk("IssuesEvent", "opened", issue), "opened", "Issue", forge.Ref{Project: "o/r", IID: 4, Kind: forge.KindIssue}, true},
		{"comment on an issue", mk("IssueCommentEvent", "created", issue), "commented on", "Note", forge.Ref{Project: "o/r", IID: 4, Kind: forge.KindIssue}, true},
		{"watch events are noise", mk("WatchEvent", "started", nil), "", "", forge.Ref{}, false},
		{"issue event without an issue", mk("IssuesEvent", "opened", nil), "", "", forge.Ref{}, false},
	}
	for _, tc := range tests {
		e, ok := tc.w.event("o/r")
		if ok != tc.ok || (ok && (e.Action != tc.action || e.TargetType != tc.tt || e.Target != tc.target || e.ID != 42)) {
			t.Errorf("%s: %+v, %v", tc.name, e, ok)
		}
	}
	pr := mk("PullRequestEvent", "closed", func(w *wireEvent) {
		w.Payload.PullRequest = &struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			Merged bool   `json:"merged"`
		}{Number: 9, Title: "P", Merged: true}
	})
	if e, ok := pr.event("o/r"); !ok || e.Action != "accepted" || e.Target.Kind != forge.KindMR {
		t.Errorf("merged PR = %+v, %v; a merge reads as GitLab's \"accepted\"", e, ok)
	}
	pr.Payload.PullRequest.Merged = false
	if e, _ := pr.event("o/r"); e.Action != "closed" {
		t.Errorf("closed unmerged PR action = %q", e.Action)
	}
	push := mk("PushEvent", "", func(w *wireEvent) { w.Payload.Ref = "refs/heads/feature/x" })
	if e, ok := push.event("o/r"); !ok || e.Action != "pushed to" || e.Branch != "feature/x" {
		t.Errorf("push = %+v, %v", e, ok)
	}
}

func TestApprovalsReduction(t *testing.T) {
	u := func(id int64, name string) wireUser { return wireUser{ID: id, Login: name} }
	rv := func(user wireUser, state string) wireReview { return wireReview{User: user, State: state} }
	tests := []struct {
		name    string
		reviews []wireReview
		want    []string
	}{
		{"approved", []wireReview{rv(u(1, "a"), "APPROVED")}, []string{"a"}},
		{"a later comment keeps the approval", []wireReview{rv(u(1, "a"), "APPROVED"), rv(u(1, "a"), "COMMENTED")}, []string{"a"}},
		{"changes requested after approval withdraws it", []wireReview{rv(u(1, "a"), "APPROVED"), rv(u(1, "a"), "CHANGES_REQUESTED")}, nil},
		{"dismissed", []wireReview{rv(u(1, "a"), "APPROVED"), rv(u(1, "a"), "DISMISSED")}, nil},
		{"re-approved", []wireReview{rv(u(1, "a"), "CHANGES_REQUESTED"), rv(u(1, "a"), "APPROVED")}, []string{"a"}},
		{"pending reviews count for nothing", []wireReview{rv(u(1, "a"), "PENDING")}, nil},
		{"two reviewers keep first-seen order", []wireReview{rv(u(2, "b"), "APPROVED"), rv(u(1, "a"), "APPROVED")}, []string{"b", "a"}},
	}
	for _, tc := range tests {
		got := approvals(tc.reviews)
		var names []string
		for _, u := range got.ApprovedBy {
			names = append(names, u.Username)
		}
		if got.Required != -1 || strings.Join(names, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: approvals = %+v, want %v", tc.name, got, tc.want)
		}
	}
}
