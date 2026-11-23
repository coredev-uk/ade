package github_test

import (
	"context"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/review"
)

// GitHub reports mergeable_state, not GitLab's detailed status: a clean PR and
// one with failing checks that are not required both merge, a draft does not.
func TestMergeVerdictOnGitHubStates(t *testing.T) {
	ctx := context.Background()
	c, _ := newClient(t)

	clean, err := c.MR(ctx, mrRef(apiProj, 5))
	if err != nil {
		t.Fatal(err)
	}
	if clean.DetailedStatus != "clean" {
		t.Fatalf("api!5 mergeable_state = %q, want clean", clean.DetailedStatus)
	}
	if v := review.Verdict(clean, ""); !v.OK {
		t.Errorf("a clean PR is refused: %+v", v)
	}

	draft, _ := c.MR(ctx, mrRef(webProj, 2))
	v := review.Verdict(draft, "ade mr ready ade-demo/web!2 marks it ready")
	if v.OK || !strings.Contains(v.Why, "still a draft") || !strings.Contains(v.Why, "ade mr ready") || strings.Contains(v.Why, "GitLab") {
		t.Errorf("draft verdict = %+v", v)
	}

	// Ready, with a failing check that is not required: unstable still merges.
	if err := c.SetDraft(ctx, mrRef(webProj, 2), false); err != nil {
		t.Fatal(err)
	}
	unstable, _ := c.MR(ctx, mrRef(webProj, 2))
	if unstable.DetailedStatus != "unstable" {
		t.Fatalf("web!2 mergeable_state = %q, want unstable", unstable.DetailedStatus)
	}
	if v := review.Verdict(unstable, ""); !v.OK {
		t.Errorf("an unstable PR is refused: %+v", v)
	}
}
