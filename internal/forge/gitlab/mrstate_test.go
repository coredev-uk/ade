package gitlab_test

import (
	"context"
	"errors"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

func TestCloseReopenMR(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 1)
		state := func(r forge.Ref) string {
			t.Helper()
			mr, err := c.MR(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			return mr.State
		}
		if err := c.CloseMR(ctx, ref); err != nil || state(ref) != "closed" {
			t.Fatalf("close: %v, state %q", err, state(ref))
		}
		mrs, err := c.ListMRs(ctx, forge.MRQuery{Project: apiProj, SourceBranch: mustMR(t, c, ref).SourceBranch, State: "all"})
		if err != nil || len(mrs) == 0 {
			t.Fatalf("a closed MR must list under state=all: %v %v", mrs, err)
		}
		apiStatus(t, c.CloseMR(ctx, ref), 400)

		// Another open MR on the same branches blocks the reopen.
		src := mustMR(t, c, ref)
		other, err := c.CreateMR(ctx, forge.MRDraft{Project: apiProj, Source: src.SourceBranch, Target: src.TargetBranch, Title: "again"})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.ReopenMR(ctx, ref); !errors.Is(err, forge.ErrExists) {
			t.Fatalf("reopen beside an open MR: %v, want ErrExists", err)
		}
		if err := c.CloseMR(ctx, other.Ref); err != nil {
			t.Fatal(err)
		}
		if err := c.ReopenMR(ctx, ref); err != nil || state(ref) != "opened" {
			t.Fatalf("reopen: %v, state %q", err, state(ref))
		}
		apiStatus(t, c.ReopenMR(ctx, ref), 400)

		merged := mrRef(webProj, 2)
		apiStatus(t, c.ReopenMR(ctx, merged), 400)
		apiStatus(t, c.CloseMR(ctx, merged), 400)
		if err := c.CloseMR(ctx, mrRef(apiProj, 99)); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown MR: %v", err)
		}
		if err := c.CloseMR(ctx, issueRef(apiProj, 1)); err == nil {
			t.Error("an issue ref was accepted")
		}
	})
}

func mustMR(t *testing.T, c *gitlab.Client, ref forge.Ref) forge.MR {
	t.Helper()
	mr, err := c.MR(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return mr
}
