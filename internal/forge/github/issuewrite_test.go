package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
	"github.com/coredev-uk/ade/internal/forge/github/fake"
)

func TestDefaultBranchAndFiles(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	if b, err := c.DefaultBranch(ctx, apiProj); err != nil || b != "main" {
		t.Errorf("DefaultBranch = %q, %v", b, err)
	}
	if _, err := c.DefaultBranch(ctx, "ade-demo/nope"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown repo: err = %v", err)
	}
	if _, err := c.DefaultBranch(ctx, "noslash"); err == nil {
		t.Error("a project without an owner was accepted")
	}

	tmpl, err := c.ReadFile(ctx, apiProj, "main", ".github/pull_request_template.md")
	if err != nil || !strings.Contains(string(tmpl), "## Checklist") {
		t.Errorf("template = %q, %v", tmpl, err)
	}
	if _, err := c.ReadFile(ctx, apiProj, "main", ".github/nope.md"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("missing file: err = %v, want ErrNotFound", err)
	}
	if b, err := c.ReadFile(ctx, apiProj, "1-rate-limit-the-export-endpoint", "limit/bucket.go"); err != nil || !strings.HasPrefix(string(b), "package limit") {
		t.Errorf("file on a branch = %q, %v", b, err)
	}

	tests := []struct {
		dir  string
		want []string
	}{
		{".github", []string{".github/pull_request_template.md"}},
		{"", []string{"Dockerfile", "README.md"}},
		{"limit", nil}, // not on main
		{"no/such/dir", nil},
		{"README.md", nil}, // a file is not a directory
	}
	for _, tc := range tests {
		got, err := c.ListFiles(ctx, apiProj, "main", tc.dir)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("ListFiles(%q) = %v, %v; want %v", tc.dir, got, err, tc.want)
		}
	}
}

func TestCreateIssue(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	me, _ := c.Me(ctx)

	// Reading the tracker teaches the client its sprint ids; the same sprint
	// has another id in the web repository, and the child must land in that one.
	tracker, err := c.Issue(ctx, issueRef(trackerProj, 1))
	if err != nil || tracker.Milestone == nil {
		t.Fatal(tracker, err)
	}
	trackerMS := tracker.Milestone.ID
	var webMS int64
	other := github.New(srv.URL, fake.Token) // a second client, so c stays unaware of web's ids
	webSprints, err := other.Milestones(ctx, webProj)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range webSprints {
		if m.Title == "Sprint 42" {
			webMS = m.ID
		}
	}
	if webMS == 0 || webMS == trackerMS {
		t.Fatalf("web Sprint 42 id %d vs tracker's %d: the ids must differ for the test to prove anything", webMS, trackerMS)
	}

	is, err := c.CreateIssue(ctx, webProj, forge.IssueDraft{
		Title: "Child", Description: "Parent: ade-demo/tracker#1", Labels: []string{"type:story", "brand-new-label"},
		AssigneeIDs: []int64{me.ID, 1002}, MilestoneID: trackerMS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if is.Ref.Project != webProj || is.Ref.Kind != forge.KindIssue || is.Ref.IID == 0 || is.State != "opened" || is.WebURL == "" {
		t.Errorf("issue = %+v", is)
	}
	if is.Milestone == nil || is.Milestone.Title != "Sprint 42" || is.Milestone.ID != webMS {
		t.Errorf("milestone = %+v: the web repo's own Sprint 42 expected", is.Milestone)
	}
	if !slices.Equal(is.Labels, []string{"type:story", "brand-new-label"}) {
		t.Errorf("labels = %v", is.Labels)
	}
	var who []string
	for _, a := range is.Assignees {
		who = append(who, a.Username)
	}
	slices.Sort(who)
	if !slices.Equal(who, []string{"demo-user", "teammate"}) {
		t.Errorf("assignees = %v: ids are sent as logins", who)
	}
	got, err := c.Issue(ctx, is.Ref)
	if err != nil || got.Title != "Child" || got.Description != "Parent: ade-demo/tracker#1" {
		t.Errorf("read back = %+v, %v", got, err)
	}

	if _, err := c.CreateIssue(ctx, webProj, forge.IssueDraft{}); err == nil {
		t.Error("an issue without a title was created")
	}
	if _, err := c.CreateIssue(ctx, webProj, forge.IssueDraft{Title: "x", AssigneeIDs: []int64{987654}}); err == nil {
		t.Error("an unknown assignee id was accepted")
	}
	if _, err := c.CreateIssue(ctx, "ade-demo/nope", forge.IssueDraft{Title: "x"}); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown repo: err = %v", err)
	}
	// An id the client never saw is a milestone number of the target repo.
	n, err := c.CreateIssue(ctx, webProj, forge.IssueDraft{Title: "By number", MilestoneID: 3})
	if err != nil || n.Milestone == nil || n.Milestone.Title != "Sprint 43" {
		t.Errorf("milestone by number = %+v, %v", n.Milestone, err)
	}
}

func TestSetLabels(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	ref := issueRef(trackerProj, 1)
	if err := c.SetLabels(ctx, ref, []string{"workflow:in progress", "type:bug"}, []string{"workflow:in review", "never-had-it"}); err != nil {
		t.Fatal(err)
	}
	got, _ := srv.Labels(ref)
	slices.Sort(got)
	want := []string{"app:api", "type:bug", "type:story", "weight:5", "workflow:in progress"}
	if !slices.Equal(got, want) {
		t.Errorf("labels = %v, want %v", got, want)
	}
	if err := c.SetLabels(ctx, ref, nil, nil); err != nil {
		t.Errorf("empty change: %v", err)
	}
	// Labels of a pull request go through the same endpoints.
	if err := c.SetLabels(ctx, mrRef(apiProj, 2), []string{"type:story"}, nil); err != nil {
		t.Errorf("labelling a PR: %v", err)
	}
	if got, _ := srv.Labels(mrRef(apiProj, 2)); !slices.Equal(got, []string{"type:story"}) {
		t.Errorf("PR labels = %v", got)
	}
	if err := c.SetLabels(ctx, issueRef(trackerProj, 99), []string{"x"}, nil); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown issue: err = %v", err)
	}
	if err := c.SetLabels(ctx, forge.Ref{}, []string{"x"}, nil); err == nil {
		t.Error("a zero ref was accepted")
	}
}

func TestLinkIssues(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	parent, child := issueRef(trackerProj, 3), issueRef(apiProj, 1)

	// api#1 already has a parent (tracker#1): GitHub allows one.
	apiStatus(t, c.LinkIssues(ctx, parent, child, "relates_to"), http.StatusUnprocessableEntity)

	fresh, err := c.CreateIssue(ctx, apiProj, forge.IssueDraft{Title: "Fresh child"})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"relates_to", "", "sub_issue", "relates_to"} {
		if err := c.LinkIssues(ctx, parent, fresh.Ref, kind); err != nil {
			t.Fatalf("LinkIssues(%q): %v (linking an existing pair is a no-op)", kind, err)
		}
	}
	if got, _ := srv.SubIssues(parent); !slices.Equal(got, []forge.Ref{fresh.Ref}) {
		t.Errorf("sub-issues = %v, want exactly the child once", got)
	}
	links, _ := c.Links(ctx, parent)
	if len(links) != 1 || links[0] != (forge.Link{Ref: fresh.Ref, Type: "sub_issue"}) {
		t.Errorf("links = %+v", links)
	}
	if err := c.LinkIssues(ctx, parent, fresh.Ref, "blocks"); !errors.Is(err, forge.ErrUnavailable) {
		t.Errorf("a blocks link: err = %v, want ErrUnavailable", err)
	}
	if err := c.LinkIssues(ctx, parent, issueRef(apiProj, 99), "relates_to"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown child: err = %v", err)
	}
	if err := c.LinkIssues(ctx, mrRef(apiProj, 2), child, "relates_to"); err == nil {
		t.Error("an MR ref was accepted")
	}
	if err := c.SetIteration(ctx, parent, 5); !errors.Is(err, forge.ErrUnavailable) {
		t.Errorf("SetIteration err = %v, want ErrUnavailable", err)
	}
}

func TestBranchesAndCreateBranch(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	got, err := c.Branches(ctx, apiProj, "")
	if err != nil || !slices.Contains(got, "main") || !slices.Contains(got, "feature/retry-budget") {
		t.Errorf("all branches = %v, %v", got, err)
	}
	if got, _ := c.Branches(ctx, apiProj, "feature/"); !slices.Equal(got, []string{"feature/retry-budget"}) {
		t.Errorf("prefix feature/ = %v", got)
	}
	if got, _ := c.Branches(ctx, apiProj, "zzz"); len(got) != 0 {
		t.Errorf("unmatched prefix = %v", got)
	}

	b, err := c.CreateBranch(ctx, apiProj, "3-retry-webhooks", "main")
	if err != nil || b.Name != "3-retry-webhooks" || len(b.SHA) != 40 || b.Project != apiProj || b.WebURL == "" {
		t.Fatalf("branch = %+v, %v", b, err)
	}
	if !slices.Contains(srv.Branches(apiProj), "3-retry-webhooks") {
		t.Error("branch not on the forge")
	}
	if _, err := c.CreateBranch(ctx, apiProj, "3-retry-webhooks", "main"); !errors.Is(err, forge.ErrExists) {
		t.Errorf("second create: err = %v, want ErrExists", err)
	}
	// From a branch with a slash and from a full sha.
	if _, err := c.CreateBranch(ctx, apiProj, "from-feature", "feature/retry-budget"); err != nil {
		t.Errorf("from a slashed branch: %v", err)
	}
	if s, err := c.CreateBranch(ctx, apiProj, "from-sha", b.SHA); err != nil || s.SHA != b.SHA {
		t.Errorf("from a sha = %+v, %v", s, err)
	}
	if _, err := c.CreateBranch(ctx, apiProj, "x/y", "no-such-branch"); !errors.Is(err, forge.ErrNotFound) && err == nil {
		t.Errorf("unknown source accepted")
	}
	if _, err := c.CreateBranch(ctx, apiProj, "", "main"); err == nil {
		t.Error("an empty name was accepted")
	}
}

func TestCreateMR(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()

	t.Run("a branch with its own commit", func(t *testing.T) {
		mr, err := c.CreateMR(ctx, forge.MRDraft{
			Project: apiProj, Source: "feature/retry-budget", Target: "main", Title: "Again", Draft: true,
		})
		// feature/retry-budget already has an open PR (api!5).
		if !errors.Is(err, forge.ErrExists) {
			t.Errorf("CreateMR = %+v, %v; want ErrExists for the branch's open PR", mr, err)
		}
	})

	t.Run("a fresh branch gets an empty start commit and a draft PR", func(t *testing.T) {
		b, err := c.CreateBranch(ctx, apiProj, "3-retry-webhooks", "main")
		if err != nil {
			t.Fatal(err)
		}
		mr, err := c.CreateMR(ctx, forge.MRDraft{
			Project: apiProj, Source: b.Name, Target: "main", Title: "Retry failed webhook deliveries",
			Description: "Closes #3", Draft: true, Labels: []string{"type:story"}, CommitPrefix: "GL-3",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !mr.Draft || mr.State != "opened" || mr.Ref.Kind != forge.KindMR || mr.SourceBranch != b.Name || mr.TargetBranch != "main" || mr.Description != "Closes #3" {
			t.Errorf("MR = %+v", mr)
		}
		if mr.HeadSHA == b.SHA {
			t.Errorf("head %s is still the branch's start: no empty commit was made", mr.HeadSHA)
		}
		if got := headCommitMessage(t, srv, apiProj, mr.HeadSHA); got != "GL-3 Start work: Retry failed webhook deliveries" {
			t.Errorf("start commit message = %q, want the prefix applied", got)
		}
		if got, _ := srv.Labels(mr.Ref); !slices.Equal(got, []string{"type:story"}) {
			t.Errorf("labels = %v", got)
		}
		d, err := c.MRDiff(ctx, mr.Ref)
		if err != nil || len(d.Files) != 0 {
			t.Errorf("diff of the empty start = %+v, %v", d, err)
		}
		if _, err := c.CreateMR(ctx, forge.MRDraft{Project: apiProj, Source: b.Name, Target: "main", Title: "dup"}); !errors.Is(err, forge.ErrExists) {
			t.Errorf("second MR for the branch: err = %v, want ErrExists", err)
		}
		found, err := c.ListMRs(ctx, forge.MRQuery{Project: apiProj, SourceBranch: b.Name})
		if err != nil || len(found) != 1 || found[0].Ref != mr.Ref {
			t.Errorf("ListMRs by branch = %+v, %v", found, err)
		}
	})

	t.Run("a ready PR", func(t *testing.T) {
		if _, err := c.CreateBranch(ctx, apiProj, "ready-one", "main"); err != nil {
			t.Fatal(err)
		}
		mr, err := c.CreateMR(ctx, forge.MRDraft{Project: apiProj, Source: "ready-one", Target: "main", Title: "Ready"})
		if err != nil || mr.Draft {
			t.Errorf("MR = %+v, %v", mr, err)
		}
	})

	t.Run("input errors", func(t *testing.T) {
		if _, err := c.CreateMR(ctx, forge.MRDraft{Project: apiProj, Source: "no-such-branch", Target: "main", Title: "x"}); err == nil {
			t.Error("an unknown branch was accepted")
		}
		if _, err := c.CreateMR(ctx, forge.MRDraft{Project: apiProj, Source: "a"}); err == nil {
			t.Error("an incomplete draft was accepted")
		}
	})
}

// headCommitMessage reads a commit's message from the fake over REST.
func headCommitMessage(t *testing.T, srv *fake.Server, project, sha string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v3/repos/"+project+"/git/commits/"+sha, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+fake.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ Message string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Message
}
