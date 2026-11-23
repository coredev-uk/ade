package gitlab_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

var _ forge.MRReviewers = (*gitlab.Client)(nil)

func reviewerStates(t *testing.T, c *gitlab.Client, ref forge.Ref) map[string]string {
	t.Helper()
	rs, err := c.Reviewers(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range rs {
		out[r.User.Username] = r.State
	}
	return out
}

func TestReviewers(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ref := mrRef(apiProj, 1)
		if got := reviewerStates(t, c, ref); len(got) != 1 || got["reviewer-rae"] != forge.ReviewerRequested {
			t.Fatalf("fresh reviewers = %v", got)
		}
		for glState, want := range map[string]string{
			"review_started":    forge.ReviewerReviewing,
			"approved":          forge.ReviewerApproved,
			"requested_changes": forge.ReviewerChangesRequested,
		} {
			if err := srv.SetReviewState(ref, "reviewer-rae", glState); err != nil {
				t.Fatal(err)
			}
			if got := reviewerStates(t, c, ref)["reviewer-rae"]; got != want {
				t.Errorf("%s reads %q, want %q", glState, got, want)
			}
		}
		if _, err := c.Reviewers(context.Background(), issueRef(apiProj, 1)); err == nil {
			t.Error("an issue ref was accepted")
		}
	})
}

// A new user is added with reviewer_ids and the reviewers already on the MR
// stay (the call replaces the set).
func TestRequestReviewAddsKeepingTheSet(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 1)
		srv.AddUser("reviewer-sam", "Sam Second")
		if err := srv.SetReviewState(ref, "reviewer-rae", "requested_changes"); err != nil {
			t.Fatal(err)
		}
		if err := c.RequestReview(ctx, ref, []string{"@Reviewer-Sam"}); err != nil {
			t.Fatal(err)
		}
		names, _ := srv.MRReviewers(ref)
		if !slices.Equal(names, []string{"reviewer-rae", "reviewer-sam"}) {
			t.Fatalf("reviewers = %v, want rae kept and sam added", names)
		}
		got := reviewerStates(t, c, ref)
		if got["reviewer-rae"] != forge.ReviewerChangesRequested || got["reviewer-sam"] != forge.ReviewerRequested {
			t.Errorf("states = %v: adding sam must not touch rae's verdict", got)
		}
	})
}

// A reviewer who already reviewed is asked again through the rereview
// mutation: their state goes back to unreviewed.
func TestRequestReviewAsksAgain(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 1)
		srv.AddUser("reviewer-sam", "Sam Second")
		if err := srv.SetReviewState(ref, "reviewer-rae", "requested_changes"); err != nil {
			t.Fatal(err)
		}
		if err := srv.SetReviewState(ref, "reviewer-sam", "approved"); err != nil {
			t.Fatal(err)
		}
		if err := c.RequestReview(ctx, ref, []string{"reviewer-rae", "reviewer-rae"}); err != nil {
			t.Fatal(err)
		}
		if st, _ := srv.ReviewerState(ref, "reviewer-rae"); st != "unreviewed" {
			t.Errorf("rae = %q, want unreviewed after the re-request", st)
		}
		if st, _ := srv.ReviewerState(ref, "reviewer-sam"); st != "approved" {
			t.Errorf("sam = %q: a user who was not named keeps their verdict", st)
		}
		// Both at once: one added, one asked again.
		srv.AddUser("reviewer-tia", "Tia Third")
		if err := c.RequestReview(ctx, ref, []string{"reviewer-tia", "reviewer-sam"}); err != nil {
			t.Fatal(err)
		}
		names, _ := srv.MRReviewers(ref)
		if !slices.Equal(names, []string{"reviewer-rae", "reviewer-sam", "reviewer-tia"}) {
			t.Errorf("reviewers = %v", names)
		}
		if st, _ := srv.ReviewerState(ref, "reviewer-sam"); st != "unreviewed" {
			t.Errorf("sam = %q, want unreviewed", st)
		}
	})
}

func TestRequestReviewRefusals(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 1)
		if err := c.RequestReview(ctx, ref, []string{"nobody-here"}); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown user err = %v, want ErrNotFound", err)
		}
		if err := c.RequestReview(ctx, ref, nil); err == nil {
			t.Error("an empty request was accepted")
		}
		if err := c.RequestReview(ctx, mrRef(apiProj, 99), []string{"reviewer-rae"}); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown MR err = %v, want ErrNotFound", err)
		}
		if names, _ := srv.MRReviewers(ref); !slices.Equal(names, []string{"reviewer-rae"}) {
			t.Errorf("a refused request changed the reviewers: %v", names)
		}
	})
}
