package gitlab_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

var _ forge.IssueWriter = (*gitlab.Client)(nil)

func TestCreateIssueCopiesFields(t *testing.T) {
	forEachTier(t, func(t *testing.T, tier fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		me, err := c.Me(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ms, err := c.Milestones(ctx, group)
		if err != nil || len(ms) < 2 {
			t.Fatalf("milestones = %v, %v", ms, err)
		}
		w := 4
		is, err := c.CreateIssue(ctx, apiProj, forge.IssueDraft{
			Title: "Child", Description: "Parent: x/y#1", Labels: []string{"type::story"},
			AssigneeIDs: []int64{me.ID}, MilestoneID: ms[1].ID, Weight: &w,
		})
		if err != nil {
			t.Fatal(err)
		}
		if is.Ref.Project != apiProj || is.Ref.IID != 3 {
			t.Errorf("ref = %v", is.Ref)
		}
		if !slices.Equal(is.Labels, []string{"type::story"}) || len(is.Assignees) != 1 || is.Assignees[0].ID != me.ID {
			t.Errorf("labels/assignees = %v %v", is.Labels, is.Assignees)
		}
		if is.Milestone == nil || is.Milestone.ID != ms[1].ID {
			t.Errorf("milestone = %+v", is.Milestone)
		}
		got, err := c.Issue(ctx, is.Ref)
		if err != nil {
			t.Fatal(err)
		}
		switch tier {
		case fake.Premium:
			if got.Weight == nil || *got.Weight != 4 {
				t.Errorf("premium weight = %v, want 4", got.Weight)
			}
		default:
			if got.Weight != nil {
				t.Errorf("free weight = %v, want unset", *got.Weight)
			}
		}
		if _, err := c.CreateIssue(ctx, apiProj, forge.IssueDraft{}); err == nil {
			t.Error("untitled issue was accepted")
		}
		if _, err := c.CreateIssue(ctx, group+"/nope", forge.IssueDraft{Title: "x"}); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown project err = %v, want ErrNotFound", err)
		}
	})
}

func TestSetLabelsRemovesExplicitly(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		ref := issueRef(tracker, 3)
		// tracker#2 carries "workflow::in progress"; #3 has none. Put two
		// workflow labels on #3 (Free keeps both) and move it in one call.
		if err := c.SetLabels(ctx, ref, []string{"workflow::blocked", "workflow::in review"}, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.SetLabels(ctx, ref, []string{"workflow::in progress"}, []string{"workflow::blocked", "workflow::in review"}); err != nil {
			t.Fatal(err)
		}
		got, err := srv.IssueLabels(ref)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(got, "workflow::in progress") || slices.Contains(got, "workflow::blocked") || slices.Contains(got, "workflow::in review") {
			t.Errorf("labels = %v", got)
		}
		if !slices.Contains(got, "type::story") {
			t.Errorf("unrelated labels were dropped: %v", got)
		}
		if err := c.SetLabels(ctx, ref, nil, nil); err != nil {
			t.Errorf("empty change = %v, want a no-op", err)
		}
		if err := c.SetLabels(ctx, issueRef(tracker, 999), []string{"x"}, nil); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown issue err = %v, want ErrNotFound", err)
		}
	})
}

func TestLinkIssuesIsIdempotent(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		a, b := issueRef(tracker, 3), issueRef(apiProj, 2)
		for range 2 { // the second call meets GitLab's 409
			if err := c.LinkIssues(ctx, a, b, "relates_to"); err != nil {
				t.Fatal(err)
			}
		}
		links, err := c.Links(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, l := range links {
			if l.Ref == b && l.Type == "relates_to" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("links = %+v, want exactly one relates_to %s", links, b)
		}
		back, _ := c.Links(ctx, b)
		if !slices.ContainsFunc(back, func(l forge.Link) bool { return l.Ref == a }) {
			t.Errorf("link not visible from the child side: %+v", back)
		}
		if err := c.LinkIssues(ctx, a, issueRef(apiProj, 999), "relates_to"); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown target err = %v, want ErrNotFound", err)
		}
	})
}

func TestSetIteration(t *testing.T) {
	forEachTier(t, func(t *testing.T, tier fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		ref := issueRef(apiProj, 2)
		if tier == fake.Free {
			// Verified live: Free accepts the mutation and ignores it.
			if err := c.SetIteration(ctx, ref, 1); err != nil {
				t.Errorf("free SetIteration = %v", err)
			}
			if got, err := c.Issue(ctx, ref); err != nil || got.Iteration != nil {
				t.Errorf("free issue iteration = %+v, %v", got.Iteration, err)
			}
			return
		}
		its, err := c.Iterations(ctx, group, "Delivery")
		if err != nil || len(its) == 0 {
			t.Fatalf("iterations = %v, %v", its, err)
		}
		cur := its[1]
		if err := c.SetIteration(ctx, ref, cur.ID); err != nil {
			t.Fatal(err)
		}
		got, err := c.Issue(ctx, ref)
		if err != nil || got.Iteration == nil || got.Iteration.ID != cur.ID {
			t.Errorf("issue iteration = %+v, %v; want %d", got.Iteration, err, cur.ID)
		}
		if err := c.SetIteration(ctx, ref, 987654); err == nil {
			t.Error("unknown iteration was accepted")
		}
		if err := c.SetIteration(ctx, issueRef(apiProj, 999), cur.ID); err == nil {
			t.Error("unknown issue was accepted")
		}
	})
}

// GitLab reports a mutation the caller may not use as a null payload next to an
// error; that is ErrUnavailable, not a transport failure.
func TestSetIterationNullPayloadIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"issueSetIteration":null},"errors":[{"message":"not allowed"}]}`))
	}))
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, "t")
	if err := c.SetIteration(context.Background(), issueRef(apiProj, 1), 5); !errors.Is(err, forge.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestBranchesCreateAndPrefix(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		def, err := c.DefaultBranch(ctx, apiProj)
		if err != nil || def != "main" {
			t.Fatalf("default branch = %q, %v", def, err)
		}
		b, err := c.CreateBranch(ctx, apiProj, "7-add-thing", def)
		if err != nil || b.Name != "7-add-thing" || len(b.SHA) != 40 {
			t.Fatalf("created = %+v, %v", b, err)
		}
		if _, err := c.CreateBranch(ctx, apiProj, "7-add-thing", def); !errors.Is(err, forge.ErrExists) {
			t.Errorf("duplicate err = %v, want ErrExists", err)
		}
		if _, err := c.CreateBranch(ctx, apiProj, "8-x", "no-such-ref"); err == nil || errors.Is(err, forge.ErrExists) {
			t.Errorf("bad ref err = %v", err)
		}
		// 17-other must not match prefix "7-".
		if _, err := c.CreateBranch(ctx, apiProj, "17-other", def); err != nil {
			t.Fatal(err)
		}
		got, err := c.Branches(ctx, apiProj, "7-")
		if err != nil || !slices.Equal(got, []string{"7-add-thing"}) {
			t.Errorf("prefix 7- = %v, %v", got, err)
		}
		all, _ := c.Branches(ctx, apiProj, "")
		if !slices.Contains(all, "main") || !slices.Contains(all, "17-other") {
			t.Errorf("all = %v", all)
		}
	})
}

func TestReadTemplates(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		dir := ".gitlab/merge_request_templates"
		if got, err := c.ListFiles(ctx, apiProj, "main", dir); err != nil || len(got) != 0 {
			t.Fatalf("empty dir = %v, %v", got, err)
		}
		srv.AddFile(apiProj, dir+"/Default.md", "## Summary\n\n/label ~\"type::story\"\n")
		srv.AddFile(apiProj, dir+"/Hotfix.md", "hotfix")
		got, err := c.ListFiles(ctx, apiProj, "main", dir)
		if err != nil || !slices.Equal(got, []string{dir + "/Default.md", dir + "/Hotfix.md"}) {
			t.Fatalf("files = %v, %v", got, err)
		}
		b, err := c.ReadFile(ctx, apiProj, "main", dir+"/Default.md")
		if err != nil || !strings.HasPrefix(string(b), "## Summary") {
			t.Errorf("content = %q, %v", b, err)
		}
		if _, err := c.ReadFile(ctx, apiProj, "main", dir+"/Missing.md"); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("missing err = %v, want ErrNotFound", err)
		}
	})
}

func TestCreateMRRunsQuickActions(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		if _, err := c.CreateBranch(ctx, apiProj, "2-work", "main"); err != nil {
			t.Fatal(err)
		}
		mr, err := c.CreateMR(ctx, forge.MRDraft{
			Project: apiProj, Source: "2-work", Target: "main", Title: "Do work", Draft: true,
			Description: "Closes #2\n\nPart of " + tracker + "#3\n\n/label ~\"type::story\"\n/assign_reviewer @reviewer-rae\n",
			Labels:      []string{"extra"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !mr.Draft || mr.Title != "Draft: Do work" || mr.SourceBranch != "2-work" || mr.TargetBranch != "main" {
			t.Errorf("mr = %+v", mr)
		}
		if strings.Contains(mr.Description, "/label") || !strings.Contains(mr.Description, "Closes #2") {
			t.Errorf("description = %q (quick actions run and vanish)", mr.Description)
		}
		labels, _ := srv.MRLabels(mr.Ref)
		if !slices.Contains(labels, "type::story") || !slices.Contains(labels, "extra") {
			t.Errorf("labels = %v", labels)
		}
		if rev, _ := srv.MRReviewers(mr.Ref); !slices.Equal(rev, []string{"reviewer-rae"}) {
			t.Errorf("reviewers = %v", rev)
		}
		// Reading back: the MR closes api#2 and mentions the tracker issue.
		mrs, err := c.MRsForIssue(ctx, issueRef(apiProj, 2))
		if err != nil || !slices.ContainsFunc(mrs, func(m forge.MR) bool { return m.Ref == mr.Ref }) {
			t.Errorf("MRsForIssue = %v, %v", mrs, err)
		}
		if _, err := c.CreateMR(ctx, forge.MRDraft{Project: apiProj, Source: "2-work", Target: "main", Title: "Again"}); !errors.Is(err, forge.ErrExists) {
			t.Errorf("second MR err = %v, want ErrExists", err)
		}
		if _, err := c.CreateMR(ctx, forge.MRDraft{Project: apiProj, Source: "ghost", Target: "main", Title: "x"}); err == nil {
			t.Error("MR from a missing branch was accepted")
		}
		if _, err := c.CreateMR(ctx, forge.MRDraft{Project: apiProj, Source: "2-work"}); err == nil {
			t.Error("incomplete draft was accepted")
		}
	})
}
