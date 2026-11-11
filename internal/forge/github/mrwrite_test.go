package github_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
	"github.com/coredev-uk/ade/internal/forge/github/fake"
)

func apiStatus(t *testing.T, err error, want int) {
	t.Helper()
	var ae *github.APIError
	if !errors.As(err, &ae) || ae.Status != want {
		t.Fatalf("err = %v, want an APIError with status %d", err, want)
	}
}

// renamePos builds a position on the renamed file of api!4 from its diff.
func renamePos(d forge.Diff, oldLine, newLine int) *forge.DiffPosition {
	f := d.Files[0]
	return &forge.DiffPosition{
		BaseSHA: d.BaseSHA, StartSHA: d.StartSHA, HeadSHA: d.HeadSHA,
		OldPath: f.OldPath, NewPath: f.NewPath, OldLine: oldLine, NewLine: newLine,
	}
}

func TestMRDiff(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	d, err := c.MRDiff(ctx, mrRef(apiProj, 4))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.HeadSHA) != 40 || d.BaseSHA == "" || d.StartSHA == "" || len(d.Files) != 1 {
		t.Fatalf("diff = %+v", d)
	}
	f := d.Files[0]
	if !f.Renamed || f.NewFile || f.Deleted || f.OldPath != "limit/bucket.go" || f.NewPath != "limit/token_bucket.go" {
		t.Errorf("rename not resolved: %+v", f)
	}
	if !strings.HasPrefix(f.Diff, "@@ -1,7 +1,11 @@") {
		t.Errorf("diff text = %q: hunk text must start at the @@ header", f.Diff)
	}

	added, err := c.MRDiff(ctx, mrRef(apiProj, 2))
	if err != nil || len(added.Files) != 1 || !added.Files[0].NewFile || added.Files[0].OldPath != added.Files[0].NewPath {
		t.Errorf("api!2 diff = %+v, %v", added, err)
	}
	if _, err := c.MRDiff(ctx, mrRef(apiProj, 99)); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown MR err = %v, want ErrNotFound", err)
	}
	if _, err := c.MRDiff(ctx, issueRef(apiProj, 1)); err == nil {
		t.Error("an issue ref was accepted")
	}
}

func TestJobsLogAndRetry(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	mr, err := c.MR(ctx, mrRef(webProj, 2))
	if err != nil || mr.Pipeline == nil {
		t.Fatalf("web!2 = %+v, %v", mr, err)
	}
	pid := mr.Pipeline.ID
	jobs, err := c.Jobs(ctx, webProj, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 || jobs[0].Name != "test" || jobs[1].Name != "lint" || jobs[1].Status != "failed" || jobs[0].Status != "success" {
		t.Fatalf("jobs = %+v", jobs)
	}
	if jobs[1].WebURL == "" || jobs[1].Stage != "ci" || jobs[1].Duration != 90 {
		t.Errorf("lint job = %+v (stage is the workflow name, duration in seconds)", jobs[1])
	}

	log, err := c.JobLog(ctx, webProj, jobs[1].ID, 0)
	if err != nil || !strings.Contains(log, "eslint: 2 errors") {
		t.Fatalf("log = %q, %v", log, err)
	}
	tail, err := c.JobLog(ctx, webProj, jobs[1].ID, 10)
	if err != nil || tail != log[len(log)-10:] {
		t.Errorf("tail = %q, want %q (%v)", tail, log[len(log)-10:], err)
	}
	if _, err := c.JobLog(ctx, webProj, 424242, 0); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown job err = %v, want ErrNotFound", err)
	}
	if none, err := c.Jobs(ctx, webProj, 0); err != nil || len(none) != 0 {
		t.Errorf("Jobs(0) = %+v, %v: checks that are not Actions have no jobs", none, err)
	}

	if err := c.RetryPipeline(ctx, webProj, pid); err != nil {
		t.Fatal(err)
	}
	if n := srv.Reruns(webProj, pid); n != 1 {
		t.Errorf("reruns = %d", n)
	}
	after, err := c.Jobs(ctx, webProj, pid)
	if err != nil || len(after) != 3 || after[1].Status != "pending" || after[0].Status != "success" {
		t.Errorf("jobs after retry = %+v, %v (only the failed job is re-run)", after, err)
	}
	mr, _ = c.MR(ctx, mrRef(webProj, 2))
	if mr.Pipeline == nil || mr.Pipeline.ID != pid || mr.Pipeline.Status != "pending" {
		t.Errorf("pipeline after retry = %+v", mr.Pipeline)
	}
	if err := c.RetryPipeline(ctx, webProj, pid); err == nil {
		t.Error("a run that is still going was retried")
	}
	if err := c.RetryPipeline(ctx, webProj, 1); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown run err = %v, want ErrNotFound", err)
	}
}

func TestReplyResolveAndComment(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	ref := mrRef(apiProj, 2)

	open := func() forge.Discussion {
		t.Helper()
		ds, err := c.Discussions(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range ds {
			if d.Resolvable && !d.Resolved && strings.Contains(d.Notes[0].Body, "429 path") {
				return d
			}
		}
		t.Fatal("no open thread on api!2")
		return forge.Discussion{}
	}
	thread := open()
	n, err := c.Reply(ctx, ref, thread.ID, "Will do.")
	if err != nil || n.Body != "Will do." || n.ID == 0 || n.Author.Username != "demo-user" {
		t.Fatalf("reply = %+v, %v", n, err)
	}
	if got := open(); len(got.Notes) != 2 || got.Notes[1].Body != "Will do." {
		t.Errorf("thread after reply = %+v", got)
	}

	if err := c.Resolve(ctx, ref, thread.ID, true); err != nil {
		t.Fatal(err)
	}
	mr, _ := c.MR(ctx, ref)
	if mr.Unresolved != 1 || mr.Resolved != 2 {
		t.Errorf("counts after resolve = %d/%d", mr.Unresolved, mr.Resolved)
	}
	if err := c.Resolve(ctx, ref, thread.ID, false); err != nil {
		t.Fatal(err)
	}
	if mr, _ = c.MR(ctx, ref); mr.Unresolved != 2 || mr.Resolved != 1 {
		t.Errorf("counts after reopen = %d/%d", mr.Unresolved, mr.Resolved)
	}

	t.Run("overview comment, and what cannot be threaded", func(t *testing.T) {
		d, err := c.Comment(ctx, ref, "Looks close.", nil)
		if err != nil || d.ID == "" || d.Resolvable || len(d.Notes) != 1 || d.Notes[0].Body != "Looks close." {
			t.Fatalf("overview comment = %+v, %v", d, err)
		}
		if err := c.Resolve(ctx, ref, d.ID, true); !errors.Is(err, forge.ErrUnavailable) {
			t.Errorf("resolving a conversation comment: err = %v, want ErrUnavailable", err)
		}
		re, err := c.Reply(ctx, ref, d.ID, "And a follow-up.")
		if err != nil || re.Body != "And a follow-up." {
			t.Fatalf("reply to a conversation comment = %+v, %v", re, err)
		}
		ds, _ := c.Discussions(ctx, ref)
		n := 0
		for _, x := range ds {
			if !x.Resolvable && len(x.Notes) == 1 && (x.Notes[0].Body == "Looks close." || x.Notes[0].Body == "And a follow-up.") {
				n++
			}
		}
		if n != 2 {
			t.Errorf("found %d of the two conversation comments", n)
		}
	})

	t.Run("line comment starts a thread", func(t *testing.T) {
		diff, err := c.MRDiff(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		pos := &forge.DiffPosition{HeadSHA: diff.HeadSHA, OldPath: "limit/bucket.go", NewPath: "limit/bucket.go", NewLine: 4}
		d, err := c.Comment(ctx, ref, "On a line.", pos)
		if err != nil || d.ID == "" || !d.Resolvable || d.Position == nil || d.Position.NewLine != 4 || d.Position.Path != "limit/bucket.go" {
			t.Fatalf("line comment = %+v, %v", d, err)
		}
		if err := c.Resolve(ctx, ref, d.ID, true); err != nil {
			t.Errorf("the new thread cannot be resolved: %v", err)
		}
		pos.NewLine = 500
		if _, err := c.Comment(ctx, ref, "Off the diff.", pos); err == nil {
			t.Error("a comment outside the diff was accepted")
		}
	})

	t.Run("input checks", func(t *testing.T) {
		if _, err := c.Reply(ctx, ref, "", "x"); err == nil {
			t.Error("reply without a discussion accepted")
		}
		if _, err := c.Reply(ctx, ref, thread.ID, "  "); err == nil {
			t.Error("empty reply accepted")
		}
		if _, err := c.Comment(ctx, ref, "", nil); err == nil {
			t.Error("empty comment accepted")
		}
		if _, err := c.Comment(ctx, ref, "x", &forge.DiffPosition{NewPath: "a"}); err == nil {
			t.Error("a position without a line accepted")
		}
		if _, err := c.Comment(ctx, ref, "x", &forge.DiffPosition{NewLine: 3}); err == nil {
			t.Error("a position without a path accepted")
		}
		if err := c.Resolve(ctx, ref, "", true); err == nil {
			t.Error("resolve without an id accepted")
		}
		if err := c.Resolve(ctx, ref, "PRRT_nope", true); err == nil {
			t.Error("resolving an unknown thread succeeded")
		}
	})
}

func TestApproveUnapprove(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	teammates := mrRef(apiProj, 5)
	head, _ := c.MR(ctx, teammates)

	if err := c.Unapprove(ctx, teammates); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unapprove without an approval: err = %v, want ErrNotFound", err)
	}
	if err := c.Approve(ctx, teammates, head.HeadSHA); err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.Approvers(teammates); !slices.Equal(got, []string{"demo-user"}) {
		t.Errorf("approvers = %v", got)
	}
	mr, _ := c.MR(ctx, teammates)
	if len(mr.Approvals.ApprovedBy) != 1 || mr.Approvals.ApprovedBy[0].Username != "demo-user" {
		t.Errorf("approvals = %+v", mr.Approvals)
	}
	if err := c.Unapprove(ctx, teammates); err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.Approvers(teammates); len(got) != 0 {
		t.Errorf("approvers after unapprove = %v", got)
	}
	if mr, _ = c.MR(ctx, teammates); len(mr.Approvals.ApprovedBy) != 0 {
		t.Errorf("approvals after unapprove = %+v", mr.Approvals)
	}

	// GitHub refuses approval of one's own pull request.
	apiStatus(t, c.Approve(ctx, mrRef(apiProj, 2), ""), http.StatusUnprocessableEntity)
	if err := c.Approve(ctx, mrRef(apiProj, 99), ""); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown MR: err = %v", err)
	}
}

func TestSetDraft(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	web2 := mrRef(webProj, 2)
	steps := []struct {
		draft bool
	}{{false}, {false}, {true}, {true}}
	for i, s := range steps {
		if err := c.SetDraft(ctx, web2, s.draft); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got, _ := srv.IsDraft(web2); got != s.draft {
			t.Errorf("step %d: draft = %v, want %v", i, got, s.draft)
		}
		if mr, _ := c.MR(ctx, web2); mr.Draft != s.draft {
			t.Errorf("step %d: MR().Draft = %v", i, mr.Draft)
		}
	}
	// Only the two real changes reached GraphQL: the repeats were no-ops.
	gql := 0
	for _, r := range srv.Requests() {
		if strings.HasSuffix(r.Path, "/graphql") && r.Method == http.MethodPost {
			gql++
		}
	}
	// Each MR read above runs one threads query too.
	if want := 2 + len(steps); gql != want {
		t.Errorf("%d GraphQL requests, want %d (2 toggles + %d MR reads)", gql, want, len(steps))
	}
	if err := c.SetDraft(ctx, mrRef(webProj, 99), true); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown MR: err = %v", err)
	}
}

func TestMerge(t *testing.T) {
	ctx := context.Background()
	t.Run("sha is pinned and mandatory", func(t *testing.T) {
		c, srv := newClient(t)
		ref := mrRef(apiProj, 5)
		if err := c.Merge(ctx, ref, forge.MergeOpts{}); err == nil || !strings.Contains(err.Error(), "SHA") {
			t.Errorf("merge without a sha: %v", err)
		}
		apiStatus(t, c.Merge(ctx, ref, forge.MergeOpts{SHA: strings.Repeat("0", 40)}), http.StatusConflict)
		if merged, _, _, _ := srv.MergeState(ref); merged {
			t.Error("a stale-head merge went through")
		}
	})
	t.Run("drafts are refused", func(t *testing.T) {
		c, _ := newClient(t)
		ref := mrRef(webProj, 2)
		mr, _ := c.MR(ctx, ref)
		apiStatus(t, c.Merge(ctx, ref, forge.MergeOpts{SHA: mr.HeadSHA}), http.StatusMethodNotAllowed)
	})
	t.Run("merge, squash and branch removal", func(t *testing.T) {
		c, srv := newClient(t)
		ref := mrRef(apiProj, 5)
		mr, _ := c.MR(ctx, ref)
		err := c.Merge(ctx, ref, forge.MergeOpts{SHA: mr.HeadSHA, Squash: true, RemoveSourceBranch: true, Message: "Add retry budget\n\nBody text."})
		if err != nil {
			t.Fatal(err)
		}
		merged, auto, params, _ := srv.MergeState(ref)
		if !merged || auto {
			t.Errorf("merged = %v auto = %v", merged, auto)
		}
		if params["merge_method"] != "squash" || params["sha"] != mr.HeadSHA || params["commit_title"] != "Add retry budget" || params["commit_message"] != "Body text." {
			t.Errorf("merge params = %v", params)
		}
		if slices.Contains(srv.Branches(apiProj), "feature/retry-budget") {
			t.Error("the source branch was not removed")
		}
		after, _ := c.MR(ctx, ref)
		if after.State != "merged" || after.MergedAt == nil {
			t.Errorf("after = %+v", after)
		}
	})
	t.Run("plain merge keeps the branch", func(t *testing.T) {
		c, srv := newClient(t)
		ref := mrRef(apiProj, 5)
		mr, _ := c.MR(ctx, ref)
		if err := c.Merge(ctx, ref, forge.MergeOpts{SHA: mr.HeadSHA}); err != nil {
			t.Fatal(err)
		}
		_, _, params, _ := srv.MergeState(ref)
		if params["merge_method"] != "merge" {
			t.Errorf("merge params = %v", params)
		}
		if !slices.Contains(srv.Branches(apiProj), "feature/retry-budget") {
			t.Error("the branch was removed without being asked")
		}
		apiStatus(t, c.Merge(ctx, ref, forge.MergeOpts{SHA: mr.HeadSHA}), http.StatusMethodNotAllowed) // already merged
	})
	t.Run("auto-merge goes through GraphQL", func(t *testing.T) {
		c, srv := newClient(t)
		ref := mrRef(apiProj, 5)
		mr, _ := c.MR(ctx, ref)
		if err := c.Merge(ctx, ref, forge.MergeOpts{SHA: mr.HeadSHA, AutoMerge: true, Squash: true}); err != nil {
			t.Fatal(err)
		}
		merged, auto, _, _ := srv.MergeState(ref)
		if merged || !auto {
			t.Errorf("merged = %v auto = %v, want auto-merge enabled and nothing merged yet", merged, auto)
		}
	})
}

// The review batch: drafts live in one pending review, nothing is visible
// before the publish, and one publish is one review event.
func TestDraftNotesAreOnePendingReview(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	rn := mrRef(apiProj, 4)
	diff, err := c.MRDiff(ctx, rn)
	if err != nil {
		t.Fatal(err)
	}
	discs0, _ := c.Discussions(ctx, rn)

	want := []struct {
		body           string
		old, new       int
		wantOld, wantN int
	}{
		{"unchanged line, new side", 2, 2, 0, 2},
		{"added line", 0, 4, 0, 4},
		{"another added line", 0, 5, 0, 5},
		{"added function", 0, 11, 0, 11},
		{"removed line, old side", 3, 0, 3, 0},
	}
	var ids []int64
	for _, w := range want {
		n, err := c.AddDraftNote(ctx, rn, w.body, renamePos(diff, w.old, w.new), "")
		if err != nil || n.ID <= 0 || n.Body != w.body || n.Position == nil || n.Position.Path != "limit/token_bucket.go" {
			t.Fatalf("AddDraftNote(%q) = %+v, %v", w.body, n, err)
		}
		ids = append(ids, n.ID)
	}
	if n, err := c.AddDraftNote(ctx, rn, "Overall: nice.", nil, ""); err != nil || n.ID >= 0 {
		t.Fatalf("overview = %+v, %v; want a negative id", n, err)
	}
	if n, err := c.AddDraftNote(ctx, rn, "One more thing.", nil, ""); err != nil || n.Body != "Overall: nice.\n\nOne more thing." {
		t.Fatalf("second overview = %+v, %v; overviews are joined", n, err)
	}

	if got, _ := srv.PendingReviewComments(rn); got != 5 {
		t.Errorf("pending comments on the forge = %d, want 5", got)
	}
	if got, _ := srv.Publishes(rn); got != 0 {
		t.Errorf("publishes before publish = %d", got)
	}
	drafts, err := c.DraftNotes(ctx, rn)
	if err != nil || len(drafts) != 6 {
		t.Fatalf("DraftNotes = %+v, %v; want the overview and five comments", drafts, err)
	}
	if drafts[0].Position != nil || drafts[0].ID >= 0 || !strings.HasPrefix(drafts[0].Body, "Overall") {
		t.Errorf("first draft = %+v, want the overview", drafts[0])
	}
	for i, w := range want {
		d := drafts[i+1]
		if d.ID != ids[i] || d.Body != w.body || d.Position == nil || d.Position.NewLine != w.wantN || d.Position.OldLine != w.wantOld || d.Position.Path != "limit/token_bucket.go" || d.InReplyTo != "" {
			t.Errorf("draft %d = %+v (position %+v), want %q at new %d old %d", i, d, d.Position, w.body, w.wantN, w.wantOld)
		}
	}
	if ds, _ := c.Discussions(ctx, rn); len(ds) != len(discs0) {
		t.Errorf("drafts leaked into Discussions: %d, was %d", len(ds), len(discs0))
	}

	t.Run("a draft can be taken back", func(t *testing.T) {
		if err := c.DeleteDraftNote(ctx, rn, ids[4]); err != nil {
			t.Fatal(err)
		}
		if got, _ := srv.PendingReviewComments(rn); got != 4 {
			t.Errorf("pending comments = %d, want 4", got)
		}
		if err := c.DeleteDraftNote(ctx, rn, ids[4]); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("deleting twice: err = %v, want ErrNotFound", err)
		}
		if _, err := c.AddDraftNote(ctx, rn, "removed line, old side", renamePos(diff, 3, 0), ""); err != nil {
			t.Fatal(err)
		}
	})

	if err := c.PublishDraftNotes(ctx, rn); err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.Publishes(rn); got != 1 {
		t.Errorf("publishes = %d, want exactly one review for six drafts", got)
	}
	if left, err := c.DraftNotes(ctx, rn); err != nil || len(left) != 0 {
		t.Errorf("DraftNotes after publish = %+v, %v", left, err)
	}
	ds, err := c.Discussions(ctx, rn)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != len(discs0)+6 { // five threads and the review's overview
		t.Errorf("%d discussions after publish, want %d", len(ds), len(discs0)+6)
	}
	var lines []int
	for _, d := range ds {
		if d.Position != nil && d.Position.Path == "limit/token_bucket.go" {
			lines = append(lines, d.Position.NewLine)
		}
	}
	slices.Sort(lines)
	if !slices.Equal(lines, []int{0, 2, 4, 5, 11}) {
		t.Errorf("thread lines on the renamed file = %v (0 is the removed line)", lines)
	}
	if err := c.PublishDraftNotes(ctx, rn); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("publishing nothing: err = %v, want ErrNotFound", err)
	}
}

func TestDraftReplyAndOverviewOnly(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	ref := mrRef(apiProj, 2)
	ds, _ := c.Discussions(ctx, ref)
	var thread forge.Discussion
	for _, d := range ds {
		if d.Resolvable && !d.Resolved {
			thread = d
		}
	}

	n, err := c.AddDraftNote(ctx, ref, "Agreed.", nil, thread.ID)
	if err != nil || n.InReplyTo != thread.ID || n.ID <= 0 {
		t.Fatalf("reply draft = %+v, %v", n, err)
	}
	got, err := c.DraftNotes(ctx, ref)
	if err != nil || len(got) != 1 || got[0].InReplyTo != thread.ID || got[0].Position != nil || got[0].ID != n.ID {
		t.Fatalf("DraftNotes = %+v, %v", got, err)
	}
	if after, _ := c.Discussions(ctx, ref); len(after) != len(ds) {
		t.Errorf("a pending reply is visible before publish")
	} else {
		for _, d := range after {
			if d.ID == thread.ID && len(d.Notes) != len(thread.Notes) {
				t.Errorf("pending reply shown in its thread: %+v", d.Notes)
			}
		}
	}
	if err := c.PublishDraftNotes(ctx, ref); err != nil {
		t.Fatal(err)
	}
	after, _ := c.Discussions(ctx, ref)
	for _, d := range after {
		if d.ID == thread.ID && (len(d.Notes) != len(thread.Notes)+1 || d.Notes[len(d.Notes)-1].Body != "Agreed.") {
			t.Errorf("thread after publish = %+v", d.Notes)
		}
	}

	// An overview alone needs no pending review: it publishes as a review body.
	web := mrRef(apiProj, 5)
	if _, err := c.AddDraftNote(ctx, web, "Summary only.", nil, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.DraftNotes(ctx, web); len(got) != 1 || got[0].Body != "Summary only." {
		t.Errorf("overview draft = %+v", got)
	}
	if err := c.PublishDraftNotes(ctx, web); err != nil {
		t.Fatal(err)
	}
	if n, _ := srv.Publishes(web); n != 1 {
		t.Errorf("publishes = %d", n)
	}
	if got, _ := c.DraftNotes(ctx, web); len(got) != 0 {
		t.Errorf("overview survived its publish: %+v", got)
	}
	if err := c.PublishDraftNotes(ctx, web); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("second publish: err = %v, want ErrNotFound", err)
	}
	if _, err := c.AddDraftNote(ctx, web, "taken back", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteDraftNote(ctx, web, -1); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.DraftNotes(ctx, web); len(got) != 0 {
		t.Errorf("deleted overview still listed: %+v", got)
	}
}

func TestDraftNoteInputChecks(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	rn := mrRef(apiProj, 4)
	diff, _ := c.MRDiff(ctx, rn)
	pos := renamePos(diff, 0, 4)
	tests := []struct {
		name      string
		body      string
		pos       *forge.DiffPosition
		inReplyTo string
	}{
		{"empty body", "  ", pos, ""},
		{"position and reply", "x", pos, "PRRT_x"},
		{"reply to a conversation comment", "x", nil, "issuecomment:5"},
		{"no line", "x", &forge.DiffPosition{NewPath: "a", HeadSHA: diff.HeadSHA}, ""},
		{"line outside the diff", "x", renamePos(diff, 0, 400), ""},
		{"unknown file", "x", &forge.DiffPosition{NewPath: "nope.go", NewLine: 1, HeadSHA: diff.HeadSHA}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if n, err := c.AddDraftNote(ctx, rn, tc.body, tc.pos, tc.inReplyTo); err == nil {
				t.Errorf("accepted: %+v", n)
			}
		})
	}
	if got, _ := srv.PendingReviewComments(rn); got != 0 {
		t.Errorf("%d comments reached the forge from refused drafts", got)
	}
	if _, err := c.AddDraftNote(ctx, mrRef(apiProj, 99), "x", pos, ""); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown MR: err = %v", err)
	}
}

// Draft state is per process only for the overview; the comments are on the
// forge, so a new client finds them (resuming after a crash).
func TestDraftsSurviveANewClient(t *testing.T) {
	c1, srv := newClient(t)
	ctx := context.Background()
	rn := mrRef(apiProj, 4)
	diff, _ := c1.MRDiff(ctx, rn)
	if _, err := c1.AddDraftNote(ctx, rn, "kept", renamePos(diff, 0, 4), ""); err != nil {
		t.Fatal(err)
	}
	c2 := github.New(srv.URL, fake.Token)
	got, err := c2.DraftNotes(ctx, rn)
	if err != nil || len(got) != 1 || got[0].Body != "kept" {
		t.Fatalf("DraftNotes from a second client = %+v, %v", got, err)
	}
	if err := c2.PublishDraftNotes(ctx, rn); err != nil {
		t.Fatal(err)
	}
	if n, _ := srv.Publishes(rn); n != 1 {
		t.Errorf("publishes = %d", n)
	}
}
