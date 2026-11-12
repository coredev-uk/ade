package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// approved plans a run and approves the plan, ready to implement.
func (e *env) approved() Run {
	e.t.Helper()
	ctx := context.Background()
	r := e.newRun(KindPlan, e.ctx())
	r, err := e.m.Plan(ctx, r.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	if r, err = e.m.Approve(r.ID); err != nil {
		e.t.Fatal(err)
	}
	return r
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestImplementApartThenAcceptFastForwardsTheItemsBranch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r := e.approved()
	// The user keeps working in the item's tree meanwhile: an uncommitted file
	// must neither block the run nor be touched by accepting it.
	if err := os.WriteFile(filepath.Join(e.wt, "scratch.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := sh(t, e.wt, "rev-parse", "HEAD")

	r, err := e.m.ImplementApart(ctx, r.ID)
	if err != nil || r.State != StateReview {
		t.Fatalf("implement apart: %v %+v", err, r)
	}
	wantTree := filepath.Join(filepath.Dir(e.wt), "42-version-ai")
	if r.AIBranch != "42-version-ai" || r.AIWorktree != wantTree || r.BaseSHA != base || !r.Isolated() {
		t.Fatalf("sandbox = %q %q base %q", r.AIBranch, r.AIWorktree, r.BaseSHA)
	}
	// The agent worked in the -ai tree; the item's tree is as the user left it.
	if !exists(filepath.Join(wantTree, "hello.txt")) || exists(filepath.Join(e.wt, "hello.txt")) {
		t.Fatal("the agent's file must be in the -ai worktree only")
	}
	if got := sh(t, e.wt, "rev-parse", "HEAD"); got != base {
		t.Fatalf("the item's branch moved before accept: %s", got)
	}
	if got := sh(t, wantTree, "rev-parse", "--abbrev-ref", "HEAD"); got != "42-version-ai" {
		t.Fatalf("-ai worktree is on %q", got)
	}
	// The diff is the sandbox's, and the run cannot be pushed yet.
	d, _, err := e.m.Diff(ctx, r.ID, false)
	if err != nil || len(d.Files) != 1 || d.Files[0].Path != "hello.txt" {
		t.Fatalf("diff = %+v %v", d, err)
	}
	if _, err := e.m.Push(ctx, r.ID, PushOpts{}); err == nil || !strings.Contains(err.Error(), "accept") {
		t.Fatalf("an isolated run must be accepted before it can be pushed: %v", err)
	}
	if prompt, err := os.ReadFile(filepath.Join(e.st.Dir(r), "prompt-run.md")); err != nil || !strings.Contains(string(prompt), wantTree) || !strings.Contains(string(prompt), "42-version-ai") {
		t.Errorf("the agent is told about its own worktree and branch: %q %v", prompt, err)
	}

	r, err = e.m.Accept(ctx, r.ID, AcceptOpts{})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if r.State != StateReview || r.Isolated() || r.AIWorktree != "" || r.AcceptedAt.IsZero() || len(r.Commits) != 1 {
		t.Fatalf("accepted run = %+v", r)
	}
	// Fast-forwarded: the item's branch is exactly the commit made on -ai.
	if got := sh(t, e.wt, "rev-parse", "HEAD"); got != r.Commits[0] {
		t.Fatalf("item branch at %s, the accepted commit is %s", got, r.Commits[0])
	}
	if got := sh(t, e.wt, "rev-parse", "HEAD^"); got != base {
		t.Fatalf("not a single fast-forward step from the base: parent %s", got)
	}
	if got := sh(t, e.wt, "log", "-1", "--format=%an|%s"); got != "Test User (ade agent)|Add /version endpoint" {
		t.Errorf("accepted commit = %q", got)
	}
	if b, err := os.ReadFile(filepath.Join(e.wt, "hello.txt")); err != nil || !strings.Contains(string(b), "hello from the agent") {
		t.Errorf("hello.txt after accept: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(e.wt, "scratch.txt")); err != nil || string(b) != "mine\n" {
		t.Errorf("the user's uncommitted file was disturbed: %q %v", b, err)
	}
	// Cleaned up: no worktree, no branch.
	if exists(wantTree) || sh(t, e.wt, "branch", "--list", "42-version-ai") != "" {
		t.Error("the -ai worktree and branch must be gone after accept")
	}
	// Never pushed: origin does not have the branch.
	if out := sh(t, e.bare, "branch", "--list", "42-version"); out != "" {
		t.Errorf("accept pushed: %q", out)
	}
	for _, a := range []string{"agent.isolate", "agent.accept"} {
		if len(e.auditEntries(a)) != 1 {
			t.Errorf("audit lacks one %s", a)
		}
	}
	// The accepted run is pushed like any other. Push commits whatever else is
	// in the tree, so the user's scratch file goes first; accept already
	// committed the agent's work, so nothing more is committed.
	if err := os.Remove(filepath.Join(e.wt, "scratch.txt")); err != nil {
		t.Fatal(err)
	}
	r, err = e.m.Push(ctx, r.ID, PushOpts{})
	if err != nil || r.State != StatePushed || len(r.Commits) != 1 {
		t.Fatalf("push after accept: %v %+v", err, r)
	}
	if got := sh(t, e.bare, "rev-parse", "42-version"); got != r.Commits[0] {
		t.Errorf("origin has %s, accepted commit is %s", got, r.Commits[0])
	}
}

func TestAcceptRefusesWhenTheItemsBranchMovedAndDiscardCleansUp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r := e.approved()
	r, err := e.m.ImplementApart(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The user commits to the item's branch while the agent worked.
	if err := os.WriteFile(filepath.Join(e.wt, "mine.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, e.wt, "add", "mine.txt")
	sh(t, e.wt, "commit", "-q", "-m", "my own work")
	mine := sh(t, e.wt, "rev-parse", "HEAD")

	_, err = e.m.Accept(ctx, r.ID, AcceptOpts{})
	if !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("accept must refuse a branch that is not a fast-forward, got %v", err)
	}
	if got := sh(t, e.wt, "rev-parse", "HEAD"); got != mine {
		t.Fatalf("a refused accept moved the item's branch: %s", got)
	}
	if exists(filepath.Join(e.wt, "hello.txt")) {
		t.Fatal("a refused accept changed the item's tree")
	}
	// The agent's work is still safe on the -ai branch, committed.
	if sh(t, e.wt, "log", "-1", "--format=%s", "42-version-ai") != "Add /version endpoint" {
		t.Error("the refused work must stay committed on the -ai branch")
	}
	got, err := e.m.Store.Get(r.ID)
	if err != nil || got.State != StateReview || !got.Isolated() || !strings.Contains(got.Error, "not a fast-forward") {
		t.Fatalf("a refused accept leaves the run in review with the reason: %+v %v", got, err)
	}

	r, err = e.m.Discard(ctx, r.ID)
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	if r.State != StateCancelled || r.Isolated() || r.AIWorktree != "" {
		t.Fatalf("discarded run = %+v", r)
	}
	if exists(filepath.Join(filepath.Dir(e.wt), "42-version-ai")) || sh(t, e.wt, "branch", "--list", "42-version-ai") != "" {
		t.Error("discard must remove the -ai worktree and branch")
	}
	if got := sh(t, e.wt, "rev-parse", "HEAD"); got != mine {
		t.Error("discard touched the item's branch")
	}
	if len(e.auditEntries("agent.discard")) != 1 {
		t.Error("discard is not audited")
	}
	if _, err := e.m.Discard(ctx, r.ID); err == nil {
		t.Error("there is nothing left to discard")
	}
}

func TestDiscardCleansUpAfterAFailedIsolatedRun(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r := e.approved()
	t.Setenv("FAKE_OMP", "fail")
	r, err := e.m.ImplementApart(ctx, r.ID)
	if err == nil || r.State != StateFailed || !r.Isolated() {
		t.Fatalf("a failed isolated run keeps its sandbox for inspection: %v %+v", err, r)
	}
	if !exists(r.AIWorktree) {
		t.Fatal("sandbox gone after the failure")
	}
	if _, err := e.m.Accept(ctx, r.ID, AcceptOpts{}); err == nil {
		t.Error("a failed run cannot be accepted")
	}
	r, err = e.m.Discard(ctx, r.ID)
	if err != nil || r.State != StateFailed || r.Isolated() {
		t.Fatalf("discard after failure: %v %+v", err, r)
	}
	if exists(filepath.Join(filepath.Dir(e.wt), "42-version-ai")) || sh(t, e.wt, "branch", "--list", "42-version-ai") != "" {
		t.Error("sandbox not removed")
	}
}

func TestImplementApartRefusesAnExistingAIBranch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sh(t, e.wt, "branch", "42-version-ai")
	r := e.approved()
	if _, err := e.m.ImplementApart(ctx, r.ID); err == nil || !strings.Contains(err.Error(), "42-version-ai exists already") {
		t.Fatalf("an existing -ai branch must not be reused or overwritten: %v", err)
	}
	got, _ := e.m.Store.Get(r.ID)
	if got.State != StateAwaiting || got.Isolated() {
		t.Fatalf("a refused start leaves the approved run as it was: %+v", got)
	}
}

func TestAcceptAndDiscardNeedAnIsolatedRun(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r := e.approved()
	r, err := e.m.Implement(ctx, r.ID) // in the item's own worktree
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Accept(ctx, r.ID, AcceptOpts{}); err == nil || !strings.Contains(err.Error(), "no -ai branch") {
		t.Errorf("accept on a normal run: %v", err)
	}
	if _, err := e.m.Discard(ctx, r.ID); err == nil || !strings.Contains(err.Error(), "no -ai branch") {
		t.Errorf("discard on a normal run: %v", err)
	}
}
