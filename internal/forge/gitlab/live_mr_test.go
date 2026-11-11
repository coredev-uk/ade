//go:build live

package gitlab_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// Round-trips the MR write endpoints against the Free sandbox group and leaves
// its threads, drafts and approvals as it found them. Needs a token with api
// scope:
//
//	set -a; . ./.env; set +a; go test -tags live -run LiveMRWrites -v ./internal/forge/gitlab
//
// It only touches api!1, api!2 and web!1 of the sandbox group. It never merges.
func TestLiveMRWrites(t *testing.T) {
	c, group := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	api1 := forge.Ref{Project: group + "/api", IID: 1, Kind: forge.KindMR}
	api2 := forge.Ref{Project: group + "/api", IID: 2, Kind: forge.KindMR}
	web1 := forge.Ref{Project: group + "/web", IID: 1, Kind: forge.KindMR}

	t.Run("renamed file diff", func(t *testing.T) {
		d, err := c.MRDiff(ctx, api2)
		if err != nil {
			t.Fatal(err)
		}
		if d.HeadSHA == "" || d.BaseSHA == "" || d.StartSHA == "" || len(d.Files) != 1 {
			t.Fatalf("diff = %+v", d)
		}
		f := d.Files[0]
		if !f.Renamed || f.OldPath != "limit/bucket.go" || f.NewPath != "limit/token_bucket.go" || !strings.HasPrefix(f.Diff, "@@ ") {
			t.Errorf("file = %+v", f)
		}
	})

	t.Run("reply resolve unresolve", func(t *testing.T) {
		ds, err := c.Discussions(ctx, api1)
		if err != nil {
			t.Fatal(err)
		}
		var thread forge.Discussion
		for _, d := range ds {
			if d.Resolvable && !d.Resolved && strings.Contains(d.Notes[0].Body, "burst of 50") {
				thread = d
			}
		}
		if thread.ID == "" {
			t.Skip("the seeded unresolved thread on api!1 is gone")
		}
		body := "ade live test reply " + time.Now().UTC().Format(time.RFC3339)
		n, err := c.Reply(ctx, api1, thread.ID, body)
		if err != nil || n.Body != body || n.ID == 0 {
			t.Fatalf("reply = %+v, %v", n, err)
		}
		state := func() bool {
			ds, err := c.Discussions(ctx, api1)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range ds {
				if d.ID == thread.ID {
					return d.Resolved
				}
			}
			t.Fatal("thread vanished")
			return false
		}
		if err := c.Resolve(ctx, api1, thread.ID, true); err != nil || !state() {
			t.Fatalf("resolve: %v, resolved=%v", err, state())
		}
		if err := c.Resolve(ctx, api1, thread.ID, false); err != nil || state() {
			t.Fatalf("unresolve: %v, resolved=%v", err, state())
		}
	})

	t.Run("ready and back to draft", func(t *testing.T) {
		if err := c.SetDraft(ctx, web1, false); err != nil {
			t.Fatal(err)
		}
		mr, err := c.MR(ctx, web1)
		if err != nil || mr.Draft || strings.HasPrefix(mr.Title, "Draft") {
			t.Fatalf("after ready: %+v, %v", mr, err)
		}
		if err := c.SetDraft(ctx, web1, true); err != nil {
			t.Fatal(err)
		}
		mr, err = c.MR(ctx, web1)
		if err != nil || !mr.Draft || mr.Title != "Draft: Paginate the audit log view" {
			t.Fatalf("after draft: %+v, %v", mr, err)
		}
	})

	t.Run("approve and unapprove", func(t *testing.T) {
		mr, err := c.MR(ctx, api1)
		if err != nil {
			t.Fatal(err)
		}
		me, _ := c.Me(ctx)
		approvedByMe := func() bool {
			mr, err := c.MR(ctx, api1)
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range mr.Approvals.ApprovedBy {
				if u.Username == me.Username {
					return true
				}
			}
			return false
		}
		if err := c.Approve(ctx, api1, mr.HeadSHA); err != nil || !approvedByMe() {
			t.Fatalf("approve: %v, approved=%v", err, approvedByMe())
		}
		if err := c.Unapprove(ctx, api1); err != nil || approvedByMe() {
			t.Fatalf("unapprove: %v, approved=%v", err, approvedByMe())
		}
	})

	t.Run("retry needs a pipeline", func(t *testing.T) {
		mr, err := c.MR(ctx, api1)
		if err != nil {
			t.Fatal(err)
		}
		if mr.Pipeline != nil {
			t.Skip("api!1 has a pipeline; retry is exercised elsewhere")
		}
		t.Log("api!1 has no pipeline (the sandbox has no CI): retry skipped")
	})
}
