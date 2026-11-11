package gitlab_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

var _ forge.MRWriter = (*gitlab.Client)(nil)

// pos builds a position on the renamed file of api!2 from its diff.
func pos(d forge.Diff, oldLine, newLine int) *forge.DiffPosition {
	f := d.Files[0]
	return &forge.DiffPosition{
		BaseSHA: d.BaseSHA, StartSHA: d.StartSHA, HeadSHA: d.HeadSHA,
		OldPath: f.OldPath, NewPath: f.NewPath, OldLine: oldLine, NewLine: newLine,
	}
}

func apiStatus(t *testing.T, err error, want int) {
	t.Helper()
	var aerr *gitlab.APIError
	if !errors.As(err, &aerr) || aerr.Status != want {
		t.Fatalf("err = %v, want an APIError with status %d", err, want)
	}
}

func TestMRDiff(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		d, err := c.MRDiff(ctx, mrRef(apiProj, 2))
		if err != nil {
			t.Fatal(err)
		}
		if d.BaseSHA == "" || d.StartSHA == "" || d.HeadSHA != "7f6e5d4c3b2a19080f7e6d5c4b3a291807f6e5d4" {
			t.Errorf("SHAs = %+v", d)
		}
		if len(d.Files) != 1 {
			t.Fatalf("files = %+v", d.Files)
		}
		f := d.Files[0]
		if !f.Renamed || f.NewFile || f.Deleted || f.OldPath != "limit/bucket.go" || f.NewPath != "limit/token_bucket.go" {
			t.Errorf("rename not resolved: %+v", f)
		}
		if !strings.HasPrefix(f.Diff, "@@ -1,7 +1,11 @@") {
			t.Errorf("diff text = %q", f.Diff)
		}

		added, err := c.MRDiff(ctx, mrRef(apiProj, 1))
		if err != nil || len(added.Files) != 1 || !added.Files[0].NewFile {
			t.Errorf("api!1 diff = %+v, %v", added, err)
		}
		if _, err := c.MRDiff(ctx, mrRef(apiProj, 99)); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown MR err = %v, want ErrNotFound", err)
		}
		if _, err := c.MRDiff(ctx, issueRef(apiProj, 1)); err == nil {
			t.Error("an issue ref was accepted")
		}
	})
}

func TestJobsLogAndRetry(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		mr, err := c.MR(ctx, mrRef(webProj, 1))
		if err != nil || mr.Pipeline == nil {
			t.Fatalf("web!1 = %+v, %v", mr, err)
		}
		pid := mr.Pipeline.ID
		jobs, err := c.Jobs(ctx, webProj, pid)
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) != 3 || jobs[0].Name != "test" || jobs[1].Name != "lint" || jobs[1].Status != "failed" || jobs[1].Duration != 12 {
			t.Fatalf("jobs = %+v (want newest first)", jobs)
		}
		if jobs[1].WebURL == "" || jobs[1].Stage != "lint" {
			t.Errorf("lint job = %+v", jobs[1])
		}

		log, err := c.JobLog(ctx, webProj, jobs[1].ID, 0)
		if err != nil || !strings.Contains(log, "eslint: 2 errors") || !strings.HasPrefix(log, "eslint") {
			t.Fatalf("log = %q, %v", log, err)
		}
		tail, err := c.JobLog(ctx, webProj, jobs[1].ID, 10)
		if err != nil || tail != log[len(log)-10:] {
			t.Errorf("tail = %q, want %q (%v)", tail, log[len(log)-10:], err)
		}
		if _, err := c.JobLog(ctx, webProj, 424242, 0); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown job err = %v, want ErrNotFound", err)
		}

		if err := c.RetryPipeline(ctx, webProj, pid); err != nil {
			t.Fatal(err)
		}
		if n, _ := srv.Retries(mrRef(webProj, 1)); n != 1 {
			t.Errorf("retries = %d", n)
		}
		after, err := c.Jobs(ctx, webProj, pid)
		if err != nil {
			t.Fatal(err)
		}
		if len(after) != 3 || after[0].Name != "lint" || after[0].Status != "pending" {
			t.Errorf("jobs after retry = %+v (the failed lint is replaced by a pending one)", after)
		}
		mr, _ = c.MR(ctx, mrRef(webProj, 1))
		if mr.Pipeline.ID != pid || mr.Pipeline.Status != "pending" {
			t.Errorf("pipeline after retry = %+v", mr.Pipeline)
		}
		if err := c.RetryPipeline(ctx, webProj, 1); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown pipeline err = %v, want ErrNotFound", err)
		}
	})
}

func TestReplyAndResolve(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 1)
		ds, err := c.Discussions(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		var open forge.Discussion
		for _, d := range ds {
			if d.Resolvable && !d.Resolved {
				open = d
				break
			}
		}
		if open.ID == "" {
			t.Fatal("no open thread on api!1")
		}

		n, err := c.Reply(ctx, ref, open.ID, "Good point; adding the test.")
		if err != nil {
			t.Fatal(err)
		}
		if n.ID == 0 || n.Body != "Good point; adding the test." || n.Author.Username != "sandbox-dev" || n.CreatedAt.IsZero() {
			t.Errorf("reply = %+v", n)
		}
		if err := c.Resolve(ctx, ref, open.ID, true); err != nil {
			t.Fatal(err)
		}
		mr, _ := c.MR(ctx, ref)
		if mr.Unresolved != 1 || mr.Resolved != 2 {
			t.Errorf("after resolve unresolved/resolved = %d/%d, want 1/2", mr.Unresolved, mr.Resolved)
		}
		got, _ := c.Discussions(ctx, ref)
		for _, d := range got {
			if d.ID == open.ID && (len(d.Notes) != len(open.Notes)+1 || !d.Resolved) {
				t.Errorf("thread after reply+resolve = %+v", d)
			}
		}
		if err := c.Resolve(ctx, ref, open.ID, false); err != nil {
			t.Fatal(err)
		}
		if mr, _ = c.MR(ctx, ref); mr.Unresolved != 2 {
			t.Errorf("after unresolve unresolved = %d, want 2", mr.Unresolved)
		}

		if _, err := c.Reply(ctx, ref, "nope", "x"); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown thread err = %v, want ErrNotFound", err)
		}
		if _, err := c.Reply(ctx, ref, open.ID, "  "); err == nil {
			t.Error("empty reply accepted")
		}
		// The system note is an individual discussion: GitLab refuses replies.
		for _, d := range ds {
			if d.Notes[0].System {
				_, err := c.Reply(ctx, ref, d.ID, "x")
				apiStatus(t, err, http.StatusBadRequest)
			}
		}
	})
}

func TestComment(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, _ *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 2)
		d, err := c.MRDiff(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}

		// An added line of the renamed file: new_line only.
		disc, err := c.Comment(ctx, ref, "Why float64?", pos(d, 0, 7))
		if err != nil {
			t.Fatal(err)
		}
		want := &forge.Position{Path: "limit/token_bucket.go", OldPath: "limit/bucket.go", NewLine: 7}
		if disc.ID == "" || len(disc.Notes) != 1 || disc.Position == nil || *disc.Position != *want {
			t.Errorf("thread = %+v (position %+v, want %+v)", disc, disc.Position, want)
		}
		// An unchanged line numbers differently on each side: both are sent.
		disc, err = c.Comment(ctx, ref, "Keep the brace.", pos(d, 7, 8))
		if err != nil || disc.Position == nil || disc.Position.OldLine != 7 || disc.Position.NewLine != 8 {
			t.Errorf("context-line thread = %+v, %v", disc, err)
		}
		// Overview thread.
		disc, err = c.Comment(ctx, ref, "Overall: nice.", nil)
		if err != nil || disc.Position != nil || !disc.Resolvable {
			t.Errorf("overview thread = %+v, %v", disc, err)
		}

		// A removed line (the old comment line 3) is old_line only.
		if rm, err := c.Comment(ctx, ref, "Keep the old wording?", pos(d, 3, 0)); err != nil || rm.Position == nil || rm.Position.OldLine != 3 {
			t.Errorf("removed-line thread = %+v, %v", rm, err)
		}
		// GitLab rejects a line outside every hunk and a number pair that names
		// no line (line 7 is added, so (7,7) is neither added nor unchanged).
		for _, bad := range []*forge.DiffPosition{pos(d, 0, 40), pos(d, 7, 7)} {
			_, err := c.Comment(ctx, ref, "x", bad)
			apiStatus(t, err, http.StatusBadRequest)
		}
		// The driver refuses incomplete positions before sending them.
		for name, bad := range map[string]*forge.DiffPosition{"no line": pos(d, 0, 0), "no SHAs": {NewPath: "a", NewLine: 1}} {
			if _, err := c.Comment(ctx, ref, "x", bad); err == nil {
				t.Errorf("%s: position accepted", name)
			}
		}
		stale := pos(d, 0, 7)
		stale.HeadSHA = strings.Repeat("0", 40)
		_, err = c.Comment(ctx, ref, "x", stale)
		apiStatus(t, err, http.StatusBadRequest)
		if _, err := c.Comment(ctx, ref, " ", nil); err == nil {
			t.Error("empty comment accepted")
		}

		ds, _ := c.Discussions(ctx, ref)
		if len(ds) != 5 { // seeded thread + 3 accepted comments + the removed-line comment
			t.Errorf("api!2 has %d discussions, want 5", len(ds))
		}
	})
}

func TestApproveUnapprove(t *testing.T) {
	forEachTier(t, func(t *testing.T, tier fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 1)
		mr, _ := c.MR(ctx, ref)
		wrong := strings.Repeat("0", 40)
		apiStatus(t, c.Approve(ctx, ref, wrong), http.StatusConflict)

		if err := c.Approve(ctx, ref, mr.HeadSHA); err != nil {
			t.Fatal(err)
		}
		got, _ := srv.Approvers(ref)
		if !containsStr(got, "sandbox-dev") {
			t.Errorf("approvers = %v", got)
		}
		after, _ := c.MR(ctx, ref)
		if !userIn(after.Approvals.ApprovedBy, "sandbox-dev") {
			t.Errorf("approved_by = %+v", after.Approvals.ApprovedBy)
		}
		if err := c.Unapprove(ctx, ref); err != nil {
			t.Fatal(err)
		}
		got, _ = srv.Approvers(ref)
		if containsStr(got, "sandbox-dev") {
			t.Errorf("approvers after unapprove = %v", got)
		}
		// An empty sha approves whatever is current.
		if err := c.Approve(ctx, ref, ""); err != nil {
			t.Fatal(err)
		}
		// A merged MR cannot be approved: GitLab answers 401.
		err := c.Approve(ctx, mrRef(webProj, 2), "")
		if !errors.Is(err, forge.ErrAuth) {
			t.Errorf("merged MR approve err = %v, want ErrAuth", err)
		}
		_ = tier
	})
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func userIn(us []forge.User, name string) bool {
	for _, u := range us {
		if u.Username == name {
			return true
		}
	}
	return false
}

func TestSetDraft(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		web1, api1 := mrRef(webProj, 1), mrRef(apiProj, 1)
		puts := func() int {
			n := 0
			for _, r := range srv.Requests() {
				if r.Method == http.MethodPut && strings.HasSuffix(r.Path, "/merge_requests/1") {
					n++
				}
			}
			return n
		}

		if err := c.SetDraft(ctx, web1, false); err != nil {
			t.Fatal(err)
		}
		mr, _ := c.MR(ctx, web1)
		if mr.Draft || mr.Title != "Paginate the audit log view" {
			t.Errorf("after ready: draft %v title %q", mr.Draft, mr.Title)
		}
		before := puts()
		if err := c.SetDraft(ctx, web1, false); err != nil || puts() != before {
			t.Errorf("ready on a ready MR wrote again: %d -> %d (%v)", before, puts(), err)
		}
		if err := c.SetDraft(ctx, web1, true); err != nil {
			t.Fatal(err)
		}
		if mr, _ = c.MR(ctx, web1); !mr.Draft || mr.Title != "Draft: Paginate the audit log view" {
			t.Errorf("back to draft: draft %v title %q", mr.Draft, mr.Title)
		}

		if err := c.SetDraft(ctx, api1, true); err != nil {
			t.Fatal(err)
		}
		if title, _ := srv.Title(api1); title != "Draft: Rate-limit the export endpoint" {
			t.Errorf("title = %q", title)
		}
		if err := c.SetDraft(ctx, api1, false); err != nil {
			t.Fatal(err)
		}
		if title, _ := srv.Title(api1); title != "Rate-limit the export endpoint" {
			t.Errorf("title = %q", title)
		}
		if err := c.SetDraft(ctx, mrRef(apiProj, 99), true); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unknown MR err = %v", err)
		}
	})
}

func TestMerge(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 1)
		mr, _ := c.MR(ctx, ref)

		if err := c.Merge(ctx, ref, forge.MergeOpts{}); err == nil {
			t.Error("merge without a SHA accepted")
		}
		apiStatus(t, c.Merge(ctx, ref, forge.MergeOpts{SHA: strings.Repeat("0", 40)}), http.StatusConflict)
		// A draft cannot merge (405).
		apiStatus(t, c.Merge(ctx, mrRef(webProj, 1), forge.MergeOpts{SHA: "5d2c7a1e0b9f4c3d8a6e1f2b7c4d9e0a3b5c6d7e"}), http.StatusMethodNotAllowed)
		if merged, _, _, _ := srv.MergeState(ref); merged {
			t.Fatal("rejected merges changed state")
		}

		// Premium requires two approvals; approving makes both tiers mergeable.
		if err := c.Approve(ctx, ref, mr.HeadSHA); err != nil {
			t.Fatal(err)
		}
		opts := forge.MergeOpts{SHA: mr.HeadSHA, Squash: true, RemoveSourceBranch: true, Message: "Rate-limit exports"}
		if err := c.Merge(ctx, ref, opts); err != nil {
			t.Fatal(err)
		}
		merged, auto, params, _ := srv.MergeState(ref)
		if !merged || auto {
			t.Fatalf("merged %v auto %v", merged, auto)
		}
		if params["sha"] != mr.HeadSHA || params["squash"] != true || params["should_remove_source_branch"] != true ||
			params["merge_commit_message"] != "Rate-limit exports" || params["squash_commit_message"] != "Rate-limit exports" {
			t.Errorf("merge parameters = %v", params)
		}
		if after, _ := c.MR(ctx, ref); after.State != "merged" || after.MergedAt == nil {
			t.Errorf("after merge = %+v", after)
		}
		apiStatus(t, c.Merge(ctx, ref, opts), http.StatusMethodNotAllowed)
	})
}

func TestMergeAuto(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 2)
		mr, _ := c.MR(ctx, ref)
		if err := srv.SetPipelineStatus(ref, "running"); err != nil {
			t.Fatal(err)
		}
		if err := c.Merge(ctx, ref, forge.MergeOpts{SHA: mr.HeadSHA, AutoMerge: true}); err != nil {
			t.Fatal(err)
		}
		merged, auto, params, _ := srv.MergeState(ref)
		if merged || !auto || params["auto_merge"] != true {
			t.Errorf("merged %v auto %v params %v: want auto-merge armed, not merged", merged, auto, params)
		}
	})
}

func TestDraftNotesAndBulkPublish(t *testing.T) {
	forEachTier(t, func(t *testing.T, _ fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		ref := mrRef(apiProj, 2)
		d, err := c.MRDiff(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := c.Discussions(ctx, ref)

		a, err := c.AddDraftNote(ctx, ref, "Why a float?", pos(d, 0, 7), "")
		if err != nil {
			t.Fatal(err)
		}
		if a.ID == 0 || a.Body != "Why a float?" || a.Position == nil || a.Position.Path != "limit/token_bucket.go" ||
			a.Position.OldPath != "limit/bucket.go" || a.Position.NewLine != 7 || a.InReplyTo != "" {
			t.Errorf("draft = %+v position %+v", a, a.Position)
		}
		b, err := c.AddDraftNote(ctx, ref, "Overview note.", nil, "")
		if err != nil || b.Position != nil {
			t.Fatalf("overview draft = %+v, %v", b, err)
		}
		reply, err := c.AddDraftNote(ctx, ref, "Lazily, I think.", nil, before[0].ID)
		if err != nil || reply.InReplyTo != before[0].ID {
			t.Fatalf("reply draft = %+v, %v", reply, err)
		}
		gone, err := c.AddDraftNote(ctx, ref, "Scrap this.", pos(d, 0, 9), "")
		if err != nil {
			t.Fatal(err)
		}

		// Drafts are invisible as discussions until published.
		if now, _ := c.Discussions(ctx, ref); len(now) != len(before) {
			t.Errorf("drafts leaked into discussions: %d -> %d", len(before), len(now))
		}
		list, err := c.DraftNotes(ctx, ref)
		if err != nil || len(list) != 4 {
			t.Fatalf("drafts = %+v, %v", list, err)
		}
		if err := c.DeleteDraftNote(ctx, ref, gone.ID); err != nil {
			t.Fatal(err)
		}
		if err := c.DeleteDraftNote(ctx, ref, gone.ID); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("second delete err = %v, want ErrNotFound", err)
		}
		if n, _ := srv.PendingDrafts(ref); n != 3 {
			t.Errorf("pending = %d, want 3", n)
		}

		// Bad drafts are refused up front or by GitLab.
		if _, err := c.AddDraftNote(ctx, ref, "x", pos(d, 0, 99), ""); err == nil {
			t.Error("draft outside the diff accepted")
		}
		if _, err := c.AddDraftNote(ctx, ref, "x", pos(d, 0, 7), before[0].ID); err == nil {
			t.Error("positioned reply accepted")
		}
		if _, err := c.AddDraftNote(ctx, ref, "x", nil, "nope"); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("reply to unknown thread err = %v", err)
		}
		if _, err := c.AddDraftNote(ctx, ref, "", nil, ""); err == nil {
			t.Error("empty draft accepted")
		}

		if err := c.PublishDraftNotes(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if n, _ := srv.Publishes(ref); n != 1 {
			t.Errorf("publishes = %d, want 1", n)
		}
		if n, _ := srv.PendingDrafts(ref); n != 0 {
			t.Errorf("pending after publish = %d", n)
		}
		after, _ := c.Discussions(ctx, ref)
		if len(after) != len(before)+2 {
			t.Fatalf("discussions %d -> %d, want +2 (the reply joins its thread)", len(before), len(after))
		}
		if len(after[0].Notes) != len(before[0].Notes)+1 {
			t.Errorf("reply did not join thread: %d notes", len(after[0].Notes))
		}
		var diffThread *forge.Discussion
		for i := range after {
			if after[i].Position != nil && after[i].Position.NewLine == 7 {
				diffThread = &after[i]
			}
		}
		if diffThread == nil || diffThread.Position.Path != "limit/token_bucket.go" || diffThread.Position.OldPath != "limit/bucket.go" {
			t.Errorf("published diff thread = %+v", diffThread)
		}
		if left, _ := c.DraftNotes(ctx, ref); len(left) != 0 {
			t.Errorf("drafts after publish = %+v", left)
		}
	})
}

func TestWritesNeedAMRRef(t *testing.T) {
	c := gitlab.New("http://127.0.0.1:1", "x")
	ctx := context.Background()
	issue := issueRef(apiProj, 1)
	checks := map[string]error{
		"Reply":     second(c.Reply(ctx, issue, "d", "b")),
		"Resolve":   c.Resolve(ctx, issue, "d", true),
		"Comment":   second(c.Comment(ctx, issue, "b", nil)),
		"Approve":   c.Approve(ctx, issue, ""),
		"Unapprove": c.Unapprove(ctx, issue),
		"SetDraft":  c.SetDraft(ctx, issue, true),
		"Merge":     c.Merge(ctx, issue, forge.MergeOpts{SHA: "x"}),
		"AddDraft":  second(c.AddDraftNote(ctx, issue, "b", nil, "")),
		"Drafts":    second(c.DraftNotes(ctx, issue)),
		"Delete":    c.DeleteDraftNote(ctx, issue, 1),
		"Publish":   c.PublishDraftNotes(ctx, issue),
	}
	for name, err := range checks {
		if err == nil || !strings.Contains(err.Error(), "invalid reference") {
			t.Errorf("%s on an issue ref: err = %v", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }
