package gitlab_test

import (
	"context"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// A fork MR has source_project_id != target_project_id: MR and ListMRs say Fork
// and name the source project (read once from GET /projects/:id); an MR within
// one project says neither.
func TestForkMR(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		ref, err := srv.AddForkMR(apiProj, "contrib/api", "fix-typo", "Fix a typo")
		if err != nil {
			t.Fatal(err)
		}
		mr, err := c.MR(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if !mr.Fork || mr.SourceProject != "contrib/api" || mr.SourceBranch != "fix-typo" {
			t.Fatalf("fork MR = fork %v from %q branch %q", mr.Fork, mr.SourceProject, mr.SourceBranch)
		}
		mrs, err := c.ListMRs(ctx, forge.MRQuery{Project: apiProj, State: "all"})
		if err != nil {
			t.Fatal(err)
		}
		forks := 0
		for _, m := range mrs {
			if m.Fork {
				forks++
				if m.Ref != ref || m.SourceProject != "contrib/api" {
					t.Errorf("list fork = %s from %q", m.Ref, m.SourceProject)
				}
			} else if m.SourceProject != "" {
				t.Errorf("%s names a source project %q without being a fork", m.Ref, m.SourceProject)
			}
		}
		if forks != 1 {
			t.Fatalf("%d fork MRs in the list, want 1", forks)
		}
		// The source project is read once, however many MRs come from it.
		before := projectReads(srv)
		if _, err := c.MR(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if got := projectReads(srv); got != before {
			t.Errorf("source project read again: %d requests, had %d", got, before)
		}
		own, err := c.MR(ctx, mrRef(apiProj, 1))
		if err != nil || own.Fork || own.SourceProject != "" {
			t.Fatalf("same-project MR = fork %v from %q, %v", own.Fork, own.SourceProject, err)
		}
	})
}

// projectReads counts GET /projects/:id requests (no sub-resource).
func projectReads(srv *fake.Server) int {
	n := 0
	for _, r := range srv.Requests() {
		if rest, ok := strings.CutPrefix(r.Path, "/api/v4/projects/"); ok && r.Method == "GET" && !strings.Contains(rest, "/") {
			n++
		}
	}
	return n
}
