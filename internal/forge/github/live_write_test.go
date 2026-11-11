//go:build live

package github_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
)

// raw talks to api.github.com without the driver: the live tests assert what
// the driver did through an independent path, and use it to clean up.
func (s *sandbox) raw(t *testing.T, method, path string, in, out any) int {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(s.ctx, method, "https://api.github.com"+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+liveToken(t))
	req.Header.Set("Accept", "application/vnd.github+json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return resp.StatusCode
}

type rawReview struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
	Body  string `json:"body"`
	User  struct {
		Login string `json:"login"`
	} `json:"user"`
}

type rawComment struct {
	ID       int64  `json:"id"`
	Path     string `json:"path"`
	Position int    `json:"position"`
	Body     string `json:"body"`
}

func (s *sandbox) reviews(t *testing.T, mr forge.Ref) []rawReview {
	t.Helper()
	var rs []rawReview
	if st := s.raw(t, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", mr.Project, mr.IID), nil, &rs); st != 200 {
		t.Fatalf("listing reviews: HTTP %d", st)
	}
	return rs
}

func (s *sandbox) reviewComments(t *testing.T, mr forge.Ref, review int64) []rawComment {
	t.Helper()
	var cs []rawComment
	if st := s.raw(t, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/reviews/%d/comments?per_page=100", mr.Project, mr.IID, review), nil, &cs); st != 200 {
		t.Fatalf("listing review comments: HTTP %d", st)
	}
	return cs
}

// A pending review of five line comments and an overview is one review,
// submitted once, with each comment on the right line of the renamed file.
func TestLiveReviewBatch(t *testing.T) {
	s := liveSandbox(t)
	ctx, c := s.ctx, s.c
	rn := s.mr(t, s.api, "Rename bucket to token bucket")
	tag := fmt.Sprintf("[live-%d]", time.Now().Unix())

	// Start clean: an earlier failed run may have left a pending review.
	if left, err := c.DraftNotes(ctx, rn.Ref); err != nil {
		t.Fatal(err)
	} else {
		for _, d := range left {
			if err := c.DeleteDraftNote(ctx, rn.Ref, d.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	threads0, err := c.Discussions(ctx, rn.Ref)
	if err != nil {
		t.Fatal(err)
	}
	reviews0 := s.reviews(t, rn.Ref)

	diff, err := c.MRDiff(ctx, rn.Ref)
	if err != nil || len(diff.Files) != 1 {
		t.Fatalf("diff = %+v, %v", diff, err)
	}
	f := diff.Files[0]
	at := func(oldLine, newLine int) *forge.DiffPosition {
		return &forge.DiffPosition{
			BaseSHA: diff.BaseSHA, StartSHA: diff.StartSHA, HeadSHA: diff.HeadSHA,
			OldPath: f.OldPath, NewPath: f.NewPath, OldLine: oldLine, NewLine: newLine,
		}
	}
	// The renamed file's hunk is "@@ -4,4 +4,6 @@": lines 4-6 and 9 of the new
	// file are context, 7 and 8 are added; old line 6 is the context line
	// that is new line 6, commented on from the old side.
	want := []struct {
		body             string
		pos              *forge.DiffPosition
		path, side       string
		line, oldL, newL int
	}{
		{tag + " context line, new side", at(5, 5), f.NewPath, "RIGHT", 5, 0, 5},
		{tag + " added blank line", at(0, 7), f.NewPath, "RIGHT", 7, 0, 7},
		{tag + " added field", at(0, 8), f.NewPath, "RIGHT", 8, 0, 8},
		{tag + " closing brace", at(7, 9), f.NewPath, "RIGHT", 9, 0, 9},
		{tag + " old side of Burst", at(6, 0), f.NewPath, "LEFT", 6, 6, 0},
	}
	for _, w := range want {
		n, err := c.AddDraftNote(ctx, rn.Ref, w.body, w.pos, "")
		if err != nil || n.ID <= 0 || n.Body != w.body {
			t.Fatalf("AddDraftNote(%q) = %+v, %v", w.body, n, err)
		}
	}
	if n, err := c.AddDraftNote(ctx, rn.Ref, tag+" overview", nil, ""); err != nil || n.ID >= 0 {
		t.Fatalf("overview draft = %+v, %v (want a negative id: the review body)", n, err)
	}

	drafts, err := c.DraftNotes(ctx, rn.Ref)
	if err != nil || len(drafts) != 6 {
		t.Fatalf("DraftNotes = %d, %v; want 5 comments and the overview", len(drafts), err)
	}
	if drafts[0].Position != nil || !strings.Contains(drafts[0].Body, "overview") {
		t.Errorf("first draft = %+v, want the overview", drafts[0])
	}
	for i, w := range want {
		d := drafts[i+1]
		if d.Body != w.body || d.Position == nil || d.Position.Path != w.path || d.Position.NewLine != w.newL || d.Position.OldLine != w.oldL {
			t.Errorf("draft %d = %+v (position %+v), want %q at new %d old %d", i, d, d.Position, w.body, w.newL, w.oldL)
		}
	}
	if ds, err := c.Discussions(ctx, rn.Ref); err != nil || len(ds) != len(threads0) {
		t.Fatalf("drafts leaked into Discussions: %d discussions, was %d (%v)", len(ds), len(threads0), err)
	}

	if err := c.PublishDraftNotes(ctx, rn.Ref); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { // remove what this run published: the comments, then nothing is left to resolve
		for _, r := range s.reviews(t, rn.Ref) {
			if strings.Contains(r.Body, tag) {
				for _, cm := range s.reviewComments(t, rn.Ref, r.ID) {
					s.raw(t, http.MethodDelete, fmt.Sprintf("/repos/%s/pulls/comments/%d", rn.Ref.Project, cm.ID), nil, nil)
				}
			}
		}
	})

	reviews1 := s.reviews(t, rn.Ref)
	if len(reviews1) != len(reviews0)+1 {
		t.Fatalf("%d reviews after publishing, want %d: one review for six drafts", len(reviews1), len(reviews0)+1)
	}
	last := reviews1[len(reviews1)-1]
	if last.State != "COMMENTED" || !strings.Contains(last.Body, tag+" overview") {
		t.Errorf("review = %+v, want a COMMENTED review with the overview as its body", last)
	}
	cms := s.reviewComments(t, rn.Ref, last.ID)
	if len(cms) != 5 {
		t.Fatalf("the review holds %d comments, want 5", len(cms))
	}
	for i, w := range want {
		cm := cms[i]
		// REST reports pending-origin comments by their offset in the file's hunk
		// (line and side are null there): new 5, 7, 8, 9 and old 6 are hunk lines 2, 4, 5, 6, 3.
		if cm.Path != w.path || cm.Position != []int{2, 4, 5, 6, 3}[i] || cm.Body != w.body {
			t.Errorf("comment %d = %+v, want %s at hunk line %d %q", i, cm, w.path, []int{2, 4, 5, 6, 3}[i], w.body)
		}
	}

	if left, err := c.DraftNotes(ctx, rn.Ref); err != nil || len(left) != 0 {
		t.Errorf("DraftNotes after publish = %+v, %v", left, err)
	}
	if err := c.PublishDraftNotes(ctx, rn.Ref); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("publishing nothing: err = %v, want ErrNotFound", err)
	}
	ds, err := c.Discussions(ctx, rn.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != len(threads0)+6 { // five threads and the review summary
		t.Errorf("%d discussions after publishing, want %d", len(ds), len(threads0)+6)
	}
	mine := 0
	for _, d := range ds {
		if len(d.Notes) == 0 || !strings.Contains(d.Notes[0].Body, tag) || d.Position == nil {
			continue
		}
		mine++
		if d.Position.Path != "limit/token_bucket.go" || !d.Resolvable || d.Resolved {
			t.Errorf("thread = %+v", d)
		}
	}
	if mine != 5 {
		t.Errorf("%d positioned threads of this run, want 5", mine)
	}

	t.Run("a reply drafted inside a review", func(t *testing.T) {
		var thread forge.Discussion
		for _, d := range ds {
			if d.Position != nil && len(d.Notes) > 0 && strings.Contains(d.Notes[0].Body, "added field") {
				thread = d
			}
		}
		if thread.ID == "" {
			t.Fatal("thread of this run not found")
		}
		n, err := c.AddDraftNote(ctx, rn.Ref, tag+" draft reply", nil, thread.ID)
		if err != nil || n.InReplyTo != thread.ID || n.ID <= 0 {
			t.Fatalf("reply draft = %+v, %v", n, err)
		}
		got, err := c.DraftNotes(ctx, rn.Ref)
		if err != nil || len(got) != 1 || got[0].InReplyTo != thread.ID || got[0].Position != nil || got[0].ID != n.ID {
			t.Fatalf("DraftNotes = %+v, %v", got, err)
		}
		if err := c.DeleteDraftNote(ctx, rn.Ref, n.ID); err != nil {
			t.Fatal(err)
		}
		if got, err := c.DraftNotes(ctx, rn.Ref); err != nil || len(got) != 0 {
			t.Errorf("DraftNotes after delete = %+v, %v", got, err)
		}
		// The emptied pending review would block nothing, but leave none behind.
		for _, r := range s.reviews(t, rn.Ref) {
			if r.State == "PENDING" {
				s.raw(t, http.MethodDelete, fmt.Sprintf("/repos/%s/pulls/%d/reviews/%d", rn.Ref.Project, rn.Ref.IID, r.ID), nil, nil)
			}
		}
	})
}

// Replies, resolution and immediate comments round-trip on the seeded PR with
// its review threads, and leave it as found.
func TestLiveThreadWrites(t *testing.T) {
	s := liveSandbox(t)
	ctx, c := s.ctx, s.c
	ready := s.mr(t, s.api, s.titleRate)

	ds, err := c.Discussions(ctx, ready.Ref)
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
		t.Fatal("the seeded open thread on the rate-limit PR is missing")
	}
	state := func(id string) forge.Discussion {
		t.Helper()
		ds, err := c.Discussions(ctx, ready.Ref)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range ds {
			if d.ID == id {
				return d
			}
		}
		t.Fatalf("thread %s vanished", id)
		return forge.Discussion{}
	}

	body := "ade live test reply " + time.Now().UTC().Format(time.RFC3339)
	n, err := c.Reply(ctx, ready.Ref, thread.ID, body)
	if err != nil || n.Body != body || n.ID == 0 || n.Author.Username == "" {
		t.Fatalf("reply = %+v, %v", n, err)
	}
	t.Cleanup(func() {
		s.raw(t, http.MethodDelete, fmt.Sprintf("/repos/%s/pulls/comments/%d", ready.Ref.Project, n.ID), nil, nil)
	})
	if got := state(thread.ID); len(got.Notes) != len(thread.Notes)+1 || got.Notes[len(got.Notes)-1].Body != body {
		t.Errorf("thread after reply = %+v", got)
	}
	if err := c.Resolve(ctx, ready.Ref, thread.ID, true); err != nil || !state(thread.ID).Resolved {
		t.Fatalf("resolve: %v, resolved=%v", err, state(thread.ID).Resolved)
	}
	if mr, err := c.MR(ctx, ready.Ref); err != nil || mr.Unresolved != 1 || mr.Resolved != 2 {
		t.Errorf("counts after resolve = %d/%d, %v", mr.Unresolved, mr.Resolved, err)
	}
	if err := c.Resolve(ctx, ready.Ref, thread.ID, false); err != nil || state(thread.ID).Resolved {
		t.Fatalf("unresolve: %v, resolved=%v", err, state(thread.ID).Resolved)
	}

	t.Run("immediate comments", func(t *testing.T) {
		diff, err := c.MRDiff(ctx, ready.Ref)
		if err != nil || len(diff.Files) == 0 {
			t.Fatal(diff, err)
		}
		pos := &forge.DiffPosition{
			BaseSHA: diff.BaseSHA, StartSHA: diff.StartSHA, HeadSHA: diff.HeadSHA,
			OldPath: diff.Files[0].OldPath, NewPath: diff.Files[0].NewPath, NewLine: 2,
		}
		d, err := c.Comment(ctx, ready.Ref, "ade live test line comment", pos)
		if err != nil || d.ID == "" || !d.Resolvable || d.Position == nil || d.Position.NewLine != 2 || len(d.Notes) != 1 {
			t.Fatalf("line comment = %+v, %v", d, err)
		}
		s.raw(t, http.MethodDelete, fmt.Sprintf("/repos/%s/pulls/comments/%d", ready.Ref.Project, d.Notes[0].ID), nil, nil)

		o, err := c.Comment(ctx, ready.Ref, "ade live test overview comment", nil)
		if err != nil || o.ID == "" || o.Resolvable || len(o.Notes) != 1 {
			t.Fatalf("overview comment = %+v, %v", o, err)
		}
		t.Cleanup(func() {
			s.raw(t, http.MethodDelete, fmt.Sprintf("/repos/%s/issues/comments/%d", ready.Ref.Project, o.Notes[0].ID), nil, nil)
		})
		if err := c.Resolve(ctx, ready.Ref, o.ID, true); !errors.Is(err, forge.ErrUnavailable) {
			t.Errorf("resolving a conversation comment: err = %v, want ErrUnavailable", err)
		}
		re, err := c.Reply(ctx, ready.Ref, o.ID, "ade live test follow-up")
		if err != nil || re.ID == 0 {
			t.Fatalf("reply to a conversation comment = %+v, %v", re, err)
		}
		s.raw(t, http.MethodDelete, fmt.Sprintf("/repos/%s/issues/comments/%d", ready.Ref.Project, re.ID), nil, nil)
	})

	if mr, err := c.MR(ctx, ready.Ref); err != nil || mr.Unresolved != 2 || mr.Resolved != 1 {
		t.Errorf("the seeded counts were not restored: %d unresolved, %d resolved, %v", mr.Unresolved, mr.Resolved, err)
	}
}

func TestLiveDraftToggleAndRefusals(t *testing.T) {
	s := liveSandbox(t)
	ctx, c := s.ctx, s.c
	draft := s.mr(t, s.web, s.titleAu)
	if !draft.Draft {
		t.Fatal("the seeded web PR is not a draft")
	}
	t.Cleanup(func() { _ = c.SetDraft(s.ctx, draft.Ref, true) })

	if err := c.SetDraft(ctx, draft.Ref, false); err != nil {
		t.Fatal(err)
	}
	if mr, err := c.MR(ctx, draft.Ref); err != nil || mr.Draft {
		t.Fatalf("after ready: draft=%v, %v", mr.Draft, err)
	}
	if err := c.SetDraft(ctx, draft.Ref, false); err != nil {
		t.Errorf("marking a ready PR ready again must be a no-op: %v", err)
	}
	if err := c.SetDraft(ctx, draft.Ref, true); err != nil {
		t.Fatal(err)
	}
	if mr, err := c.MR(ctx, draft.Ref); err != nil || !mr.Draft {
		t.Fatalf("after draft: draft=%v, %v", mr.Draft, err)
	}

	t.Run("refusals change nothing", func(t *testing.T) {
		if err := c.Merge(ctx, draft.Ref, forge.MergeOpts{}); err == nil || !strings.Contains(err.Error(), "SHA") {
			t.Errorf("merge without a sha: %v", err)
		}
		if err := c.Merge(ctx, draft.Ref, forge.MergeOpts{SHA: strings.Repeat("0", 40)}); err == nil {
			t.Error("merging a draft at an unknown head succeeded")
		}
		ready := s.mr(t, s.api, s.titleRate)
		var ae *github.APIError
		if err := c.Merge(ctx, ready.Ref, forge.MergeOpts{SHA: strings.Repeat("0", 40)}); !errors.As(err, &ae) || ae.Status != http.StatusConflict {
			t.Errorf("merging at a stale head: err = %v, want HTTP 409", err)
		}
		if mr, err := c.MR(ctx, ready.Ref); err != nil || mr.State != "opened" {
			t.Errorf("rate-limit PR after refused merge = %+v, %v", mr, err)
		}
		// GitHub refuses approval of one's own pull request.
		if err := c.Approve(ctx, ready.Ref, ready.HeadSHA); err == nil {
			t.Error("approved our own PR")
		}
		if err := c.Unapprove(ctx, ready.Ref); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("unapprove without an approval: err = %v, want ErrNotFound", err)
		}
	})
}

// CreateIssue, labels, sub-issue link, branch and draft PR on a scratch set of
// objects that the test removes again.
func TestLiveIssueWrites(t *testing.T) {
	s := liveSandbox(t)
	ctx, c := s.ctx, s.c
	me, err := c.Me(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tag := fmt.Sprintf("ade live %d", time.Now().Unix())
	parent := s.issue(t, s.tracker, s.titleRate)
	ms, err := c.Milestones(ctx, s.tracker) // teaches the client the tracker's sprints
	if err != nil {
		t.Fatal(err)
	}
	var sprint forge.Milestone
	for _, m := range ms {
		if m.Title == "Sprint 42" {
			sprint = m
		}
	}

	child, err := c.CreateIssue(ctx, s.web, forge.IssueDraft{
		Title: tag + " child", Description: "Parent: " + parent.Ref.String() + "\n\nScratch issue of a live test.",
		Labels: []string{"type:story"}, AssigneeIDs: []int64{me.ID}, MilestoneID: sprint.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.raw(t, http.MethodDelete, fmt.Sprintf("/repos/%s/issues/%d/sub_issue", parent.Ref.Project, parent.Ref.IID), map[string]int64{"sub_issue_id": child.ID}, nil)
		s.raw(t, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", child.Ref.Project, child.Ref.IID), map[string]string{"state": "closed"}, nil)
		var node struct {
			NodeID string `json:"node_id"`
		}
		s.raw(t, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d", child.Ref.Project, child.Ref.IID), nil, &node)
		s.raw(t, http.MethodPost, "/graphql", map[string]any{
			"query": "mutation($id: ID!) { deleteIssue(input: {issueId: $id}) { clientMutationId } }", "variables": map[string]any{"id": node.NodeID},
		}, nil)
	})
	if child.Ref.Project != s.web || child.Milestone == nil || child.Milestone.Title != "Sprint 42" ||
		len(child.Assignees) != 1 || child.Assignees[0].Username != me.Username || !contains(child.Labels, "type:story") {
		t.Fatalf("child = %+v: the tracker's milestone must map to the web repo's, with the assignee and label", child)
	}

	if err := c.SetLabels(ctx, child.Ref, []string{"workflow:in progress"}, []string{"type:story", "label-it-never-had"}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Issue(ctx, child.Ref)
	if err != nil || !contains(got.Labels, "workflow:in progress") || contains(got.Labels, "type:story") {
		t.Fatalf("labels after SetLabels = %v, %v", got.Labels, err)
	}

	if err := c.LinkIssues(ctx, parent.Ref, child.Ref, "relates_to"); err != nil {
		t.Fatal(err)
	}
	if err := c.LinkIssues(ctx, parent.Ref, child.Ref, "relates_to"); err != nil {
		t.Errorf("linking an existing pair must be a no-op: %v", err)
	}
	links, err := c.Links(ctx, parent.Ref)
	n := 0
	for _, l := range links {
		if l.Ref == child.Ref && l.Type == "sub_issue" {
			n++
		}
	}
	if err != nil || n != 1 {
		t.Errorf("parent links = %+v (%d to the child), %v", links, n, err)
	}
	if err := c.LinkIssues(ctx, parent.Ref, child.Ref, "blocks"); !errors.Is(err, forge.ErrUnavailable) {
		t.Errorf("a blocks link: err = %v, want ErrUnavailable", err)
	}

	branch := "ade-live-" + fmt.Sprint(time.Now().Unix())
	b, err := c.CreateBranch(ctx, s.web, branch, "main")
	if err != nil || b.SHA == "" || b.Name != branch {
		t.Fatalf("branch = %+v, %v", b, err)
	}
	t.Cleanup(func() {
		s.raw(t, http.MethodDelete, fmt.Sprintf("/repos/%s/git/refs/heads/%s", s.web, branch), nil, nil)
	})
	if _, err := c.CreateBranch(ctx, s.web, branch, "main"); !errors.Is(err, forge.ErrExists) {
		t.Errorf("creating the branch twice: err = %v, want ErrExists", err)
	}
	if bs, err := c.Branches(ctx, s.web, "ade-live-"); err != nil || !contains(bs, branch) {
		t.Errorf("Branches = %v, %v", bs, err)
	}

	mr, err := c.CreateMR(ctx, forge.MRDraft{
		Project: s.web, Source: branch, Target: "main", Title: tag + " draft", Draft: true,
		Description: "Closes #" + fmt.Sprint(child.Ref.IID), Labels: []string{"type:story"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.raw(t, http.MethodPatch, fmt.Sprintf("/repos/%s/pulls/%d", s.web, mr.Ref.IID), map[string]string{"state": "closed"}, nil)
	})
	if !mr.Draft || mr.SourceBranch != branch || mr.State != "opened" {
		t.Errorf("MR = %+v, want a draft from %s (a commitless branch gets an empty start commit)", mr, branch)
	}
	if _, err := c.CreateMR(ctx, forge.MRDraft{Project: s.web, Source: branch, Target: "main", Title: tag + " again"}); !errors.Is(err, forge.ErrExists) {
		t.Errorf("a second MR for the branch: err = %v, want ErrExists", err)
	}
	full, err := c.MR(ctx, mr.Ref)
	if err != nil || full.HeadSHA == b.SHA {
		t.Errorf("head = %s (branch started at %s): the empty start commit is missing (%v)", full.HeadSHA, b.SHA, err)
	}
	if mrs, err := c.MRsForIssue(ctx, child.Ref); err != nil || len(mrs) != 1 || mrs[0].Ref != mr.Ref {
		t.Errorf("MRsForIssue = %+v, %v", mrs, err)
	}
}
