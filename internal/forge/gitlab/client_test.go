package gitlab_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

const (
	group   = fake.GroupPath
	tracker = group + "/tracker"
	apiProj = group + "/api"
	webProj = group + "/web"
)

var (
	tiers = []fake.Tier{fake.Free, fake.Premium}
	clock = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
)

// forEachTier runs fn against a fresh fake of each tier.
func forEachTier(t *testing.T, fn func(t *testing.T, tier fake.Tier, c *gitlab.Client, srv *fake.Server)) {
	t.Helper()
	for _, tier := range tiers {
		t.Run(tier.String(), func(t *testing.T) {
			srv := fake.New(tier, fake.WithClock(clock))
			t.Cleanup(srv.Close)
			c := gitlab.New(srv.URL, fake.Token, gitlab.WithClock(func() time.Time { return clock }))
			fn(t, tier, c, srv)
		})
	}
}

func issueRef(project string, iid int) forge.Ref {
	return forge.Ref{Project: project, IID: iid, Kind: forge.KindIssue}
}

func mrRef(project string, iid int) forge.Ref {
	return forge.Ref{Project: project, IID: iid, Kind: forge.KindMR}
}

func iids(issues []forge.Issue) []int {
	out := make([]int, len(issues))
	for i, is := range issues {
		out[i] = is.Ref.IID
	}
	return out
}

func sameSet[T comparable](t *testing.T, what string, got, want []T) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	seen := map[T]int{}
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		if seen[w]--; seen[w] < 0 {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}

func TestKindAndMe(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		if c.Kind() != "gitlab" {
			t.Errorf("Kind = %q", c.Kind())
		}
		me, err := c.Me(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if me.Username != "sandbox-dev" || me.ID == 0 {
			t.Errorf("Me = %+v", me)
		}
	})
}

func TestListIssues(t *testing.T) {
	forEachTier(t, func(t *testing.T, tier fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		all, err := c.ListIssues(ctx, forge.IssueQuery{Project: tracker, State: "all"})
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 11 {
			t.Fatalf("tracker has %d issues, want 11", len(all))
		}

		tests := []struct {
			name string
			q    forge.IssueQuery
			want []int
		}{
			{"default is opened", forge.IssueQuery{}, []int{1, 2, 3, 4, 5, 6, 7, 8, 11}},
			{"closed", forge.IssueQuery{State: "closed"}, []int{9, 10}},
			{"milestone title", forge.IssueQuery{Milestone: "Sprint 42"}, []int{1, 2, 3, 4, 5, 6, 11}},
			{"previous sprint, all states", forge.IssueQuery{Milestone: "Sprint 41", State: "all"}, []int{9}},
			{"labels must all match", forge.IssueQuery{Labels: []string{"type::bug", "app::web"}}, []int{2}},
			{"label with space", forge.IssueQuery{Labels: []string{"workflow::in progress"}}, []int{2}},
			{"assignee", forge.IssueQuery{Assignee: "sandbox-dev"}, []int{1, 2, 3, 4, 5, 8, 11}},
			{"assignee nobody", forge.IssueQuery{Assignee: "ghost"}, nil},
			{"updated after", forge.IssueQuery{UpdatedAfter: clock.Add(-60 * time.Hour)}, []int{1, 2}},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				q := tc.q
				q.Project = tracker
				got, err := c.ListIssues(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				sameSet(t, "iids", iids(got), tc.want)
			})
		}

		first := byIID(t, all, 1)
		if first.Ref != issueRef(tracker, 1) || first.State != "opened" {
			t.Errorf("issue 1 ref/state = %v %q", first.Ref, first.State)
		}
		if first.Tasks != (forge.TaskCount{Done: 1, Total: 4}) {
			t.Errorf("tasks = %+v, want 1/4", first.Tasks)
		}
		if m := first.Milestone; m == nil || m.Title != "Sprint 42" || m.Start.String() != "2026-11-02" || m.Due.String() != "2026-11-13" {
			t.Errorf("milestone = %+v", m)
		}
		if first.Author.Username != "sandbox-dev" || len(first.Assignees) != 1 {
			t.Errorf("author/assignees = %+v %+v", first.Author, first.Assignees)
		}
		if closed := byIID(t, all, 9); closed.ClosedAt == nil || closed.State != "closed" {
			t.Errorf("closed issue = %+v", closed)
		}
		if got := byIID(t, all, 5).Labels; len(got) != 3 {
			t.Errorf("labels = %v", got)
		}
		checkPlanningFields(t, tier, all)
	})
}

// checkPlanningFields asserts the tier-gated fields: absent on Free, present on
// Premium with one issue left unweighted.
func checkPlanningFields(t *testing.T, tier fake.Tier, all []forge.Issue) {
	t.Helper()
	unweighted := 0
	for _, is := range all {
		if is.Weight == nil {
			unweighted++
		} else if *is.Weight == 0 {
			t.Errorf("%v: weight 0 must be nil or positive", is.Ref)
		}
		if tier == fake.Free && (is.Weight != nil || is.Iteration != nil || is.Epic != "") {
			t.Errorf("%v: Free carries premium fields: %+v", is.Ref, is)
		}
	}
	if tier == fake.Free {
		if unweighted != len(all) {
			t.Errorf("Free: %d unweighted of %d", unweighted, len(all))
		}
		return
	}
	if unweighted != 1 {
		t.Errorf("Premium: %d unweighted issues, want 1", unweighted)
	}
	if w := byIID(t, all, 8).Weight; w == nil || *w != 8 {
		t.Errorf("tracker#8 weight = %v", w)
	}
	if byIID(t, all, 6).Weight != nil {
		t.Error("tracker#6 should be unweighted")
	}
	sched := byIID(t, all, 8)
	if sched.Epic != "Self-serve exports" {
		t.Errorf("epic = %q", sched.Epic)
	}
	if it := sched.Iteration; it == nil || it.State != "upcoming" || it.Title != "" || it.Start.String() != "2026-11-16" {
		t.Errorf("next-sprint iteration = %+v", it)
	}
	if it := byIID(t, all, 1).Iteration; it == nil || it.State != "current" || it.Due.String() != "2026-11-15" {
		t.Errorf("current iteration = %+v", it)
	}
	if it := byIID(t, all, 9).Iteration; it == nil || it.State != "closed" {
		t.Errorf("previous iteration = %+v", it)
	}
}

func byIID(t *testing.T, issues []forge.Issue, iid int) forge.Issue {
	t.Helper()
	for _, is := range issues {
		if is.Ref.IID == iid {
			return is
		}
	}
	t.Fatalf("no issue #%d in %v", iid, iids(issues))
	return forge.Issue{}
}

func TestListIssuesByIteration(t *testing.T) {
	srv := fake.New(fake.Premium, fake.WithClock(clock))
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, fake.Token, gitlab.WithClock(func() time.Time { return clock }))
	ctx := context.Background()
	its, err := c.Iterations(ctx, group, "Delivery")
	if err != nil {
		t.Fatal(err)
	}
	var current forge.Iteration
	for _, it := range its {
		if it.State == "current" {
			current = it
		}
	}
	got, err := c.ListIssues(ctx, forge.IssueQuery{Project: tracker, IterationID: current.ID, State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	sameSet(t, "iids", iids(got), []int{1, 2, 3, 4, 5, 6, 10, 11})
}

func TestListIssuesByIterationOnFree(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, fake.Token)
	got, err := c.ListIssues(context.Background(), forge.IssueQuery{Project: tracker, IterationID: 123, State: "all"})
	if err != nil || len(got) != 0 {
		t.Errorf("Free has no iterations to match: %v, %v", iids(got), err)
	}
}

func TestListIssuesRequiresProject(t *testing.T) {
	c := gitlab.New("http://127.0.0.1:1", "x")
	if _, err := c.ListIssues(context.Background(), forge.IssueQuery{}); err == nil {
		t.Fatal("want error without a project")
	}
}

func TestListAssignedIssues(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		got, err := c.ListAssignedIssues(context.Background(), forge.IssueQuery{})
		if err != nil {
			t.Fatal(err)
		}
		var refs []string
		for _, is := range got {
			refs = append(refs, is.Ref.String())
		}
		sameSet(t, "refs", refs, []string{
			tracker + "#1", tracker + "#2", tracker + "#3", tracker + "#4", tracker + "#5", tracker + "#8", tracker + "#11",
			apiProj + "#1", webProj + "#1",
		})
		last := srv.Requests()[len(srv.Requests())-1]
		if !strings.Contains(last.Query, "scope=assigned_to_me") || strings.Contains(last.Query, "scope=all") {
			t.Errorf("global listing must use assigned_to_me, got %q", last.Query)
		}
	})
}

func TestIssue(t *testing.T) {
	forEachTier(t, func(t *testing.T, tier fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		is, err := c.Issue(ctx, issueRef(tracker, 8))
		if err != nil {
			t.Fatal(err)
		}
		if is.Title != "Scheduled exports" || is.Ref != issueRef(tracker, 8) || is.ID == 0 || is.WebURL == "" {
			t.Errorf("issue = %+v", is)
		}
		if tier == fake.Premium && (is.Weight == nil || *is.Weight != 8) {
			t.Errorf("weight = %v", is.Weight)
		}
		if _, err := c.Issue(ctx, issueRef(tracker, 999)); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("missing issue err = %v, want ErrNotFound", err)
		}
		if _, err := c.Issue(ctx, issueRef("nope/nope", 1)); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("missing project err = %v, want ErrNotFound", err)
		}
		if _, err := c.Issue(ctx, mrRef(tracker, 1)); err == nil {
			t.Error("an MR ref must not be accepted as an issue")
		}
	})
}

func TestLinks(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		got, err := c.Links(ctx, issueRef(tracker, 1))
		if err != nil {
			t.Fatal(err)
		}
		want := []forge.Link{{Ref: issueRef(apiProj, 1), Type: "relates_to"}}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("links of tracker#1 = %+v, want %+v", got, want)
		}
		// Seen from the other side the link points back at the tracker.
		back, err := c.Links(ctx, issueRef(apiProj, 1))
		if err != nil {
			t.Fatal(err)
		}
		if len(back) != 1 || back[0] != (forge.Link{Ref: issueRef(tracker, 1), Type: "relates_to"}) {
			t.Errorf("links of api#1 = %+v", back)
		}
		none, err := c.Links(ctx, issueRef(tracker, 3))
		if err != nil || len(none) != 0 {
			t.Errorf("tracker#3 links = %+v, %v", none, err)
		}
		if _, err := c.Links(ctx, issueRef(tracker, 999)); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestListMRs(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		tests := []struct {
			name string
			q    forge.MRQuery
			want []string
		}{
			{"api opened", forge.MRQuery{Project: apiProj}, []string{apiProj + "!1", apiProj + "!2"}},
			{"web default is opened", forge.MRQuery{Project: webProj}, []string{webProj + "!1"}},
			{"web all", forge.MRQuery{Project: webProj, State: "all"}, []string{webProj + "!1", webProj + "!2"}},
			{"web merged", forge.MRQuery{Project: webProj, State: "merged"}, []string{webProj + "!2"}},
			{"reviewer", forge.MRQuery{Project: apiProj, Reviewer: "reviewer-rae"}, []string{apiProj + "!1"}},
			{"reviewer nobody", forge.MRQuery{Project: webProj, Reviewer: "reviewer-rae", State: "all"}, nil},
			{"author", forge.MRQuery{Project: webProj, Author: "sandbox-dev", State: "all"}, []string{webProj + "!1", webProj + "!2"}},
			{"assignee", forge.MRQuery{Project: apiProj, Assignee: "sandbox-dev"}, []string{apiProj + "!1", apiProj + "!2"}},
			{"source branch", forge.MRQuery{Project: webProj, State: "all", SourceBranch: "2-fix-truncated-column-headers-in-export-preview"}, []string{webProj + "!2"}},
			{"updated after", forge.MRQuery{Project: webProj, State: "all", UpdatedAfter: clock.Add(-48 * time.Hour)}, []string{webProj + "!1"}},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, err := c.ListMRs(ctx, tc.q)
				if err != nil {
					t.Fatal(err)
				}
				var refs []string
				for _, m := range got {
					refs = append(refs, m.Ref.String())
				}
				sameSet(t, "refs", refs, tc.want)
			})
		}
		drafts, _ := c.ListMRs(ctx, forge.MRQuery{Project: webProj})
		if len(drafts) != 1 || !drafts[0].Draft || drafts[0].Pipeline == nil || drafts[0].Pipeline.Status != "failed" {
			t.Errorf("web!1 from list = %+v", drafts)
		}
		if _, err := c.ListMRs(ctx, forge.MRQuery{}); err == nil {
			t.Error("want error without a project")
		}
	})
}

func TestMR(t *testing.T) {
	forEachTier(t, func(t *testing.T, tier fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		m, err := c.MR(ctx, mrRef(apiProj, 1))
		if err != nil {
			t.Fatal(err)
		}
		if m.Ref != mrRef(apiProj, 1) || m.State != "opened" || m.Draft || m.HeadSHA == "" || m.TargetBranch != "main" {
			t.Errorf("api!1 = %+v", m)
		}
		if m.Unresolved != 2 || m.Resolved != 1 {
			t.Errorf("threads = %d unresolved / %d resolved, want 2/1 (system notes must not count)", m.Unresolved, m.Resolved)
		}
		if m.Pipeline == nil || m.Pipeline.Status != "success" || m.Pipeline.ID == 0 {
			t.Errorf("pipeline = %+v", m.Pipeline)
		}
		if len(m.Reviewers) != 1 || len(m.Assignees) != 1 {
			t.Errorf("reviewers/assignees = %+v %+v", m.Reviewers, m.Assignees)
		}
		checkApprovals(t, tier, m)

		web1, err := c.MR(ctx, mrRef(webProj, 1))
		if err != nil {
			t.Fatal(err)
		}
		if !web1.Draft || web1.Pipeline == nil || web1.Pipeline.Status != "failed" || web1.DetailedStatus != "draft_status" {
			t.Errorf("web!1 = %+v", web1)
		}
		web2, err := c.MR(ctx, mrRef(webProj, 2))
		if err != nil {
			t.Fatal(err)
		}
		if web2.State != "merged" || web2.MergedAt == nil || web2.Pipeline != nil || web2.Unresolved != 0 {
			t.Errorf("web!2 = %+v", web2)
		}

		if _, err := c.MR(ctx, mrRef(webProj, 99)); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("missing MR err = %v, want ErrNotFound", err)
		}
		if _, err := c.MR(ctx, issueRef(webProj, 1)); err == nil {
			t.Error("an issue ref must not be accepted as an MR")
		}
	})
}

// TestMRReviewerVerdictsThreadsAndMerger covers what sync events read from an
// MR: who requested changes, who wrote each note, and who merged.
func TestMRReviewerVerdictsThreadsAndMerger(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 1)
		m, err := c.MR(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.ChangesRequestedBy) != 0 || m.MergedBy != "" {
			t.Errorf("fresh MR: changes %+v merged by %q", m.ChangesRequestedBy, m.MergedBy)
		}
		// Threads: rae's two open threads and her resolved nit; the first has my reply.
		// The system note is not conversation.
		if m.Threads == nil || len(m.Threads) != 3 {
			t.Fatalf("threads = %+v, want 3 conversations (the system note is skipped)", m.Threads)
		}
		if a := m.Threads[0].Authors; len(a) != 2 || a[0].Username != "reviewer-rae" || a[1].Username != "sandbox-dev" {
			t.Errorf("first thread authors = %v", a)
		}

		if err := srv.SetReviewState(ref, "reviewer-rae", "requested_changes"); err != nil {
			t.Fatal(err)
		}
		if m, err = c.MR(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if len(m.ChangesRequestedBy) != 1 || m.ChangesRequestedBy[0].Username != "reviewer-rae" {
			t.Errorf("changes requested by %+v", m.ChangesRequestedBy)
		}
		if err := srv.SetReviewState(ref, "reviewer-rae", "approved"); err != nil {
			t.Fatal(err)
		}
		if m, _ = c.MR(ctx, ref); len(m.ChangesRequestedBy) != 0 {
			t.Errorf("an approving reviewer still asks for changes: %+v", m.ChangesRequestedBy)
		}

		if err := srv.MergeMRAs(ref, "reviewer-rae"); err != nil {
			t.Fatal(err)
		}
		if m, _ = c.MR(ctx, ref); m.State != "merged" || m.MergedBy != "reviewer-rae" {
			t.Errorf("merged MR = %q by %q", m.State, m.MergedBy)
		}
		// The list payload carries the merger too: merged MRs get no detail fetch.
		list, err := c.ListMRs(ctx, forge.MRQuery{Project: apiProj, State: "merged"})
		if err != nil || len(list) != 1 || list[0].MergedBy != "reviewer-rae" {
			t.Errorf("merged list = %+v %v", list, err)
		}
	})
}

func checkApprovals(t *testing.T, tier fake.Tier, m forge.MR) {
	t.Helper()
	switch tier {
	case fake.Free:
		if m.Approvals.Required != -1 || len(m.Approvals.ApprovedBy) != 0 {
			t.Errorf("Free approvals = %+v, want Required -1", m.Approvals)
		}
		if m.DetailedStatus != "mergeable" {
			t.Errorf("detailed status = %q", m.DetailedStatus)
		}
	case fake.Premium:
		if m.Approvals.Required != 2 || len(m.Approvals.ApprovedBy) != 1 || m.Approvals.ApprovedBy[0].Username != "reviewer-rae" {
			t.Errorf("Premium approvals = %+v, want 2 required, rae approved", m.Approvals)
		}
		if m.DetailedStatus != "not_approved" {
			t.Errorf("detailed status = %q", m.DetailedStatus)
		}
	}
}

func TestMRReflectsChanges(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 1)
		d, err := srv.AddNote(ref, "", "reviewer-rae", "One more thing.")
		if err != nil {
			t.Fatal(err)
		}
		if err := srv.SetPipelineStatus(ref, "failed"); err != nil {
			t.Fatal(err)
		}
		m, err := c.MR(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if m.Unresolved != 3 || m.Pipeline.Status != "failed" {
			t.Errorf("after note + failed CI: unresolved %d, pipeline %v", m.Unresolved, m.Pipeline)
		}
		if err := srv.ResolveThread(ref, d, true); err != nil {
			t.Fatal(err)
		}
		if m, _ = c.MR(ctx, ref); m.Unresolved != 2 || m.Resolved != 2 {
			t.Errorf("after resolve: %d/%d", m.Unresolved, m.Resolved)
		}
		if err := srv.MergeMR(ref); err != nil {
			t.Fatal(err)
		}
		if m, _ = c.MR(ctx, ref); m.State != "merged" || m.MergedAt == nil {
			t.Errorf("after merge: %+v", m)
		}
		if err := srv.CloseIssue(issueRef(apiProj, 1)); err != nil {
			t.Fatal(err)
		}
		is, err := c.Issue(ctx, issueRef(apiProj, 1))
		if err != nil || is.State != "closed" || is.ClosedAt == nil {
			t.Errorf("closed issue = %+v, %v", is, err)
		}
	})
}

func TestMRsForIssue(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		tests := []struct {
			issue forge.Ref
			want  []string
		}{
			{issueRef(tracker, 1), []string{apiProj + "!1"}}, // mentioned only
			{issueRef(apiProj, 1), []string{apiProj + "!1"}}, // closes, listed by both endpoints once
			{issueRef(webProj, 2), []string{webProj + "!2"}}, // closed by a merged MR
			{issueRef(tracker, 3), nil},
		}
		for _, tc := range tests {
			got, err := c.MRsForIssue(ctx, tc.issue)
			if err != nil {
				t.Fatalf("%v: %v", tc.issue, err)
			}
			var refs []string
			for _, m := range got {
				refs = append(refs, m.Ref.String())
			}
			sameSet(t, tc.issue.String(), refs, tc.want)
		}
		if _, err := c.MRsForIssue(ctx, issueRef(tracker, 999)); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestDiscussions(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		ds, err := c.Discussions(ctx, mrRef(apiProj, 1))
		if err != nil {
			t.Fatal(err)
		}
		if len(ds) != 4 {
			t.Fatalf("%d discussions, want 4 (system note + 3 threads)", len(ds))
		}
		sys := ds[0]
		if sys.Resolvable || len(sys.Notes) != 1 || !sys.Notes[0].System {
			t.Errorf("system discussion = %+v", sys)
		}
		open, replied, diff := ds[1], ds[2], ds[3]
		if !open.Resolvable || open.Resolved || len(open.Notes) != 2 || open.Notes[1].Author.Username != "sandbox-dev" {
			t.Errorf("replied thread = %+v", open)
		}
		if !replied.Resolvable || replied.Resolved || replied.Position != nil {
			t.Errorf("open thread = %+v", replied)
		}
		if !diff.Resolved || diff.Position == nil || diff.Position.Path != "limit/bucket.go" || diff.Position.NewLine != 6 {
			t.Errorf("resolved diff thread = %+v", diff)
		}
		if open.Notes[0].CreatedAt.IsZero() || open.ID == "" || open.Notes[0].ID == 0 {
			t.Errorf("note fields not decoded: %+v", open.Notes[0])
		}

		issueThreads, err := c.Discussions(ctx, issueRef(tracker, 1))
		if err != nil || len(issueThreads) != 0 {
			t.Errorf("issue discussions = %v, %v", issueThreads, err)
		}
		if _, err := c.Discussions(ctx, mrRef(webProj, 99)); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
		if _, err := c.Discussions(ctx, forge.Ref{}); err == nil {
			t.Error("want error for a zero ref")
		}
	})
}

func TestMilestones(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		for _, ns := range []string{group, tracker} { // a group, then a project path
			ms, err := c.Milestones(ctx, ns)
			if err != nil {
				t.Fatalf("%s: %v", ns, err)
			}
			if len(ms) != 3 {
				t.Fatalf("%s: %d milestones, want 3", ns, len(ms))
			}
			if ms[0].Title != "Sprint 41" || ms[0].State != "closed" || ms[0].Start.String() != "2026-10-19" || ms[0].Due.String() != "2026-10-30" {
				t.Errorf("%s: first milestone = %+v", ns, ms[0])
			}
			if ms[2].Title != "Sprint 43" || ms[2].ID == 0 {
				t.Errorf("%s: last milestone = %+v", ns, ms[2])
			}
		}
		if _, err := c.Milestones(ctx, "nope/nope"); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
		if _, err := c.Milestones(ctx, ""); err == nil {
			t.Error("want error for an empty namespace")
		}
	})
}

func TestIterations(t *testing.T) {
	forEachTier(t, func(t *testing.T, tier fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		its, err := c.Iterations(ctx, group, "")
		if tier == fake.Free {
			if !errors.Is(err, forge.ErrUnavailable) {
				t.Fatalf("Free err = %v, want ErrUnavailable", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(its) != 4 {
			t.Fatalf("%d iterations, want 3 Delivery + 1 Maintenance", len(its))
		}
		delivery, err := c.Iterations(ctx, group, "Delivery")
		if err != nil {
			t.Fatal(err)
		}
		if len(delivery) != 3 {
			t.Fatalf("%d Delivery iterations, want 3", len(delivery))
		}
		wantStates := []string{"closed", "current", "upcoming"}
		wantStart := []string{"2026-10-19", "2026-11-02", "2026-11-16"}
		for i, it := range delivery {
			if it.State != wantStates[i] || it.Start.String() != wantStart[i] || it.Title != "" || it.Cadence != "Delivery" {
				t.Errorf("delivery[%d] = %+v", i, it)
			}
			if it.Due.Sub(it.Start.Time) != 13*24*time.Hour {
				t.Errorf("delivery[%d] spans %v..%v, want two weeks", i, it.Start, it.Due)
			}
		}
		patch, err := c.Iterations(ctx, group, "Maintenance")
		if err != nil || len(patch) != 1 || patch[0].Title != "Patch window" || patch[0].State != "current" {
			t.Errorf("Maintenance = %+v, %v", patch, err)
		}
		none, err := c.Iterations(ctx, group, "Nonexistent")
		if err != nil || len(none) != 0 {
			t.Errorf("unknown cadence = %+v, %v", none, err)
		}
		if _, err := c.Iterations(ctx, "", ""); err == nil {
			t.Error("want error for an empty group")
		}
	})
}

func TestCapabilities(t *testing.T) {
	forEachTier(t, func(t *testing.T, tier fake.Tier, c *gitlab.Client, _ *fake.Server) {
		caps, err := c.Capabilities(context.Background(), group, "")
		if err != nil {
			t.Fatal(err)
		}
		premium := tier == fake.Premium
		want := forge.Caps{
			Iterations: premium, Cadences: premium, Weight: premium,
			ApprovalRules: premium, BlocksLinks: premium, Epics: premium, ProbedAt: clock,
		}
		if caps != want {
			t.Errorf("caps = %+v\nwant   %+v", caps, want)
		}
		if _, err := c.Capabilities(context.Background(), "no-such-group", ""); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown group err = %v, want ErrNotFound", err)
		}
		if _, err := c.Capabilities(context.Background(), "", ""); err == nil {
			t.Error("want error for an empty group")
		}
	})
}

// A user's personal namespace has no group endpoints: the licence is read
// from the tracker project, and iterations, cadences and epics are off.
func TestCapabilitiesUserNamespace(t *testing.T) {
	for _, tier := range tiers {
		t.Run(tier.String(), func(t *testing.T) {
			srv := fake.New(tier, fake.WithClock(clock), fake.WithUserNamespace())
			t.Cleanup(srv.Close)
			c := gitlab.New(srv.URL, fake.Token, gitlab.WithClock(func() time.Time { return clock }))
			premium := tier == fake.Premium

			// The seeded merge requests live in web, so it has a licence flag to read.
			caps, err := c.Capabilities(context.Background(), group, group+"/web")
			if err != nil {
				t.Fatal(err)
			}
			want := forge.Caps{
				UserNamespace: true, Weight: premium, ApprovalRules: premium, BlocksLinks: premium, ProbedAt: clock,
			}
			if caps != want {
				t.Errorf("caps = %+v\nwant   %+v", caps, want)
			}
			for _, r := range srv.Requests() {
				if strings.HasPrefix(r.Path, "/api/v4/groups/") || r.Path == "/api/graphql" {
					t.Errorf("user namespace probe sent %s %s", r.Method, r.Path)
				}
			}

			// Without a project there is nothing to sample.
			caps, err = c.Capabilities(context.Background(), group, "")
			if err != nil {
				t.Fatal(err)
			}
			if want := (forge.Caps{UserNamespace: true, ProbedAt: clock}); caps != want {
				t.Errorf("caps without a project = %+v\nwant   %+v", caps, want)
			}
		})
	}
}

// userNamespaceStub answers /namespaces/:id as a user's namespace and every
// other path with status[path], or 200 [] when it has none.
func userNamespaceStub(t *testing.T, wantNamespace string, status map[string]int) *gitlab.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch p := r.URL.EscapedPath(); {
		case p == "/api/v4/namespaces/"+wantNamespace:
			io.WriteString(w, `{"kind":"user"}`)
		case status[p] != 0:
			w.WriteHeader(status[p])
			io.WriteString(w, `{"message":"nope"}`)
		case strings.HasPrefix(p, "/api/v4/namespaces/"):
			w.WriteHeader(http.StatusNotFound)
		default:
			io.WriteString(w, `[]`)
		}
	}))
	t.Cleanup(srv.Close)
	return gitlab.New(srv.URL, fake.Token, gitlab.WithClock(func() time.Time { return clock }))
}

// A tracker project with issues or merge requests turned off answers 403 or
// 404 to the sample: nothing to sample, not a failed probe.
func TestCapabilitiesUserNamespaceProjectWithoutSamples(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c := userNamespaceStub(t, "me", map[string]int{
				"/api/v4/projects/me%2Ftracker/issues":         status,
				"/api/v4/projects/me%2Ftracker/merge_requests": status,
			})
			caps, err := c.Capabilities(context.Background(), "me", "me/tracker")
			if err != nil {
				t.Fatal(err)
			}
			if want := (forge.Caps{UserNamespace: true, ProbedAt: clock}); caps != want {
				t.Errorf("caps = %+v\nwant   %+v", caps, want)
			}
		})
	}
	t.Run("a rejected token still fails", func(t *testing.T) {
		c := userNamespaceStub(t, "me", map[string]int{"/api/v4/projects/me%2Ftracker/issues": http.StatusUnauthorized})
		if _, err := c.Capabilities(context.Background(), "me", "me/tracker"); !errors.Is(err, forge.ErrAuth) {
			t.Errorf("err = %v, want ErrAuth", err)
		}
	})
}

// A namespace path with a slash is looked up escaped, like a group path.
func TestCapabilitiesUserNamespaceLookupEscapesSubgroupPaths(t *testing.T) {
	c := userNamespaceStub(t, "a%2Fb", nil)
	caps, err := c.Capabilities(context.Background(), "a/b", "")
	if err != nil {
		t.Fatal(err)
	}
	if !caps.UserNamespace {
		t.Errorf("caps = %+v, want a user namespace", caps)
	}
}

func TestCapabilitiesNeverWrites(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		if _, err := c.Capabilities(context.Background(), group, ""); err != nil {
			t.Fatal(err)
		}
		for _, r := range srv.Requests() {
			if r.Method != "GET" && r.Path != "/api/graphql" {
				t.Errorf("probe sent %s %s", r.Method, r.Path)
			}
		}
	})
}

func TestPagination(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithClock(clock), fake.WithPageSize(4))
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, fake.Token)
	got, err := c.ListIssues(context.Background(), forge.IssueQuery{Project: tracker, State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	sameSet(t, "iids", iids(got), []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11})
	var pages []string
	for _, r := range srv.Requests() {
		pages = append(pages, r.Query)
	}
	if len(pages) != 3 {
		t.Fatalf("%d requests, want 3 pages: %v", len(pages), pages)
	}
	for _, q := range pages {
		if !strings.Contains(q, "per_page=100") {
			t.Errorf("query %q lacks per_page=100", q)
		}
	}
	if !strings.Contains(pages[1], "page=2") || !strings.Contains(pages[2], "page=3") {
		t.Errorf("pages not followed in order: %v", pages)
	}
}

func TestPaginationAcrossEveryList(t *testing.T) {
	srv := fake.New(fake.Premium, fake.WithClock(clock), fake.WithPageSize(1))
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, fake.Token, gitlab.WithClock(func() time.Time { return clock }))
	ctx := context.Background()
	if ds, err := c.Discussions(ctx, mrRef(apiProj, 1)); err != nil || len(ds) != 4 {
		t.Errorf("discussions = %d, %v", len(ds), err)
	}
	if ms, err := c.Milestones(ctx, group); err != nil || len(ms) != 3 {
		t.Errorf("milestones = %d, %v", len(ms), err)
	}
	if its, err := c.Iterations(ctx, group, ""); err != nil || len(its) != 4 {
		t.Errorf("iterations = %d, %v", len(its), err)
	}
	if ls, err := c.Links(ctx, issueRef(tracker, 1)); err != nil || len(ls) != 1 {
		t.Errorf("links = %d, %v", len(ls), err)
	}
	if mrs, err := c.ListMRs(ctx, forge.MRQuery{Project: webProj, State: "all"}); err != nil || len(mrs) != 2 {
		t.Errorf("mrs = %d, %v", len(mrs), err)
	}
}

func TestRateLimitRetries(t *testing.T) {
	var (
		mu     sync.Mutex
		sleeps []time.Duration
	)
	newClient := func(srv *fake.Server, retries int) *gitlab.Client {
		return gitlab.New(srv.URL, fake.Token,
			gitlab.WithMaxRetries(retries),
			gitlab.WithSleep(func(_ context.Context, d time.Duration) error {
				mu.Lock()
				defer mu.Unlock()
				sleeps = append(sleeps, d)
				return nil
			}))
	}
	tests := []struct {
		name       string
		throttle   int
		retryAfter string
		retries    int
		wantErr    bool
		wantSleeps []time.Duration
	}{
		{"honours Retry-After plus a second of slack", 2, "3", 3, false, []time.Duration{4 * time.Second, 4 * time.Second}},
		{"backs off without a header", 2, "", 3, false, []time.Duration{time.Second, 2 * time.Second}},
		{"gives up after the bound", 10, "1", 2, true, []time.Duration{2 * time.Second, 2 * time.Second}},
		{"never retries when disabled", 1, "1", 0, true, nil},
		{"refuses a day-long wait", 1, "86400", 3, true, nil},
		{"zero wait still pads", 1, "0", 3, false, []time.Duration{time.Second}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sleeps = nil
			srv := fake.New(fake.Free)
			t.Cleanup(srv.Close)
			srv.Throttle(tc.throttle, tc.retryAfter)
			_, err := newClient(srv, tc.retries).Me(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				var aerr *gitlab.APIError
				if !errors.As(err, &aerr) || aerr.Status != 429 {
					t.Fatalf("err = %v, want *APIError 429", err)
				}
			}
			if len(sleeps) != len(tc.wantSleeps) {
				t.Fatalf("sleeps = %v, want %v", sleeps, tc.wantSleeps)
			}
			for i := range sleeps {
				if sleeps[i] != tc.wantSleeps[i] {
					t.Errorf("sleeps = %v, want %v", sleeps, tc.wantSleeps)
				}
			}
		})
	}
}

// A 429 closes the window for every request that has not started: the next
// call waits out the same Retry-After instead of being refused again.
func TestRateLimitGateHoldsLaterRequests(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	srv.Throttle(1, "30")
	var (
		mu     sync.Mutex
		sleeps []time.Duration
	)
	c := gitlab.New(srv.URL, fake.Token, gitlab.WithSleep(func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		sleeps = append(sleeps, d)
		return nil
	}))
	ctx := context.Background()
	if _, err := c.Me(ctx); err != nil { // refused once, retried after 30s
		t.Fatal(err)
	}
	if _, err := c.Me(ctx); err != nil { // starts inside the closed window
		t.Fatal(err)
	}
	if len(sleeps) != 2 || sleeps[0] != 31*time.Second {
		t.Fatalf("sleeps = %v, want the padded retry wait then a gate wait", sleeps)
	}
	if g := sleeps[1]; g <= 0 || g > 31*time.Second {
		t.Errorf("gate wait = %v, want within the 31s window", g)
	}
}

func TestRateLimitRetryHonoursCancellation(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	srv.Throttle(5, "1")
	ctx, cancel := context.WithCancel(context.Background())
	c := gitlab.New(srv.URL, fake.Token, gitlab.WithSleep(func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}))
	if _, err := c.Me(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestInFlightCap(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithLatency(30*time.Millisecond))
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, fake.Token)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Me(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := srv.MaxInFlight(); got > 4 || got < 2 {
		t.Errorf("max in-flight = %d, want 2..4", got)
	}
}

func TestErrorMapping(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	ctx := context.Background()

	bad := gitlab.New(srv.URL, "wrong-token")
	if _, err := bad.Me(ctx); !errors.Is(err, forge.ErrAuth) {
		t.Errorf("bad token err = %v, want ErrAuth", err)
	}
	if _, err := bad.Capabilities(ctx, group, ""); !errors.Is(err, forge.ErrAuth) {
		t.Errorf("bad token on a probe = %v, want ErrAuth, not a silent false", err)
	}
	if _, err := bad.Iterations(ctx, group, ""); !errors.Is(err, forge.ErrAuth) {
		t.Errorf("bad token on a gated endpoint = %v, want ErrAuth", err)
	}

	good := gitlab.New(srv.URL, fake.Token)
	_, err := good.Iterations(ctx, group, "")
	var aerr *gitlab.APIError
	if !errors.Is(err, forge.ErrUnavailable) || !errors.As(err, &aerr) || aerr.Status != 403 {
		t.Errorf("Free iterations err = %v, want ErrUnavailable wrapping a 403", err)
	}
	if strings.Contains(err.Error(), fake.Token) {
		t.Error("error leaks the token")
	}
	if _, err := good.ListIssues(ctx, forge.IssueQuery{Project: "no/such"}); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestUpdatedAfterIsSentInUTC(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, fake.Token)
	tz := time.FixedZone("CEST", 2*3600)
	_, err := c.ListIssues(context.Background(), forge.IssueQuery{Project: tracker, UpdatedAfter: time.Date(2026, 11, 11, 14, 0, 0, 0, tz)})
	if err != nil {
		t.Fatal(err)
	}
	q := srv.Requests()[0].Query
	if !strings.Contains(q, "updated_after=2026-11-11T12%3A00%3A00Z") || strings.Contains(q, "%2B") {
		t.Errorf("query = %q, want a UTC timestamp", q)
	}
}

func TestSendsTokenHeaderNotQuery(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL+"/", fake.Token) // trailing slash tolerated
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, r := range srv.Requests() {
		if strings.Contains(r.Query, fake.Token) || strings.Contains(r.Path, fake.Token) {
			t.Errorf("token appears in the URL: %+v", r)
		}
	}
}
