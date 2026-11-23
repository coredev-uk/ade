package forge

import (
	"context"
	"errors"
)

// RebaseOpts controls RebaseMR. SHA is the head the caller saw: a forge that
// can pin it (GitHub's expected_head_sha) refuses when the branch has moved.
// SkipCI asks GitLab not to start a pipeline for the rebased head.
type RebaseOpts struct {
	SHA    string `json:"sha,omitempty"`
	SkipCI bool   `json:"skip_ci,omitempty"`
}

// RebaseResult says what RebaseMR did. Method is "rebase" (GitLab rebases the
// source branch onto the target) or "merge" (GitHub's update-branch merges the
// target into the source branch). UpToDate is set when the branch already
// contained the target and nothing changed. HeadSHA is the source branch's
// head afterwards, when the forge said.
type RebaseResult struct {
	Method   string `json:"method"`
	UpToDate bool   `json:"up_to_date,omitempty"`
	HeadSHA  string `json:"head_sha,omitempty"`
}

// MRRebaser is what bringing an MR's source branch up to date with its target
// needs beyond MRWriter. A driver that has not implemented it simply does not
// satisfy it; callers say so and stop. RebaseMR returns when the forge has
// finished (GitLab's rebase and GitHub's update run in the background, so it
// polls), and wraps ErrRebaseFailed when the forge could not do it.
type MRRebaser interface {
	RebaseMR(ctx context.Context, ref Ref, o RebaseOpts) (RebaseResult, error)
}

// ErrRebaseFailed marks a rebase or update the forge could not do, in practice
// a conflict. The branch is untouched; the fix is a local rebase.
var ErrRebaseFailed = errors.New("the forge could not update the branch")
