//go:build live

package github_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
)

// Live tests run against the synthetic GitHub sandbox seeded by
// scripts/sandbox/github-seed.sh (coredev-uk/ade-sandbox-{tracker,api,web}).
// They need `gh auth token` (or GITHUB_TOKEN) with the repo scope:
//
//	go test -tags live -run Live -v ./internal/forge/github
//
// Writes are round trips that leave the sandbox as found, except review
// comments, which are left resolved (as the GitLab sandbox does).
//
// Objects are found by title, never by number: the sandbox numbering depends on
// what the seed converged.

func liveToken(t *testing.T) string {
	t.Helper()
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		return tok
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		t.Skip("no GITHUB_TOKEN and `gh auth token` failed")
	}
	return strings.TrimSpace(string(out))
}

type sandbox struct {
	c                  *github.Client
	owner              string
	tracker, api, web  string
	ctx                context.Context
	titleRate, titleAu string
}

func liveSandbox(t *testing.T) *sandbox {
	t.Helper()
	owner := os.Getenv("ADE_SANDBOX_OWNER")
	if owner == "" {
		owner = "coredev-uk"
	}
	prefix := os.Getenv("ADE_SANDBOX_PREFIX")
	if prefix == "" {
		prefix = "ade-sandbox"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	return &sandbox{
		c: github.New("", liveToken(t)), owner: owner, ctx: ctx,
		tracker: owner + "/" + prefix + "-tracker", api: owner + "/" + prefix + "-api", web: owner + "/" + prefix + "-web",
		titleRate: "Rate-limit the export endpoint", titleAu: "Paginate the audit log view",
	}
}

func (s *sandbox) issue(t *testing.T, project, title string) forge.Issue {
	t.Helper()
	is, err := s.c.ListIssues(s.ctx, forge.IssueQuery{Project: project, State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range is {
		if i.Title == title {
			return i
		}
	}
	t.Fatalf("no issue %q in %s", title, project)
	return forge.Issue{}
}

func (s *sandbox) mr(t *testing.T, project, title string) forge.MR {
	t.Helper()
	mrs, err := s.c.ListMRs(s.ctx, forge.MRQuery{Project: project, State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mrs {
		if m.Title == title {
			return m
		}
	}
	t.Fatalf("no MR %q in %s", title, project)
	return forge.MR{}
}

func TestLiveRead(t *testing.T) {
	s := liveSandbox(t)
	ctx, c := s.ctx, s.c

	me, err := c.Me(ctx)
	if err != nil || me.Username == "" || me.ID == 0 {
		t.Fatalf("Me = %+v, %v", me, err)
	}
	caps, err := c.Capabilities(ctx, s.owner)
	if err != nil || caps.Iterations || caps.Weight || caps.ApprovalRules || caps.Epics || caps.ProbedAt.IsZero() {
		t.Fatalf("caps = %+v, %v", caps, err)
	}
	if _, err := c.Iterations(ctx, s.owner, ""); !errors.Is(err, forge.ErrUnavailable) {
		t.Errorf("Iterations err = %v, want ErrUnavailable", err)
	}

	t.Run("milestones are sprints", func(t *testing.T) {
		ms, err := c.Milestones(ctx, s.tracker)
		if err != nil {
			t.Fatal(err)
		}
		var cur, prev *forge.Milestone
		for i, m := range ms {
			switch m.Title {
			case "Sprint 42":
				cur = &ms[i]
			case "Sprint 41":
				prev = &ms[i]
			}
		}
		if cur == nil || prev == nil {
			t.Fatalf("milestones = %+v", ms)
		}
		if cur.Start.String() != "2026-11-02" || cur.Due.String() != "2026-11-13" || cur.State != "active" {
			t.Errorf("Sprint 42 = %+v, want 2026-11-02..2026-11-13 active", *cur)
		}
		if prev.State != "closed" || prev.Due.String() != "2026-10-30" {
			t.Errorf("Sprint 41 = %+v", *prev)
		}
	})

	rate := s.issue(t, s.tracker, s.titleRate)
	t.Run("issues", func(t *testing.T) {
		for _, want := range []string{"type:story", "app:api", "workflow:in review", "weight:5"} {
			if !contains(rate.Labels, want) {
				t.Errorf("tracker issue %q lacks label %q: %v", rate.Title, want, rate.Labels)
			}
		}
		if rate.Milestone == nil || rate.Milestone.Title != "Sprint 42" || rate.State != "opened" || rate.Tasks.Total != 4 || rate.Tasks.Done != 1 {
			t.Errorf("issue = %+v", rate)
		}
		if len(rate.Assignees) != 1 || rate.Assignees[0].Username != me.Username {
			t.Errorf("assignees = %+v, want %s", rate.Assignees, me.Username)
		}
		all, err := c.ListIssues(ctx, forge.IssueQuery{Project: s.tracker, State: "all"})
		if err != nil {
			t.Fatal(err)
		}
		for _, is := range all {
			if is.Ref.Kind != forge.KindIssue || is.Ref.Project != s.tracker {
				t.Errorf("odd ref %v", is.Ref)
			}
		}
		sprint, err := c.ListIssues(ctx, forge.IssueQuery{Project: s.tracker, State: "all", Milestone: "Sprint 42"})
		if err != nil || len(sprint) == 0 || len(sprint) >= len(all) {
			t.Errorf("Sprint 42 filter: %d of %d issues, %v", len(sprint), len(all), err)
		}
		if none, err := c.ListIssues(ctx, forge.IssueQuery{Project: s.tracker, Milestone: "No such sprint"}); err != nil || len(none) != 0 {
			t.Errorf("unknown milestone: %d issues, %v", len(none), err)
		}
	})

	t.Run("sub-issues are the child links", func(t *testing.T) {
		links, err := c.Links(ctx, rate.Ref)
		if err != nil {
			t.Fatal(err)
		}
		var child forge.Ref
		for _, l := range links {
			if l.Type == "sub_issue" && l.Ref.Project == s.api {
				child = l.Ref
			}
		}
		if child.IID == 0 {
			t.Fatalf("no sub_issue link into %s: %+v", s.api, links)
		}
		back, err := c.Links(ctx, child)
		if err != nil || len(back) != 1 || back[0].Type != "parent" || back[0].Ref != rate.Ref {
			t.Errorf("child links = %+v, %v; want the parent %s", back, err, rate.Ref)
		}
		ci, err := c.Issue(ctx, child)
		if err != nil || !strings.Contains(ci.Description, "Parent: "+s.tracker+"#") {
			t.Errorf("child = %+v, %v", ci, err)
		}
		mrs, err := c.MRsForIssue(ctx, child)
		if err != nil || len(mrs) == 0 || mrs[0].Title != s.titleRate {
			t.Errorf("MRsForIssue(%s) = %+v, %v", child, mrs, err)
		}
	})

	t.Run("pull requests", func(t *testing.T) {
		ready := s.mr(t, s.api, s.titleRate)
		if ready.Draft || ready.State != "opened" || ready.SourceBranch == "" || ready.HeadSHA == "" {
			t.Fatalf("ready PR = %+v", ready)
		}
		full, err := c.MR(ctx, ready.Ref)
		if err != nil {
			t.Fatal(err)
		}
		if full.Unresolved != 2 || full.Resolved != 1 || full.Approvals.Required != -1 {
			t.Errorf("threads unresolved=%d resolved=%d approvals=%+v, want 2 / 1 / unknown", full.Unresolved, full.Resolved, full.Approvals)
		}
		ds, err := c.Discussions(ctx, ready.Ref)
		if err != nil {
			t.Fatal(err)
		}
		open := 0
		for _, d := range ds {
			if d.Resolvable && !d.Resolved {
				open++
				if d.Position == nil || d.Position.Path != "limit/bucket.go" || d.Position.NewLine == 0 {
					t.Errorf("open thread position = %+v", d.Position)
				}
			}
		}
		if open != 2 {
			t.Errorf("%d open threads in Discussions, want 2", open)
		}
		byBranch, err := c.ListMRs(ctx, forge.MRQuery{Project: s.api, State: "all", SourceBranch: ready.SourceBranch})
		if err != nil || len(byBranch) != 1 || byBranch[0].Ref != ready.Ref {
			t.Errorf("SourceBranch filter = %+v, %v", byBranch, err)
		}

		draft := s.mr(t, s.web, s.titleAu)
		if !draft.Draft || draft.State != "opened" {
			t.Errorf("draft PR = %+v", draft)
		}
		if _, err := c.Issue(ctx, forge.Ref{Project: s.api, IID: ready.Ref.IID, Kind: forge.KindIssue}); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("a PR number read as an issue: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("diff resolves the rename", func(t *testing.T) {
		rn := s.mr(t, s.api, "Rename bucket to token bucket")
		d, err := c.MRDiff(ctx, rn.Ref)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Files) != 1 || d.HeadSHA == "" || len(d.HeadSHA) != 40 {
			t.Fatalf("diff = %+v", d)
		}
		f := d.Files[0]
		if !f.Renamed || f.OldPath != "limit/bucket.go" || f.NewPath != "limit/token_bucket.go" || !strings.HasPrefix(f.Diff, "@@ ") {
			t.Errorf("file = %+v", f)
		}
	})

	t.Run("agile reads", func(t *testing.T) {
		les, err := c.LabelEvents(ctx, rate.Ref)
		if err != nil || len(les) == 0 {
			t.Errorf("LabelEvents = %+v, %v", les, err)
		}
		ses, err := c.SprintEvents(ctx, rate.Ref)
		if err != nil || len(ses) == 0 || ses[0].Title != "Sprint 42" || ses[0].ID == 0 || ses[0].Action != "add" {
			t.Errorf("SprintEvents = %+v, %v", ses, err)
		}
		evs, err := c.ProjectEvents(ctx, s.api, time.Time{}, time.Time{})
		if err != nil || len(evs) == 0 {
			t.Errorf("ProjectEvents = %d events, %v", len(evs), err)
		}
	})

	t.Run("repository reads", func(t *testing.T) {
		if b, err := c.DefaultBranch(ctx, s.api); err != nil || b != "main" {
			t.Errorf("DefaultBranch = %q, %v", b, err)
		}
		tmpl, err := c.ReadFile(ctx, s.api, "main", ".github/pull_request_template.md")
		if err != nil || !strings.Contains(string(tmpl), "Checklist") {
			t.Errorf("template = %q, %v", tmpl, err)
		}
		if _, err := c.ReadFile(ctx, s.api, "main", "no/such/file.md"); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("missing file err = %v, want ErrNotFound", err)
		}
		files, err := c.ListFiles(ctx, s.api, "main", ".github")
		if err != nil || !contains(files, ".github/pull_request_template.md") {
			t.Errorf("ListFiles = %v, %v", files, err)
		}
		if none, err := c.ListFiles(ctx, s.api, "main", "no/such/dir"); err != nil || len(none) != 0 {
			t.Errorf("missing dir = %v, %v", none, err)
		}
		bs, err := c.Branches(ctx, s.api, "rename-")
		if err != nil || len(bs) == 0 {
			t.Errorf("Branches = %v, %v", bs, err)
		}
	})

	t.Run("a missing project is ErrNotFound", func(t *testing.T) {
		if _, err := c.ListIssues(ctx, forge.IssueQuery{Project: s.owner + "/ade-sandbox-nonexistent"}); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
