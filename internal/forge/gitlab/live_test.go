//go:build live

package gitlab_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
)

// Runs against the Free sandbox group seeded by scripts/sandbox/gitlab-seed.sh:
//
//	. scripts/sandbox/env.sh && go test -tags live -run Live -v ./internal/forge/gitlab
//
// It only reads. GITLAB_PAT needs read_api; GITLAB_URL and ADE_SANDBOX_GROUP
// override gitlab.com and ade-demo-env.
func liveClient(t *testing.T) (c *gitlab.Client, group string) {
	t.Helper()
	token := os.Getenv("GITLAB_PAT")
	if token == "" {
		t.Skip("GITLAB_PAT not set")
	}
	base := os.Getenv("GITLAB_URL")
	if base == "" {
		base = "https://gitlab.com"
	}
	group = os.Getenv("ADE_SANDBOX_GROUP")
	if group == "" {
		group = "ade-demo-env"
	}
	return gitlab.New(base, token), group
}

func TestLiveSandbox(t *testing.T) {
	c, group := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tracker := group + "/tracker"

	t.Run("me", func(t *testing.T) {
		me, err := c.Me(ctx)
		if err != nil || me.Username == "" {
			t.Fatalf("Me = %+v, %v", me, err)
		}
	})

	t.Run("tracker issues carry milestones", func(t *testing.T) {
		issues, err := c.ListIssues(ctx, forge.IssueQuery{Project: tracker, State: "all"})
		if err != nil {
			t.Fatal(err)
		}
		if len(issues) != 11 {
			t.Fatalf("tracker has %d issues, want 11", len(issues))
		}
		for _, is := range issues {
			if is.Milestone == nil {
				t.Errorf("%v has no milestone", is.Ref)
			}
			if is.Weight != nil || is.Iteration != nil {
				t.Errorf("%v carries Premium data on Free: weight %v iteration %v", is.Ref, is.Weight, is.Iteration)
			}
		}
		for _, is := range issues {
			if is.Ref.IID == 1 && is.Tasks != (forge.TaskCount{Done: 1, Total: 4}) {
				t.Errorf("tracker#1 tasks = %+v, want 1/4", is.Tasks)
			}
		}
	})

	t.Run("links", func(t *testing.T) {
		got, err := c.Links(ctx, forge.Ref{Project: tracker, IID: 1, Kind: forge.KindIssue})
		if err != nil {
			t.Fatal(err)
		}
		want := forge.Link{Ref: forge.Ref{Project: group + "/api", IID: 1, Kind: forge.KindIssue}, Type: "relates_to"}
		if len(got) != 1 || got[0] != want {
			t.Errorf("links = %+v, want %+v", got, want)
		}
	})

	t.Run("merge requests", func(t *testing.T) {
		m, err := c.MR(ctx, forge.Ref{Project: group + "/api", IID: 1, Kind: forge.KindMR})
		if err != nil {
			t.Fatal(err)
		}
		if m.Unresolved != 2 || m.Resolved != 1 || m.Draft || m.State != "opened" {
			t.Errorf("api!1 = unresolved %d resolved %d draft %v state %s, want 2/1/false/opened", m.Unresolved, m.Resolved, m.Draft, m.State)
		}
		if m.Approvals.Required != -1 {
			t.Errorf("Free approvals Required = %d, want -1", m.Approvals.Required)
		}
		web1, err := c.MR(ctx, forge.Ref{Project: group + "/web", IID: 1, Kind: forge.KindMR})
		if err != nil || !web1.Draft {
			t.Errorf("web!1 = %+v, %v; want draft", web1, err)
		}
		web2, err := c.MR(ctx, forge.Ref{Project: group + "/web", IID: 2, Kind: forge.KindMR})
		if err != nil || web2.State != "merged" || web2.MergedAt == nil {
			t.Errorf("web!2 = %+v, %v; want merged", web2, err)
		}
		related, err := c.MRsForIssue(ctx, forge.Ref{Project: tracker, IID: 1, Kind: forge.KindIssue})
		if err != nil || len(related) != 1 || related[0].Ref.String() != group+"/api!1" {
			t.Errorf("MRs for tracker#1 = %+v, %v", related, err)
		}
	})

	t.Run("milestones and iterations", func(t *testing.T) {
		ms, err := c.Milestones(ctx, group)
		if err != nil || len(ms) != 3 {
			t.Fatalf("milestones = %+v, %v", ms, err)
		}
		if ms[1].Title != "Sprint 42" || ms[1].Start.String() != "2026-11-02" || ms[1].Due.String() != "2026-11-13" {
			t.Errorf("Sprint 42 = %+v", ms[1])
		}
		if _, err := c.Iterations(ctx, group, ""); !errors.Is(err, forge.ErrUnavailable) {
			t.Errorf("Free iterations err = %v, want ErrUnavailable", err)
		}
	})

	t.Run("capabilities are Free only", func(t *testing.T) {
		caps, err := c.Capabilities(ctx, group)
		if err != nil {
			t.Fatal(err)
		}
		want := forge.Caps{ProbedAt: caps.ProbedAt}
		if caps != want {
			t.Errorf("caps = %+v, want every gated feature false", caps)
		}
		t.Logf("caps: %+v", caps)
	})
}
