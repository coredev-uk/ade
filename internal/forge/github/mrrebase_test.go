package github_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
	"github.com/coredev-uk/ade/internal/forge/github/fake"
)

var _ forge.MRRebaser = (*github.Client)(nil)

func TestRebaseMRUpdatesTheBranch(t *testing.T) {
	var sleeps int
	_, srv := newClient(t)
	c := github.New(srv.URL, fake.Token, github.WithSleep(func(context.Context, time.Duration) error { sleeps++; return nil }))
	ctx := context.Background()
	ref := mrRef(apiProj, 4)
	if err := srv.MoveBase(ref, map[string]string{"docs/NEWS.md": "news\n"}); err != nil {
		t.Fatal(err)
	}
	srv.SetUpdateDelay(2)
	before, err := c.MR(ctx, ref)
	if err != nil || before.DetailedStatus != "behind" {
		t.Fatalf("behind PR = %+v, %v", before, err)
	}
	res, err := c.RebaseMR(ctx, ref, forge.RebaseOpts{SHA: before.HeadSHA})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := c.MR(ctx, ref)
	if res.Method != "merge" || res.UpToDate || res.HeadSHA != after.HeadSHA || after.HeadSHA == before.HeadSHA {
		t.Errorf("result %+v, head %s -> %s", res, before.HeadSHA, after.HeadSHA)
	}
	if sleeps == 0 {
		t.Error("the update landed with no polling: GitHub works in the background")
	}
	if after.DetailedStatus == "behind" {
		t.Errorf("status after the update = %q", after.DetailedStatus)
	}
	if n, _ := srv.Updates(ref); n != 1 {
		t.Errorf("update-branch requests = %d, want 1", n)
	}

	// Nothing left to merge: not an error, and no second request lands.
	again, err := c.RebaseMR(ctx, ref, forge.RebaseOpts{})
	if err != nil || !again.UpToDate {
		t.Errorf("second update = %+v, %v, want up to date", again, err)
	}
}

func TestRebaseMRPinsTheHead(t *testing.T) {
	c, srv := newClient(t)
	ref := mrRef(apiProj, 4)
	if err := srv.MoveBase(ref, map[string]string{"docs/NEWS.md": "news\n"}); err != nil {
		t.Fatal(err)
	}
	_, err := c.RebaseMR(context.Background(), ref, forge.RebaseOpts{SHA: "0000000000000000000000000000000000000000"})
	apiStatus(t, err, http.StatusUnprocessableEntity)
	if n, _ := srv.Updates(ref); n != 0 {
		t.Errorf("a stale head pin was accepted: %d updates", n)
	}
}

func TestRebaseMRConflict(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	ref := mrRef(apiProj, 4)
	// api!4 changes limit/token_bucket.go; so does the base.
	if err := srv.MoveBase(ref, map[string]string{"limit/token_bucket.go": "package limit\n// other\n"}); err != nil {
		t.Fatal(err)
	}
	mr, _ := c.MR(ctx, ref)
	if !mr.HasConflicts {
		t.Fatalf("conflicting PR = %+v", mr)
	}
	_, err := c.RebaseMR(ctx, ref, forge.RebaseOpts{SHA: mr.HeadSHA})
	if !errors.Is(err, forge.ErrRebaseFailed) {
		t.Fatalf("err = %v, want ErrRebaseFailed", err)
	}
	if after, _ := c.MR(ctx, ref); after.HeadSHA != mr.HeadSHA {
		t.Error("a conflicting update moved the head")
	}
}

func TestRebaseMRRefusals(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	if _, err := c.RebaseMR(ctx, issueRef(apiProj, 1), forge.RebaseOpts{}); err == nil {
		t.Error("an issue ref was accepted")
	}
	if _, err := c.RebaseMR(ctx, mrRef(apiProj, 99), forge.RebaseOpts{}); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown PR err = %v, want ErrNotFound", err)
	}
}
