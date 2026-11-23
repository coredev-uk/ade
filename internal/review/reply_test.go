package review_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
	ghfake "github.com/coredev-uk/ade/internal/forge/github/fake"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/review"
)

// openThread is the first unresolved, resolvable thread of the MR.
func openThread(t *testing.T, ds []forge.Discussion) forge.Discussion {
	t.Helper()
	for _, d := range ds {
		if d.Resolvable && !d.Resolved && len(d.Notes) > 0 {
			return d
		}
	}
	t.Fatal("no open thread")
	return forge.Discussion{}
}

// An agent's reply is a reply: Submit posts it in the thread it answers, even
// when the line the thread was anchored on is gone from the diff (a stale line
// used to fail the whole submit), and creates no new discussion.
func agentReply(t *testing.T, thread forge.Discussion) review.Draft {
	t.Helper()
	d, err := review.Normalize(review.Draft{
		Path: "limit/token_bucket.go", Line: 99999, Body: "Done: renamed the variable.",
		Source: review.SourceAgent, State: review.StateAccepted, InReplyTo: thread.ID,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSubmitAgentReplyAnswersTheThreadOnGitLab(t *testing.T) {
	c, srv := newFake(t, fake.Free)
	ctx := context.Background()
	before, _ := c.Discussions(ctx, renameMR)
	thread := openThread(t, before)

	res, err := review.Submit(ctx, c, renameMR, []review.Draft{agentReply(t, thread)}, "", false)
	if err != nil || res.Uploaded != 1 || res.Published != 1 {
		t.Fatalf("submit = %+v %v", res, err)
	}
	if n, _ := srv.PendingDrafts(renameMR); n != 0 {
		t.Errorf("%d draft notes left", n)
	}
	after, _ := c.Discussions(ctx, renameMR)
	if len(after) != len(before) {
		t.Errorf("a reply must not open a discussion: %d -> %d", len(before), len(after))
	}
	for _, d := range after {
		if d.ID == thread.ID {
			if len(d.Notes) != len(thread.Notes)+1 || !strings.Contains(d.Notes[len(d.Notes)-1].Body, "Done: renamed the variable.") {
				t.Errorf("the thread after submit: %+v", d.Notes)
			}
		}
	}
	// Dry run: the reply is validated and counted, nothing is written.
	again := agentReply(t, thread)
	if res, err := review.Submit(ctx, c, renameMR, []review.Draft{again}, "", false, review.WithDryRun()); err != nil || !res.DryRun || res.Uploaded != 1 {
		t.Errorf("dry run = %+v %v", res, err)
	}
}

func TestSubmitAgentReplyResumesWithoutDuplicating(t *testing.T) {
	c, _ := newFake(t, fake.Free)
	ctx := context.Background()
	ds, _ := c.Discussions(ctx, renameMR)
	thread := openThread(t, ds)
	d := agentReply(t, thread)

	// The upload succeeded but its answer was lost: the next run finds the
	// reply on the forge by thread and body, and does not post it twice.
	w := &flaky{MRWriter: c, failAdd: 1, lose: true}
	if _, err := review.Submit(ctx, w, renameMR, []review.Draft{d}, "", false); err == nil {
		t.Fatal("the injected failure should surface")
	}
	res, err := review.Submit(ctx, c, renameMR, []review.Draft{d}, "", false)
	if err != nil || res.Resumed != 1 || res.Uploaded != 0 {
		t.Fatalf("resume = %+v %v", res, err)
	}
	after, _ := c.Discussions(ctx, renameMR)
	for _, got := range after {
		if got.ID == thread.ID && len(got.Notes) != len(thread.Notes)+1 {
			t.Errorf("the reply was posted %d times", len(got.Notes)-len(thread.Notes))
		}
	}
}

func TestSubmitAgentReplyAnswersTheThreadOnGitHub(t *testing.T) {
	srv := ghfake.New()
	t.Cleanup(srv.Close)
	c := github.New(srv.URL, ghfake.Token, github.WithSleep(func(context.Context, time.Duration) error { return nil }))
	ctx := context.Background()
	ref := forge.Ref{Project: "ade-demo/api", IID: 2, Kind: forge.KindMR}
	before, err := c.Discussions(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	thread := openThread(t, before)

	res, err := review.Submit(ctx, c, ref, []review.Draft{agentReply(t, thread)}, "", false)
	if err != nil || res.Uploaded != 1 || res.Published != 1 {
		t.Fatalf("submit = %+v %v", res, err)
	}
	after, _ := c.Discussions(ctx, ref)
	if len(after) != len(before) {
		t.Errorf("a reply must not open a thread: %d -> %d", len(before), len(after))
	}
	for _, d := range after {
		if d.ID == thread.ID && (len(d.Notes) != len(thread.Notes)+1 || !strings.Contains(d.Notes[len(d.Notes)-1].Body, "Done: renamed the variable.")) {
			t.Errorf("the thread after submit: %+v", d.Notes)
		}
	}
}

// Drafts saved before replies existed have no thread: they decode as comments
// of their own.
func TestDraftWithoutInReplyToStaysAComment(t *testing.T) {
	d, err := review.Normalize(review.Draft{Path: "a.go", Line: 3, Body: "x"}, time.Now())
	if err != nil || d.InReplyTo != "" {
		t.Errorf("draft = %+v %v", d, err)
	}
}
