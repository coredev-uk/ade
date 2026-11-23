package forge

import "context"

// Reviewer states, as Reviewer.State says them on both forges.
const (
	ReviewerRequested        = "requested"         // asked, no verdict yet (GitLab unreviewed)
	ReviewerReviewing        = "reviewing"         // started a review (GitLab review_started)
	ReviewerCommented        = "commented"         // reviewed without a verdict (GitLab reviewed, GitHub COMMENTED)
	ReviewerApproved         = "approved"          //
	ReviewerChangesRequested = "changes_requested" //
)

// Reviewer is someone asked to review a merge request, or who reviewed it,
// with where their review stands.
type Reviewer struct {
	User  User   `json:"user"`
	State string `json:"state"`
}

// MRReviewers is the reviewer side of a forge: who is reviewing, and asking
// for a review (again). A driver that has not implemented it does not satisfy
// it; callers say so and stop.
type MRReviewers interface {
	// Reviewers lists everyone asked to review or who has reviewed, with
	// their standing verdict. The author is never one.
	Reviewers(ctx context.Context, ref Ref) ([]Reviewer, error)
	// RequestReview asks each user (by username) to review. A user who is not
	// a reviewer yet is added, and the reviewers already on the MR stay. One
	// who already is one, whatever they said before, is asked again: their
	// state goes back to "requested" and they are notified.
	RequestReview(ctx context.Context, ref Ref, usernames []string) error
}
