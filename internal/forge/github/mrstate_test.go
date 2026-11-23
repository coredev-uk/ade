package github_test

import (
	"context"
	"errors"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
)

func TestCloseReopenMR(t *testing.T) {
	ctx := context.Background()
	c, _ := newClient(t)
	ref := mrRef(apiProj, 5)
	src, err := c.MR(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	state := func(r forge.Ref) string {
		mr, err := c.MR(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return mr.State
	}
	if err := c.CloseMR(ctx, ref); err != nil || state(ref) != "closed" {
		t.Fatalf("close: %v, state %q", err, state(ref))
	}
	mrs, err := c.ListMRs(ctx, forge.MRQuery{Project: apiProj, SourceBranch: src.SourceBranch, State: "all"})
	if err != nil || len(mrs) == 0 {
		t.Fatalf("a closed MR must list under state=all: %v %v", mrs, err)
	}
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

	// A merged pull request cannot be reopened.
	head, _ := c.MR(ctx, ref)
	if err := c.Merge(ctx, ref, forge.MergeOpts{SHA: head.HeadSHA}); err != nil {
		t.Fatal(err)
	}
	if err := c.ReopenMR(ctx, ref); err == nil || state(ref) != "merged" {
		t.Errorf("reopen of a merged MR: %v, state %q", err, state(ref))
	}
	if err := c.CloseMR(ctx, mrRef(apiProj, 99)); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown MR: %v", err)
	}
}
