package github_test

import (
	"context"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
)

// A PR whose head.repo differs from base.repo is a fork MR and names the fork;
// a deleted fork (head.repo null) is a fork with no name; a PR within one
// repository is neither.
func TestForkMR(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	fork, err := srv.AddForkPull(apiProj, "contrib/api", "fix-typo", "Fix a typo", false)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := srv.AddForkPull(apiProj, "ghost/api", "old-work", "Work from a deleted fork", true)
	if err != nil {
		t.Fatal(err)
	}
	mr, err := c.MR(ctx, fork)
	if err != nil {
		t.Fatal(err)
	}
	if !mr.Fork || mr.SourceProject != "contrib/api" || mr.SourceBranch != "fix-typo" {
		t.Fatalf("fork PR = fork %v from %q branch %q", mr.Fork, mr.SourceProject, mr.SourceBranch)
	}
	mr, err = c.MR(ctx, gone)
	if err != nil {
		t.Fatal(err)
	}
	if !mr.Fork || mr.SourceProject != "" {
		t.Fatalf("deleted-fork PR = fork %v from %q", mr.Fork, mr.SourceProject)
	}
	mrs, err := c.ListMRs(ctx, forge.MRQuery{Project: apiProj, State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	forks := map[forge.Ref]string{}
	for _, m := range mrs {
		if m.Fork {
			forks[m.Ref] = m.SourceProject
		}
	}
	if len(forks) != 2 || forks[fork] != "contrib/api" || forks[gone] != "" {
		t.Fatalf("forks in the list = %v", forks)
	}
	own, err := c.MR(ctx, mrRef(apiProj, 2))
	if err != nil || own.Fork || own.SourceProject != "" {
		t.Fatalf("same-repo PR = fork %v from %q, %v", own.Fork, own.SourceProject, err)
	}
}
