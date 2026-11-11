package review_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/review"
	"github.com/coredev-uk/ade/internal/store"
)

func openStore(t *testing.T, path string) *review.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return review.NewStore(st)
}

func TestStoreSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	mr := forge.Ref{Project: "g/api", IID: 3, Kind: forge.KindMR}
	other := forge.Ref{Project: "g/web", IID: 1, Kind: forge.KindMR}

	st1, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := review.NewStore(st1)
	at := time.Date(2026, 11, 11, 12, 0, 0, 123, time.UTC)
	want := []review.Draft{
		{ID: "a", Path: "x.go", OldPath: "w.go", Line: 4, Side: review.SideOld, Body: "first, with 'quotes' and\nnewlines", Severity: review.SeverityNit, Source: review.SourceAgent, State: review.StatePending, CreatedAt: at},
		{ID: "b", Path: "y.go", Line: 9, Side: review.SideNew, Body: "second", Source: review.SourceYou, State: review.StateAccepted, RemoteID: 77, Published: true, CreatedAt: at},
	}
	for _, d := range want {
		if err := s.Put(mr, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Put(other, review.Draft{ID: "a", Path: "z", Line: 1, Side: review.SideNew, Body: "z", Source: review.SourceYou, State: review.StateAccepted, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	st1.Close()

	s = openStore(t, path) // a new process
	got, err := s.List(mr)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("drafts after reopen = %+v, want %+v", got, want)
	}

	// Put replaces by id and keeps the position in the list.
	want[0].Body, want[0].State = "edited", review.StateAccepted
	if err := s.Put(mr, want[0]); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.List(mr); len(got) != 2 || got[0].Body != "edited" || got[0].ID != "a" {
		t.Errorf("after update = %+v", got)
	}
	if mrs, _ := s.MRs(); len(mrs) != 2 || mrs[0] != mr || mrs[1] != other {
		t.Errorf("MRs = %v", mrs)
	}

	// Save replaces the whole set, leaving other MRs alone.
	if err := s.Save(mr, want[1:]); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.List(mr); len(got) != 1 || got[0].ID != "b" {
		t.Errorf("after Save = %+v", got)
	}
	if got, _ = s.List(other); len(got) != 1 {
		t.Errorf("another MR's drafts changed: %+v", got)
	}
	if err := s.Delete(mr, "b"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(mr, "missing"); err != nil {
		t.Errorf("deleting a missing draft: %v", err)
	}
	if got, _ = s.List(mr); len(got) != 0 {
		t.Errorf("drafts = %+v", got)
	}
}

func TestSessionAddValidatesAgainstTheDiff(t *testing.T) {
	c, _ := newFake(t, fake.Free)
	ctx := context.Background()
	st := openStore(t, filepath.Join(t.TempDir(), "cache.db"))
	sess, err := review.Open(ctx, c, st, renameMR)
	if err != nil {
		t.Fatal(err)
	}

	d, err := sess.Add(review.Draft{Path: "limit/token_bucket.go", Line: 5, Body: "  why?  ", Severity: review.SeverityQuestion})
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == "" || d.Body != "why?" || d.Side != review.SideNew || d.Source != review.SourceYou || d.State != review.StateAccepted ||
		d.OldPath != "limit/bucket.go" || d.CreatedAt.IsZero() {
		t.Errorf("draft = %+v", d)
	}
	agent, err := sess.Add(review.Draft{Path: "limit/token_bucket.go", Line: 3, Body: "Refill is not atomic.", Source: review.SourceAgent})
	if err != nil || agent.State != review.StatePending {
		t.Fatalf("agent draft = %+v, %v", agent, err)
	}
	// By the old name of the renamed file, on the removed side.
	if _, err := sess.Add(review.Draft{Path: "limit/bucket.go", Line: 3, Side: review.SideOld, Body: "Keep this wording."}); err != nil {
		t.Fatal(err)
	}

	for name, bad := range map[string]review.Draft{
		"line outside the hunks": {Path: "limit/token_bucket.go", Line: 50, Body: "x"},
		"unknown file":           {Path: "nope.go", Line: 1, Body: "x"},
		"no body":                {Path: "limit/token_bucket.go", Line: 3, Body: " "},
		"no line":                {Path: "limit/token_bucket.go", Body: "x"},
		"bad severity":           {Path: "limit/token_bucket.go", Line: 3, Body: "x", Severity: "scary"},
		"bad side":               {Path: "limit/token_bucket.go", Line: 3, Body: "x", Side: "left"},
	} {
		if _, err := sess.Add(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if len(sess.Drafts) != 3 {
		t.Fatalf("session drafts = %d, want only the valid 3", len(sess.Drafts))
	}

	if err := sess.Decide(agent.ID, review.StateDenied); err != nil {
		t.Fatal(err)
	}
	if err := sess.Decide("nope", review.StateDenied); err == nil {
		t.Error("deciding an unknown draft succeeded")
	}
	if err := sess.Remove(d.ID); err != nil {
		t.Fatal(err)
	}

	// A second process (a fresh session) sees exactly what was saved.
	again, err := review.Open(ctx, c, st, renameMR)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Drafts) != 2 || again.Drafts[0].State != review.StateDenied {
		t.Errorf("reloaded drafts = %+v", again.Drafts)
	}
}

func TestSessionSubmitClearsSentDrafts(t *testing.T) {
	c, srv := newFake(t, fake.Free)
	ctx := context.Background()
	st := openStore(t, filepath.Join(t.TempDir(), "cache.db"))
	sess, _ := review.Open(ctx, c, st, renameMR)
	for _, line := range []int{3, 5, 7} {
		if _, err := sess.Add(review.Draft{Path: "limit/token_bucket.go", Line: line, Body: "c"}); err != nil {
			t.Fatal(err)
		}
	}
	pending, _ := sess.Add(review.Draft{Path: "limit/token_bucket.go", Line: 8, Body: "agent says", Source: review.SourceAgent})
	denied, _ := sess.Add(review.Draft{Path: "limit/token_bucket.go", Line: 10, Body: "no", Source: review.SourceYou})
	_ = sess.Decide(denied.ID, review.StateDenied)

	// A failing first attempt leaves resumable progress in the store.
	w := &flaky{MRWriter: c, failAdd: 2}
	if _, err := sess.Submit(ctx, w, "", false); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	saved, _ := st.List(renameMR)
	if len(saved) != 5 || saved[0].RemoteID == 0 || saved[1].RemoteID != 0 {
		t.Fatalf("saved progress = %+v", saved)
	}

	// A new process resumes and finishes.
	sess2, _ := review.Open(ctx, c, st, renameMR)
	res, err := sess2.Submit(ctx, c, "", false)
	if err != nil || res.Published != 3 || res.Resumed != 1 {
		t.Fatalf("resume = %+v, %v", res, err)
	}
	if n, _ := srv.Publishes(renameMR); n != 1 {
		t.Errorf("publishes = %d", n)
	}
	left, _ := st.List(renameMR)
	if len(left) != 1 || left[0].ID != pending.ID {
		t.Errorf("after submit the store holds %+v, want only the untriaged agent draft", left)
	}
}

func TestAuditedWriter(t *testing.T) {
	c, srv := newFake(t, fake.Free)
	ctx := context.Background()
	log, _ := audit.Open(t.TempDir(), "user")
	ds, _ := c.Discussions(ctx, renameMR)

	w := review.Audited(c, log, false)
	if _, err := w.Reply(ctx, renameMR, ds[0].ID, "a secret-ish body"); err != nil {
		t.Fatal(err)
	}
	if err := w.Resolve(ctx, renameMR, ds[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if err := w.Resolve(ctx, renameMR, "nope", true); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	es, _ := log.Query(audit.Filter{Action: "forge.*"})
	if len(es) != 3 {
		t.Fatalf("entries = %+v", es)
	}
	if es[0].Action != "forge.thread.reply" || es[0].Item != renameMR.String() || es[0].Target != ds[0].ID || es[0].Params["chars"] != float64(17) {
		t.Errorf("reply entry = %+v", es[0])
	}
	if _, has := es[0].Params["body"]; has {
		t.Error("comment body written to the audit log")
	}
	if es[1].Outcome != "ok" || es[2].Outcome != "error" || es[2].Error == "" {
		t.Errorf("outcomes = %q / %q", es[1].Outcome, es[2].Outcome)
	}

	// Dry-run: reads pass through, writes are recorded but not made.
	dry := review.Audited(c, log, true)
	if _, err := dry.MRDiff(ctx, renameMR); err != nil {
		t.Fatal(err)
	}
	if err := dry.Approve(ctx, renameMR, ""); err != nil {
		t.Fatal(err)
	}
	if err := dry.Merge(ctx, renameMR, forge.MergeOpts{SHA: "x"}); err != nil {
		t.Fatal(err)
	}
	if a, _ := srv.Approvers(renameMR); len(a) != 0 {
		t.Errorf("dry run approved: %v", a)
	}
	if merged, _, _, _ := srv.MergeState(renameMR); merged {
		t.Error("dry run merged")
	}
	es, _ = log.Query(audit.Filter{Action: "forge.*"})
	last := es[len(es)-1]
	if last.Action != "forge.mr.merge" || !last.DryRun || last.Outcome != "skipped" {
		t.Errorf("dry-run entry = %+v", last)
	}
}
