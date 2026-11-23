package gitlab_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

var _ forge.MRRebaser = (*gitlab.Client)(nil)

// rebaseClient is a client whose poll waits are counted instead of slept.
func rebaseClient(srv *fake.Server, sleeps *int) *gitlab.Client {
	return gitlab.New(srv.URL, fake.Token, gitlab.WithClock(func() time.Time { return clock }),
		gitlab.WithSleep(func(context.Context, time.Duration) error { *sleeps++; return nil }))
}

func TestRebaseMR(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, _ *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 1)
		var sleeps int
		c := rebaseClient(srv, &sleeps)
		if err := srv.SetBehind(ref, true); err != nil {
			t.Fatal(err)
		}
		if err := srv.SetRebaseDelay(ref, 2); err != nil {
			t.Fatal(err)
		}
		before, _ := c.MR(ctx, ref)
		if before.DetailedStatus != "need_rebase" {
			t.Fatalf("behind MR status = %q, want need_rebase", before.DetailedStatus)
		}
		// A merge of a behind MR is refused until it is rebased.
		apiStatus(t, c.Merge(ctx, ref, forge.MergeOpts{SHA: before.HeadSHA}), http.StatusMethodNotAllowed)

		res, err := c.RebaseMR(ctx, ref, forge.RebaseOpts{SHA: before.HeadSHA, SkipCI: true})
		if err != nil {
			t.Fatal(err)
		}
		if sleeps != 2 {
			t.Errorf("polled with %d waits, want 2: the rebase runs in the background", sleeps)
		}
		if calls, skip, _ := srv.Rebases(ref); calls != 1 || !skip {
			t.Errorf("rebase requests = %d skip_ci = %v, want 1 and true", calls, skip)
		}
		after, _ := c.MR(ctx, ref)
		if res.Method != "rebase" || res.HeadSHA == "" || res.HeadSHA != after.HeadSHA || after.HeadSHA == before.HeadSHA {
			t.Errorf("result %+v, head %s -> %s: want a new head reported", res, before.HeadSHA, after.HeadSHA)
		}
		if after.DetailedStatus == "need_rebase" {
			t.Errorf("status after rebase = %q", after.DetailedStatus)
		}
	})
}

func TestRebaseMRConflict(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, _ *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 1)
		var sleeps int
		c := rebaseClient(srv, &sleeps)
		if err := srv.SetConflicts(ref, true); err != nil {
			t.Fatal(err)
		}
		before, _ := c.MR(ctx, ref)
		if !before.HasConflicts || before.DetailedStatus != "conflict" {
			t.Fatalf("conflicting MR = has_conflicts %v status %q", before.HasConflicts, before.DetailedStatus)
		}
		_, err := c.RebaseMR(ctx, ref, forge.RebaseOpts{})
		if !errors.Is(err, forge.ErrRebaseFailed) {
			t.Fatalf("err = %v, want ErrRebaseFailed", err)
		}
		after, _ := c.MR(ctx, ref)
		if after.HeadSHA != before.HeadSHA {
			t.Error("a failed rebase moved the head")
		}
	})
}

func TestRebaseMRRefusals(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		if _, err := c.RebaseMR(ctx, issueRef(apiProj, 1), forge.RebaseOpts{}); err == nil {
			t.Error("an issue ref was accepted")
		}
		if _, err := c.RebaseMR(ctx, mrRef(apiProj, 99), forge.RebaseOpts{}); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown MR err = %v, want ErrNotFound", err)
		}
		ref := mrRef(apiProj, 1)
		if err := srv.MergeMR(ref); err != nil {
			t.Fatal(err)
		}
		_, err := c.RebaseMR(ctx, ref, forge.RebaseOpts{})
		apiStatus(t, err, http.StatusForbidden)
		if calls, _, _ := srv.Rebases(ref); calls != 0 {
			t.Errorf("a merged MR accepted %d rebases", calls)
		}
	})
}
