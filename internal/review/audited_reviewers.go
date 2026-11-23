package review

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// ParseUsernames reads "@alice, bob @carol" into usernames: separated by
// spaces or commas, a leading @ dropped, repeats (any case) kept once.
func ParseUsernames(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		if f = strings.TrimPrefix(f, "@"); f != "" && !containsUser(out, f) {
			out = append(out, f)
		}
	}
	return out
}

func containsUser(names []string, name string) bool {
	return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, name) })
}

// Review request targets: what happens to each named user.
const (
	RequestAdd   = "add"   // not a reviewer yet: added
	RequestAgain = "again" // already a reviewer: asked again
)

// ReviewTarget is one user a review request goes to.
type ReviewTarget struct {
	Username string `json:"username"`
	Action   string `json:"action"`         // RequestAdd or RequestAgain
	Was      string `json:"was,omitempty"`  // their state now (forge.Reviewer*), for an again
	Name     string `json:"name,omitempty"` // display name when the forge gave one
}

// PlanReviewRequest says whom a request goes to: the named users, plus every
// current reviewer when again is set. Each is an add (not a reviewer now) or
// an again. The order is the named users first, then the reviewers.
func PlanReviewRequest(current []forge.Reviewer, named []string, again bool) []ReviewTarget {
	var out []ReviewTarget
	add := func(username, name string) {
		if containsTarget(out, username) {
			return
		}
		t := ReviewTarget{Username: username, Action: RequestAdd, Name: name}
		for _, r := range current {
			if strings.EqualFold(r.User.Username, username) {
				t.Action, t.Was, t.Name = RequestAgain, r.State, r.User.Name
				t.Username = r.User.Username
				break
			}
		}
		out = append(out, t)
	}
	for _, n := range named {
		add(n, "")
	}
	if again {
		for _, r := range current {
			add(r.User.Username, r.User.Name)
		}
	}
	return out
}

func containsTarget(ts []ReviewTarget, username string) bool {
	return slices.ContainsFunc(ts, func(t ReviewTarget) bool { return strings.EqualFold(t.Username, username) })
}

// Usernames lists the targets' usernames.
func Usernames(ts []ReviewTarget) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Username
	}
	return out
}

// Reviewers reads who reviews the MR (not a write, so not audited). The
// wrapped driver must implement forge.MRReviewers.
func (a *AuditedWriter) Reviewers(ctx context.Context, mr forge.Ref) ([]forge.Reviewer, error) {
	rv, ok := a.MRWriter.(forge.MRReviewers)
	if !ok {
		return nil, errNoReviewers(mr)
	}
	return rv.Reviewers(ctx, mr)
}

// RequestReview asks users to review, or to review again (forge.mr.request_review).
// again records whether the request was for the current reviewers as well; it
// changes nothing on the forge, which already tells new reviewers from
// existing ones. The wrapped driver must implement forge.MRReviewers; one
// that does not is an error, not a silent no-op.
func (a *AuditedWriter) RequestReview(ctx context.Context, mr forge.Ref, users []string, again bool) error {
	rv, ok := a.MRWriter.(forge.MRReviewers)
	if !ok {
		return errNoReviewers(mr)
	}
	return a.do("forge.mr.request_review", mr.String(), "", map[string]any{"users": users, "again": again}, func() error {
		return rv.RequestReview(ctx, mr, users)
	})
}

func errNoReviewers(mr forge.Ref) error {
	return fmt.Errorf("this forge cannot request reviews yet (%s)", mr)
}
