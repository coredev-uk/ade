package github_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
	"github.com/coredev-uk/ade/internal/forge/github/fake"
)

const (
	trackerProj = "ade-demo/tracker"
	apiProj     = "ade-demo/api"
	webProj     = "ade-demo/web"
)

func issueRef(project string, n int) forge.Ref {
	return forge.Ref{Project: project, IID: n, Kind: forge.KindIssue}
}

func mrRef(project string, n int) forge.Ref {
	return forge.Ref{Project: project, IID: n, Kind: forge.KindMR}
}

// newClient returns a client for a fresh fake server. Retry waits are recorded
// instead of slept.
func newClient(t *testing.T, opts ...fake.Option) (*github.Client, *fake.Server) {
	t.Helper()
	srv := fake.New(opts...)
	t.Cleanup(srv.Close)
	return github.New(srv.URL, fake.Token, github.WithSleep(func(context.Context, time.Duration) error { return nil })), srv
}

func titles(is []forge.Issue) []string {
	out := make([]string, len(is))
	for i, is := range is {
		out[i] = is.Title
	}
	return out
}

func mrTitles(ms []forge.MR) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Ref.String()
	}
	return out
}

func TestMeAndCapabilities(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	me, err := c.Me(ctx)
	if err != nil || me.Username != "demo-user" || me.ID != 1001 {
		t.Fatalf("Me = %+v, %v", me, err)
	}
	if _, err := c.Me(ctx); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range srv.Requests() {
		if strings.HasSuffix(r.Path, "/user") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("GET /user ran %d times; the answer must be kept", n)
	}

	caps, err := c.Capabilities(ctx, "ade-demo")
	if err != nil || caps.Iterations || caps.Cadences || caps.Weight || caps.ApprovalRules || caps.BlocksLinks || caps.Epics || caps.ProbedAt.IsZero() {
		t.Errorf("caps = %+v, %v; GitHub's free tier has none of the gated features", caps, err)
	}
	if _, err := c.Capabilities(ctx, "no-such-owner"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown owner: err = %v, want ErrNotFound", err)
	}
	if _, err := c.Capabilities(ctx, ""); err == nil {
		t.Error("an empty owner was accepted")
	}
	if _, err := c.Iterations(ctx, "ade-demo", ""); !errors.Is(err, forge.ErrUnavailable) {
		t.Errorf("Iterations err = %v, want ErrUnavailable", err)
	}
	if c.Kind() != "github" {
		t.Errorf("Kind = %q", c.Kind())
	}
}

func TestMilestonesAreSprints(t *testing.T) {
	c, _ := newClient(t)
	ms, err := c.Milestones(context.Background(), trackerProj)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		title, start, due, state string
	}{
		{"Sprint 41", "2026-10-19", "2026-10-30", "closed"},
		{"Sprint 42", "2026-11-02", "2026-11-13", "active"},
		{"Sprint 43", "2026-11-16", "2026-11-27", "active"},
	}
	if len(ms) != len(want) {
		t.Fatalf("milestones = %+v", ms)
	}
	for i, w := range want {
		m := ms[i]
		if m.Title != w.title || m.Start.String() != w.start || m.Due.String() != w.due || m.State != w.state || m.ID == 0 {
			t.Errorf("milestone %d = %+v, want %+v", i, m, w)
		}
	}
	for _, bad := range []string{"", "ade-demo", "a/b/c"} {
		if _, err := c.Milestones(context.Background(), bad); err == nil {
			t.Errorf("Milestones(%q) accepted: milestones belong to one repository", bad)
		}
	}
	if _, err := c.Milestones(context.Background(), "ade-demo/nope"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown repo: err = %v, want ErrNotFound", err)
	}
}

func TestListIssues(t *testing.T) {
	c, _ := newClient(t)
	tests := []struct {
		name string
		q    forge.IssueQuery
		want []string // a title substring per expected issue, any order
	}{
		{"open tracker issues", forge.IssueQuery{Project: trackerProj}, []string{
			"Rate-limit", "Paginate", "Retry failed", "Remove the legacy", "timezone", "Billing", "Dark mode", "Scheduled", "stale totals",
		}},
		{"all adds the two closed", forge.IssueQuery{Project: trackerProj, State: "all"}, []string{
			"Rate-limit", "Paginate", "Retry failed", "Remove the legacy", "timezone", "Billing", "Dark mode", "Scheduled", "stale totals", "truncated", "Upgrade",
		}},
		{"closed only", forge.IssueQuery{Project: trackerProj, State: "closed"}, []string{"truncated", "Upgrade"}},
		{"sprint 42 by milestone title", forge.IssueQuery{Project: trackerProj, State: "all", Milestone: "Sprint 42"}, []string{
			"Rate-limit", "Paginate", "Retry failed", "Remove the legacy", "timezone", "Billing", "stale totals", "Upgrade",
		}},
		{"sprint 43 open", forge.IssueQuery{Project: trackerProj, Milestone: "Sprint 43"}, []string{"Dark mode", "Scheduled"}},
		{"unknown milestone matches nothing", forge.IssueQuery{Project: trackerProj, Milestone: "Sprint 99"}, nil},
		{"assignee", forge.IssueQuery{Project: trackerProj, Assignee: "demo-user", Milestone: "Sprint 43"}, []string{"Scheduled"}},
		{"labels must all match", forge.IssueQuery{Project: trackerProj, Labels: []string{"app:api", "weight:5"}}, []string{"Rate-limit", "Retry failed"}},
		{"updated after", forge.IssueQuery{Project: trackerProj, State: "all", UpdatedAfter: time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)}, []string{"Rate-limit"}},
		{"pull requests are not issues", forge.IssueQuery{Project: apiProj, State: "all"}, []string{"Rate-limit", "Upgrade"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.ListIssues(context.Background(), tc.q)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("issues = %q, want %d matching %q", titles(got), len(tc.want), tc.want)
			}
			for _, w := range tc.want {
				if !slices.ContainsFunc(got, func(is forge.Issue) bool { return strings.Contains(is.Title, w) }) {
					t.Errorf("no issue matching %q in %q", w, titles(got))
				}
			}
			for _, is := range got {
				if is.Ref.Kind != forge.KindIssue || is.Ref.Project != tc.q.Project || is.Ref.IID == 0 {
					t.Errorf("ref = %+v", is.Ref)
				}
			}
		})
	}

	t.Run("errors", func(t *testing.T) {
		ctx := context.Background()
		if _, err := c.ListIssues(ctx, forge.IssueQuery{}); err == nil {
			t.Error("no project accepted")
		}
		if _, err := c.ListIssues(ctx, forge.IssueQuery{Project: trackerProj, IterationID: 3}); !errors.Is(err, forge.ErrUnavailable) {
			t.Errorf("iteration filter err = %v, want ErrUnavailable (ignoring it would return every issue)", err)
		}
		if _, err := c.ListIssues(ctx, forge.IssueQuery{Project: trackerProj, State: "weird"}); err == nil {
			t.Error("unknown state accepted")
		}
		if _, err := c.ListIssues(ctx, forge.IssueQuery{Project: "ade-demo/nope"}); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown project err = %v, want ErrNotFound", err)
		}
	})
}

func TestIssue(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	is, err := c.Issue(ctx, issueRef(trackerProj, 1))
	if err != nil {
		t.Fatal(err)
	}
	if is.Title != "Rate-limit the export endpoint" || is.State != "opened" || is.Ref != issueRef(trackerProj, 1) || is.ID == 0 || is.WebURL == "" {
		t.Errorf("issue = %+v", is)
	}
	if !slices.Equal(is.Labels, []string{"type:story", "app:api", "workflow:in review", "weight:5"}) {
		t.Errorf("labels = %v", is.Labels)
	}
	if is.Milestone == nil || is.Milestone.Title != "Sprint 42" || is.Milestone.Start.String() != "2026-11-02" {
		t.Errorf("milestone = %+v", is.Milestone)
	}
	if len(is.Assignees) != 1 || is.Assignees[0].Username != "demo-user" || is.Author.Username == "" {
		t.Errorf("people = %+v / %+v", is.Assignees, is.Author)
	}
	if is.Tasks.Total != 4 || is.Tasks.Done != 1 || is.Weight != nil {
		t.Errorf("tasks = %+v weight = %v; weight rides on labels, never a field", is.Tasks, is.Weight)
	}
	if is.CreatedAt.IsZero() || is.UpdatedAt.IsZero() || is.ClosedAt != nil {
		t.Errorf("times = %v %v %v", is.CreatedAt, is.UpdatedAt, is.ClosedAt)
	}

	done, err := c.Issue(ctx, issueRef(trackerProj, 10))
	if err != nil || done.State != "closed" || done.ClosedAt == nil {
		t.Errorf("closed issue = %+v, %v", done, err)
	}
	if none, err := c.Issue(ctx, issueRef(trackerProj, 11)); err != nil || len(none.Labels) != 0 || none.Milestone == nil {
		t.Errorf("unlabelled issue = %+v, %v", none, err)
	}

	if _, err := c.Issue(ctx, issueRef(trackerProj, 99)); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown issue err = %v, want ErrNotFound", err)
	}
	if _, err := c.Issue(ctx, issueRef(apiProj, 2)); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("a PR number read as an issue: err = %v, want ErrNotFound", err)
	}
	for _, bad := range []forge.Ref{mrRef(apiProj, 2), {}, issueRef("noslash", 1), issueRef(apiProj, 0), issueRef("a/b/c", 1)} {
		if _, err := c.Issue(ctx, bad); err == nil {
			t.Errorf("Issue(%+v) accepted", bad)
		}
	}
}

func TestLinksAreSubIssues(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	tests := []struct {
		name string
		ref  forge.Ref
		want []forge.Link
	}{
		{"parent lists its cross-repo children", issueRef(trackerProj, 1), []forge.Link{{Ref: issueRef(apiProj, 1), Type: "sub_issue"}}},
		{"child names its parent", issueRef(apiProj, 1), []forge.Link{{Ref: issueRef(trackerProj, 1), Type: "parent"}}},
		{"closed child", issueRef(webProj, 3), []forge.Link{{Ref: issueRef(trackerProj, 9), Type: "parent"}}},
		{"no links", issueRef(trackerProj, 3), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.Links(ctx, tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("links = %+v, want %+v", got, tc.want)
			}
		})
	}
	if _, err := c.Links(ctx, issueRef(trackerProj, 99)); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown issue: err = %v, want ErrNotFound", err)
	}
	if _, err := c.Links(ctx, mrRef(apiProj, 2)); err == nil {
		t.Error("an MR ref was accepted")
	}
}

func TestListMRs(t *testing.T) {
	c, _ := newClient(t)
	tests := []struct {
		name string
		q    forge.MRQuery
		want []string
	}{
		{"open in api", forge.MRQuery{Project: apiProj}, []string{"ade-demo/api!2", "ade-demo/api!4", "ade-demo/api!5"}},
		{"all in api", forge.MRQuery{Project: apiProj, State: "all"}, []string{"ade-demo/api!2", "ade-demo/api!4", "ade-demo/api!5", "ade-demo/api!6"}},
		{"merged is not closed", forge.MRQuery{Project: apiProj, State: "merged"}, []string{"ade-demo/api!6"}},
		{"closed excludes merged", forge.MRQuery{Project: apiProj, State: "closed"}, nil},
		{"awaiting my review", forge.MRQuery{Project: apiProj, Reviewer: "demo-user"}, []string{"ade-demo/api!5"}},
		{"by author", forge.MRQuery{Project: apiProj, Author: "teammate", State: "all"}, []string{"ade-demo/api!5"}},
		{"by source branch", forge.MRQuery{Project: apiProj, State: "all", SourceBranch: "rename-bucket-to-token-bucket"}, []string{"ade-demo/api!4"}},
		{"no such branch", forge.MRQuery{Project: apiProj, State: "all", SourceBranch: "nope"}, nil},
		{"updated after stops the listing", forge.MRQuery{Project: apiProj, State: "all", UpdatedAfter: time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)}, []string{"ade-demo/api!2", "ade-demo/api!4", "ade-demo/api!5"}},
		{"web: the draft and the merged", forge.MRQuery{Project: webProj, State: "all"}, []string{"ade-demo/web!2", "ade-demo/web!4"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.ListMRs(context.Background(), tc.q)
			if err != nil {
				t.Fatal(err)
			}
			g := mrTitles(got)
			slices.Sort(g)
			if !slices.Equal(g, tc.want) {
				t.Errorf("MRs = %v, want %v", g, tc.want)
			}
		})
	}

	got, err := c.ListMRs(context.Background(), forge.MRQuery{Project: webProj, State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	by := map[int]forge.MR{}
	for _, m := range got {
		by[m.Ref.IID] = m
	}
	if d := by[2]; !d.Draft || d.State != "opened" || d.SourceBranch != "1-paginate-the-audit-log-view" || d.TargetBranch != "main" || d.HeadSHA == "" || d.Author.Username != "demo-user" {
		t.Errorf("web!2 = %+v", d)
	}
	if m := by[4]; m.State != "merged" || m.MergedAt == nil || m.Draft {
		t.Errorf("web!4 = %+v", m)
	}
	if _, err := c.ListMRs(context.Background(), forge.MRQuery{}); err == nil {
		t.Error("no project accepted")
	}
}

func TestMR(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()

	ready, err := c.MR(ctx, mrRef(apiProj, 2))
	if err != nil {
		t.Fatal(err)
	}
	if ready.Unresolved != 2 || ready.Resolved != 1 {
		t.Errorf("threads = %d unresolved / %d resolved, want 2 / 1 (another user's pending review is invisible)", ready.Unresolved, ready.Resolved)
	}
	if ready.Approvals.Required != -1 || len(ready.Approvals.ApprovedBy) != 0 {
		t.Errorf("approvals = %+v; commenting is not approving and the requirement is unknown", ready.Approvals)
	}
	if p := ready.Pipeline; p == nil || p.Status != "success" || p.ID != 9001 || p.WebURL == "" {
		t.Errorf("pipeline = %+v", p)
	}
	if ready.Draft || ready.State != "opened" || len(ready.Reviewers) != 1 || ready.Reviewers[0].Username != "teammate" {
		t.Errorf("mr = %+v", ready)
	}
	if !strings.Contains(ready.Description, "Closes #1") || ready.HeadSHA == "" {
		t.Errorf("description/head = %q / %q", ready.Description, ready.HeadSHA)
	}

	draft, err := c.MR(ctx, mrRef(webProj, 2))
	if err != nil {
		t.Fatal(err)
	}
	if !draft.Draft || draft.Pipeline == nil || draft.Pipeline.Status != "failed" || draft.Pipeline.ID != 9002 || draft.Unresolved != 0 {
		t.Errorf("draft = %+v pipeline = %+v", draft, draft.Pipeline)
	}
	plain, err := c.MR(ctx, mrRef(apiProj, 4))
	if err != nil || plain.Pipeline != nil {
		t.Errorf("rename PR = %+v, %v: no checks means no pipeline", plain.Pipeline, err)
	}
	merged, err := c.MR(ctx, mrRef(webProj, 4))
	if err != nil || merged.State != "merged" || merged.MergedAt == nil {
		t.Errorf("merged = %+v, %v", merged, err)
	}

	if _, err := c.MR(ctx, mrRef(apiProj, 99)); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown MR err = %v, want ErrNotFound", err)
	}
	if _, err := c.MR(ctx, issueRef(apiProj, 2)); err == nil {
		t.Error("an issue ref was accepted")
	}
}

func TestDiscussions(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	ds, err := c.Discussions(ctx, mrRef(apiProj, 2))
	if err != nil {
		t.Fatal(err)
	}
	var threads, flat []forge.Discussion
	for _, d := range ds {
		if d.Resolvable {
			threads = append(threads, d)
		} else {
			flat = append(flat, d)
		}
	}
	if len(threads) != 3 {
		t.Fatalf("threads = %d, want 3 (the other user's pending thread must not show): %+v", len(threads), ds)
	}
	var burst, resolved *forge.Discussion
	for i, d := range threads {
		switch {
		case strings.Contains(d.Notes[0].Body, "burst of 50"):
			burst = &threads[i]
		case d.Resolved:
			resolved = &threads[i]
		}
		if d.Position == nil || d.Position.Path != "limit/bucket.go" || d.Position.NewLine == 0 || d.Position.OldLine != 0 {
			t.Errorf("thread position = %+v", d.Position)
		}
	}
	if burst == nil || len(burst.Notes) != 2 || burst.Resolved || burst.Position.NewLine != 6 || burst.Notes[1].Author.Username != "demo-user" {
		t.Errorf("burst thread = %+v", burst)
	}
	if resolved == nil || !strings.Contains(resolved.Notes[0].Body, "rename Bucket.Rate") {
		t.Errorf("resolved thread = %+v", resolved)
	}
	if len(flat) != 2 {
		t.Fatalf("conversation = %+v, want the issue comment and the review summary", flat)
	}
	for _, d := range flat {
		if d.Resolved || d.Position != nil || len(d.Notes) != 1 || d.Notes[0].Body == "" || d.Notes[0].CreatedAt.IsZero() {
			t.Errorf("conversation entry = %+v", d)
		}
	}
	if !slices.IsSortedFunc(ds, func(a, b forge.Discussion) int { return a.Notes[0].CreatedAt.Compare(b.Notes[0].CreatedAt) }) {
		t.Error("discussions are not oldest first")
	}

	iss, err := c.Discussions(ctx, issueRef(apiProj, 1))
	if err != nil || len(iss) != 0 {
		t.Errorf("issue discussions = %+v, %v", iss, err)
	}
	if _, err := c.Discussions(ctx, mrRef(apiProj, 99)); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown MR: err = %v, want ErrNotFound", err)
	}
	if _, err := c.Discussions(ctx, forge.Ref{}); err == nil {
		t.Error("a zero ref was accepted")
	}
}

func TestMRsForIssue(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	got, err := c.MRsForIssue(ctx, issueRef(apiProj, 1))
	if err != nil || len(got) != 1 || got[0].Ref != mrRef(apiProj, 2) || got[0].Title != "Rate-limit the export endpoint" {
		t.Errorf("MRsForIssue(api#1) = %+v, %v", got, err)
	}
	none, err := c.MRsForIssue(ctx, issueRef(trackerProj, 3))
	if err != nil || len(none) != 0 {
		t.Errorf("MRsForIssue(tracker#3) = %+v, %v", none, err)
	}
	if _, err := c.MRsForIssue(ctx, issueRef(apiProj, 99)); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown issue: err = %v, want ErrNotFound", err)
	}
	if _, err := c.MRsForIssue(ctx, mrRef(apiProj, 2)); err == nil {
		t.Error("an MR ref was accepted")
	}
}

func TestPaginationAndConcurrencyCap(t *testing.T) {
	c, srv := newClient(t, fake.WithPageSize(2), fake.WithLatency(5*time.Millisecond))
	got, err := c.ListIssues(context.Background(), forge.IssueQuery{Project: trackerProj, State: "all"})
	if err != nil || len(got) != 11 {
		t.Fatalf("issues = %d, %v; want all 11 across pages of 2", len(got), err)
	}
	seen := map[forge.Ref]bool{}
	for _, is := range got {
		if seen[is.Ref] {
			t.Errorf("%s listed twice", is.Ref)
		}
		seen[is.Ref] = true
	}
	pages := 0
	for _, r := range srv.Requests() {
		if strings.HasSuffix(r.Path, "/tracker/issues") {
			pages++
		}
	}
	if pages != 6 {
		t.Errorf("%d list requests, want 6 pages of 2", pages)
	}

	// Many parallel calls stay within the per-client cap.
	_, err = c.MR(context.Background(), mrRef(apiProj, 2))
	if err != nil {
		t.Fatal(err)
	}
	if m := srv.MaxInFlight(); m > 4 {
		t.Errorf("%d requests in flight at once, the cap is 4", m)
	}
}

func TestErrorsAndRateLimits(t *testing.T) {
	ctx := context.Background()
	t.Run("a bad token is ErrAuth", func(t *testing.T) {
		srv := fake.New()
		t.Cleanup(srv.Close)
		c := github.New(srv.URL, "wrong")
		_, err := c.Me(ctx)
		var ae *github.APIError
		if !errors.Is(err, forge.ErrAuth) || !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized {
			t.Errorf("err = %v, want ErrAuth with status 401", err)
		}
		if strings.Contains(err.Error(), "wrong") {
			t.Errorf("the token leaked into the error: %v", err)
		}
	})

	t.Run("a secondary limit is retried after Retry-After", func(t *testing.T) {
		srv := fake.New()
		t.Cleanup(srv.Close)
		var waits []time.Duration
		c := github.New(srv.URL, fake.Token, github.WithSleep(func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }))
		srv.Throttle(2, "7")
		if _, err := c.Me(ctx); err != nil {
			t.Fatal(err)
		}
		if len(waits) != 2 || waits[0] != 7*time.Second || waits[1] != 7*time.Second {
			t.Errorf("waits = %v, want two of 7s", waits)
		}
	})

	t.Run("a primary limit waits for the reset", func(t *testing.T) {
		srv := fake.New()
		t.Cleanup(srv.Close)
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		var waits []time.Duration
		c := github.New(srv.URL, fake.Token,
			github.WithClock(func() time.Time { return now }),
			github.WithSleep(func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }))
		srv.RateLimit(1, now.Add(45*time.Second).Unix())
		if _, err := c.Me(ctx); err != nil {
			t.Fatal(err)
		}
		if len(waits) != 1 || waits[0] != 45*time.Second {
			t.Errorf("waits = %v, want 45s", waits)
		}
	})

	t.Run("retries are bounded and the limit is reported", func(t *testing.T) {
		srv := fake.New()
		t.Cleanup(srv.Close)
		c := github.New(srv.URL, fake.Token, github.WithMaxRetries(1),
			github.WithSleep(func(context.Context, time.Duration) error { return nil }))
		srv.Throttle(10, "1")
		_, err := c.Me(ctx)
		var ae *github.APIError
		if !errors.As(err, &ae) || ae.Status != http.StatusTooManyRequests || ae.RetryAfter != time.Second {
			t.Fatalf("err = %v, want a 429 APIError carrying Retry-After", err)
		}
		if errors.Is(err, forge.ErrAuth) {
			t.Error("a rate limit is not an auth failure")
		}
	})

	t.Run("a wait longer than the cap is not slept", func(t *testing.T) {
		srv := fake.New()
		t.Cleanup(srv.Close)
		slept := false
		c := github.New(srv.URL, fake.Token, github.WithSleep(func(context.Context, time.Duration) error { slept = true; return nil }))
		srv.Throttle(1, "3600")
		if _, err := c.Me(ctx); err == nil || slept {
			t.Errorf("err = %v, slept = %v; an hour-long wait must fail fast", err, slept)
		}
	})

	t.Run("cancellation stops a request", func(t *testing.T) {
		c, _ := newClient(t)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := c.Me(cctx); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}
