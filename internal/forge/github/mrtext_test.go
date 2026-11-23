package github_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
)

func TestMRCommitsListsTheBranchsCommitsOldestFirst(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	for _, n := range []int{2, 4} {
		commits, err := c.MRCommits(ctx, mrRef(apiProj, n))
		if err != nil {
			t.Fatalf("api!%d: %v", n, err)
		}
		if len(commits) == 0 {
			t.Fatalf("api!%d: no commits", n)
		}
		for _, cm := range commits {
			if len(cm.SHA) != 40 || cm.Title == "" || strings.Contains(cm.Title, "\n") || cm.Author == "" {
				t.Errorf("api!%d: commit %+v", n, cm)
			}
		}
	}
	if _, err := c.MRCommits(ctx, mrRef(apiProj, 99)); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown PR: %v", err)
	}
	if _, err := c.MRCommits(ctx, issueRef(apiProj, 1)); err == nil {
		t.Error("an issue ref was accepted")
	}
}

func TestSetMRDescriptionRewritesTheBodyAndNothingElse(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	ref := mrRef(apiProj, 2)
	before, err := c.MR(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	const text = "## What\nA per-tenant token bucket.\n\n/close stays plain text here\n"
	if err := c.SetMRDescription(ctx, ref, text); err != nil {
		t.Fatal(err)
	}
	body, writes, err := srv.Description(ref)
	if err != nil || body != text || writes != 1 {
		t.Fatalf("body %q after %d writes (%v)", body, writes, err)
	}
	after, err := c.MR(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if after.Description != text || after.Title != before.Title || after.Draft != before.Draft || after.State != before.State || after.HeadSHA != before.HeadSHA {
		t.Errorf("only the description changes:\nbefore %+v\nafter  %+v", before, after)
	}
	// An empty body clears it; an issue ref and an unknown PR are refused.
	if err := c.SetMRDescription(ctx, ref, ""); err != nil {
		t.Fatal(err)
	}
	if body, _, _ := srv.Description(ref); body != "" {
		t.Errorf("body %q", body)
	}
	if err := c.SetMRDescription(ctx, issueRef(apiProj, 1), "x"); err == nil {
		t.Error("an issue ref was accepted")
	}
	apiStatus(t, c.SetMRDescription(ctx, mrRef(apiProj, 99), "x"), 404)
}
