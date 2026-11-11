package gitlab_test

import (
	"context"
	"errors"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

var _ forge.WeightWriter = (*gitlab.Client)(nil)

func TestSetWeight(t *testing.T) {
	forEachTier(t, func(t *testing.T, tier fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		ref := issueRef(tracker, 6) // unweighted on Premium
		two := 2
		err := c.SetWeight(ctx, ref, &two)
		if tier == fake.Free {
			if !errors.Is(err, forge.ErrUnavailable) {
				t.Fatalf("Free SetWeight err = %v, want ErrUnavailable", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		is, err := c.Issue(ctx, ref)
		if err != nil || is.Weight == nil || *is.Weight != 2 {
			t.Fatalf("weight after set = %v, %v; want 2", is.Weight, err)
		}
		if err := c.SetWeight(ctx, ref, nil); err != nil {
			t.Fatal(err)
		}
		if is, _ = c.Issue(ctx, ref); is.Weight != nil {
			t.Errorf("weight after clear = %d, want unweighted", *is.Weight)
		}
		zero := 0
		if err := c.SetWeight(ctx, ref, &zero); err == nil {
			t.Error("weight 0 was sent: it would silently mean unweighted")
		}
		if err := c.SetWeight(ctx, issueRef(tracker, 999), &two); err == nil {
			t.Error("a missing issue was accepted")
		}
		if err := c.SetWeight(ctx, mrRef(tracker, 1), &two); err == nil {
			t.Error("a merge request was accepted")
		}
	})
}
