package github_test

import (
	"context"
	"slices"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
)

var _ forge.MRReviewers = (*github.Client)(nil)

func reviewerStates(t *testing.T, c *github.Client, ref forge.Ref) map[string]string {
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

// api!2 is authored by the viewer, asks teammate for a review, and teammate
// has commented: the pending request wins, and the author (who replied in the
// review) is not a reviewer.
func TestReviewers(t *testing.T) {
	c, srv := newClient(t)
	ref := mrRef(apiProj, 2)
	if got := reviewerStates(t, c, ref); len(got) != 1 || got["teammate"] != forge.ReviewerRequested {
		t.Fatalf("api!2 reviewers = %v", got)
	}
	// teammate reviews: the request is answered, the verdict shows.
	if err := srv.SubmitReviewAs(ref, "teammate", "CHANGES_REQUESTED", "Needs the 429 test."); err != nil {
		t.Fatal(err)
	}
	if got := reviewerStates(t, c, ref); got["teammate"] != forge.ReviewerChangesRequested || len(got) != 1 {
		t.Errorf("after the review: %v", got)
	}
	// A later plain comment does not undo the request for changes.
	if err := srv.SubmitReviewAs(ref, "teammate", "COMMENTED", "One more thing."); err != nil {
		t.Fatal(err)
	}
	if got := reviewerStates(t, c, ref); got["teammate"] != forge.ReviewerChangesRequested {
		t.Errorf("a comment undid the verdict: %v", got)
	}
	if err := srv.SubmitReviewAs(ref, "reviewer-bot", "APPROVED", ""); err != nil {
		t.Fatal(err)
	}
	if got := reviewerStates(t, c, ref); got["reviewer-bot"] != forge.ReviewerApproved {
		t.Errorf("approver missing: %v", got)
	}
}

func TestRequestReview(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	ref := mrRef(apiProj, 2)
	if err := srv.SubmitReviewAs(ref, "teammate", "CHANGES_REQUESTED", "Needs the 429 test."); err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.RequestedReviewers(ref); len(got) != 0 {
		t.Fatalf("requested after a review = %v", got)
	}
	// Naming a reviewer who already reviewed asks them again.
	if err := c.RequestReview(ctx, ref, []string{"@teammate", "reviewer-bot"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.RequestedReviewers(ref); !slices.Equal(got, []string{"teammate", "reviewer-bot"}) {
		t.Errorf("requested = %v", got)
	}
	if got := reviewerStates(t, c, ref); got["teammate"] != forge.ReviewerRequested {
		t.Errorf("teammate = %q, want requested again", got["teammate"])
	}
	// The author cannot be asked: GitHub answers 422.
	apiStatus(t, c.RequestReview(ctx, ref, []string{"demo-user"}), 422)
	if err := c.RequestReview(ctx, ref, nil); err == nil {
		t.Error("an empty request was accepted")
	}
	if err := c.RequestReview(ctx, mrRef(apiProj, 99), []string{"teammate"}); err == nil {
		t.Error("an unknown PR was accepted")
	}
}
