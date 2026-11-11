package review_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/review"
)

var (
	renameMR = forge.Ref{Project: fake.GroupPath + "/api", IID: 2, Kind: forge.KindMR}
	errBoom  = errors.New("boom: connection reset")
)

func newFake(t *testing.T, tier fake.Tier) (*gitlab.Client, *fake.Server) {
	t.Helper()
	srv := fake.New(tier)
	t.Cleanup(srv.Close)
	return gitlab.New(srv.URL, fake.Token), srv
}

// drafts makes accepted drafts of yours on lines of the renamed file.
func drafts(t *testing.T, lines ...int) []review.Draft {
	t.Helper()
	var out []review.Draft
	for _, n := range lines {
		d, err := review.Normalize(review.Draft{Path: "limit/token_bucket.go", Line: n, Body: "comment on line " + string(rune('0'+n%10))}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func discussionCount(t *testing.T, c *gitlab.Client) int {
	t.Helper()
	ds, err := c.Discussions(context.Background(), renameMR)
	if err != nil {
		t.Fatal(err)
	}
	return len(ds)
}

// flaky injects failures into the writer calls a submit makes. lose performs
// the call first and then reports failure: the answer was lost, the effect was
// not.
type flaky struct {
	forge.MRWriter
	failAdd     int // fail the nth AddDraftNote (1-based); 0 = never
	failPublish bool
	failApprove bool
	lose        bool
	adds        int
}

func (f *flaky) AddDraftNote(ctx context.Context, mr forge.Ref, body string, pos *forge.DiffPosition, inReplyTo string) (forge.DraftNote, error) {
	f.adds++
	if f.adds != f.failAdd {
		return f.MRWriter.AddDraftNote(ctx, mr, body, pos, inReplyTo)
	}
	if f.lose {
		_, _ = f.MRWriter.AddDraftNote(ctx, mr, body, pos, inReplyTo)
	}
	return forge.DraftNote{}, errBoom
}

func (f *flaky) PublishDraftNotes(ctx context.Context, mr forge.Ref) error {
	if !f.failPublish {
		return f.MRWriter.PublishDraftNotes(ctx, mr)
	}
	f.failPublish = false
	if f.lose {
		_ = f.MRWriter.PublishDraftNotes(ctx, mr)
	}
	return errBoom
}

func (f *flaky) Approve(ctx context.Context, mr forge.Ref, sha string) error {
	if !f.failApprove {
		return f.MRWriter.Approve(ctx, mr, sha)
	}
	f.failApprove = false
	return errBoom
}

func TestSubmitFiveDraftsOneBatch(t *testing.T) {
	for _, tier := range []fake.Tier{fake.Free, fake.Premium} {
		t.Run(tier.String(), func(t *testing.T) {
			c, srv := newFake(t, tier)
			ctx := context.Background()
			before := discussionCount(t, c)

			ds := drafts(t, 3, 5, 7, 8, 10)
			ds[0].Severity = review.SeverityBlocking
			res, err := review.Submit(ctx, c, renameMR, ds, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if res.Uploaded != 5 || res.Published != 5 || res.Resumed != 0 || res.Approved {
				t.Errorf("result = %+v", res)
			}
			if n, _ := srv.Publishes(renameMR); n != 1 {
				t.Errorf("bulk publishes = %d, want exactly 1", n)
			}
			if n, _ := srv.PendingDrafts(renameMR); n != 0 {
				t.Errorf("%d draft notes left", n)
			}
			all, _ := c.Discussions(ctx, renameMR)
			if len(all) != before+5 {
				t.Fatalf("discussions %d -> %d, want +5", before, len(all))
			}

			// Each landed on its requested line of the renamed file. Line 8 is an
			// unchanged line, anchored by both numbers (old 7).
			type at struct{ old, new int }
			got := map[at]forge.Discussion{}
			for _, d := range all[before:] {
				if d.Position == nil || d.Position.Path != "limit/token_bucket.go" || d.Position.OldPath != "limit/bucket.go" {
					t.Fatalf("thread = %+v position %+v", d, d.Position)
				}
				got[at{d.Position.OldLine, d.Position.NewLine}] = d
			}
			for _, w := range []at{{0, 3}, {5, 5}, {0, 7}, {7, 8}, {0, 10}} {
				if _, ok := got[w]; !ok {
					t.Errorf("no thread at %+v; have %v", w, keys(got))
				}
			}
			if body := got[at{0, 3}].Notes[0].Body; body != "**blocking:** comment on line 3" {
				t.Errorf("severity not rendered: %q", body)
			}
			for _, d := range res.Drafts {
				if !d.Published || d.RemoteID == 0 {
					t.Errorf("draft not recorded as sent: %+v", d)
				}
			}
		})
	}
}

func keys[K comparable, V any](m map[K]V) []K {
	var out []K
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSubmitRetryAfterMidwayFailureDoesNotDuplicate(t *testing.T) {
	for _, lose := range []bool{false, true} {
		for _, fromProgress := range []bool{true, false} {
			name := "request failed"
			if lose {
				name = "answer lost after the forge accepted it"
			}
			if fromProgress {
				name += "/resume from saved progress"
			} else {
				name += "/progress lost, drafts as originally written"
			}
			t.Run(name, func(t *testing.T) {
				c, srv := newFake(t, fake.Free)
				ctx := context.Background()
				before := discussionCount(t, c)
				ds := drafts(t, 3, 5, 7, 8, 10)

				w := &flaky{MRWriter: c, failAdd: 3, lose: lose}
				var saved []review.Draft
				res, err := review.Submit(ctx, w, renameMR, ds, "", false, review.WithProgress(func(d []review.Draft) error { saved = d; return nil }))
				if !errors.Is(err, errBoom) {
					t.Fatalf("err = %v, want the injected failure", err)
				}
				if res.Uploaded != 2 || res.Published != 0 || len(saved) != 5 || slices.ContainsFunc(saved, func(d review.Draft) bool { return d.Published }) {
					t.Errorf("partial result = %+v, progress %+v", res, saved)
				}
				if n, _ := srv.Publishes(renameMR); n != 0 {
					t.Fatal("published despite the failure")
				}
				onForge := 2
				if lose {
					onForge = 3 // the lost answer still created the third draft note
				}
				if n, _ := srv.PendingDrafts(renameMR); n != onForge {
					t.Fatalf("draft notes on the forge after the failure = %d, want %d", n, onForge)
				}

				in := ds
				if fromProgress {
					in = saved
				}
				res, err = review.Submit(ctx, w, renameMR, in, "", false)
				if err != nil {
					t.Fatal(err)
				}
				if res.Resumed != onForge || res.Uploaded != 5-onForge || res.Published != 5 {
					t.Errorf("retry = %+v, want %d resumed and %d uploaded", res, onForge, 5-onForge)
				}
				if n, _ := srv.Publishes(renameMR); n != 1 {
					t.Errorf("publishes = %d, want 1", n)
				}
				if got := discussionCount(t, c); got != before+5 {
					t.Errorf("discussions %d -> %d, want exactly +5 (no duplicates)", before, got)
				}
				// Submitting what is already published sends nothing.
				again, err := review.Submit(ctx, w, renameMR, res.Drafts, "", false)
				if err != nil || again.Uploaded+again.Published != 0 {
					t.Errorf("second submit = %+v, %v", again, err)
				}
				if n, _ := srv.Publishes(renameMR); n != 1 {
					t.Errorf("publishes = %d after a no-op submit", n)
				}
			})
		}
	}
}

func TestSubmitPublishFailure(t *testing.T) {
	for _, lose := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish refused", true: "publish done, answer lost"}[lose], func(t *testing.T) {
			c, srv := newFake(t, fake.Free)
			ctx := context.Background()
			before := discussionCount(t, c)
			w := &flaky{MRWriter: c, failPublish: true, lose: lose}
			res, err := review.Submit(ctx, w, renameMR, drafts(t, 3, 5), "", true)
			if !errors.Is(err, errBoom) || res.Approved {
				t.Fatalf("err = %v, result %+v", err, res)
			}
			if approvers, _ := srv.Approvers(renameMR); len(approvers) != 0 {
				t.Errorf("approved although the publish failed: %v", approvers)
			}
			res, err = review.Submit(ctx, w, renameMR, res.Drafts, "", true)
			if err != nil {
				t.Fatal(err)
			}
			if lose && res.Assumed != 2 {
				t.Errorf("assumed = %d, want 2 (already published, gone from the draft list)", res.Assumed)
			}
			if n, _ := srv.Publishes(renameMR); n != 1 {
				t.Errorf("publishes = %d, want 1", n)
			}
			if got := discussionCount(t, c); got != before+2 {
				t.Errorf("discussions %d -> %d, want +2", before, got)
			}
			if !res.Approved {
				t.Error("retry did not approve")
			}
		})
	}
}

func TestSubmitApproveFailureRetriesOnlyTheApproval(t *testing.T) {
	c, srv := newFake(t, fake.Free)
	ctx := context.Background()
	w := &flaky{MRWriter: c, failApprove: true}
	res, err := review.Submit(ctx, w, renameMR, drafts(t, 3, 5), "Looks good overall.", true)
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	if n, _ := srv.Publishes(renameMR); n != 1 {
		t.Fatalf("publishes = %d", n)
	}
	// The summary rides along as an overview comment, and every draft is recorded published.
	if res.Published != 3 || !slices.ContainsFunc(res.Drafts, func(d review.Draft) bool { return d.ID == review.SummaryID && d.Published }) {
		t.Fatalf("result = %+v", res)
	}
	before := discussionCount(t, c)
	res, err = review.Submit(ctx, w, renameMR, res.Drafts, "Looks good overall.", true)
	if err != nil || !res.Approved || res.Uploaded != 0 || res.Published != 0 {
		t.Fatalf("retry = %+v, %v", res, err)
	}
	if n, _ := srv.Publishes(renameMR); n != 1 || discussionCount(t, c) != before {
		t.Error("the retry published or created threads again")
	}
	if a, _ := srv.Approvers(renameMR); !slices.Contains(a, "sandbox-dev") {
		t.Errorf("approvers = %v", a)
	}
}

func TestSubmitValidatesBeforeWriting(t *testing.T) {
	c, srv := newFake(t, fake.Free)
	ds := drafts(t, 3, 5)
	ds = append(ds, drafts(t, 99)...) // outside every hunk
	res, err := review.Submit(context.Background(), c, renameMR, ds, "", false)
	if !errors.Is(err, review.ErrNotInDiff) {
		t.Fatalf("err = %v, want ErrNotInDiff", err)
	}
	if res.Uploaded != 0 {
		t.Errorf("uploaded %d before validating", res.Uploaded)
	}
	if n, _ := srv.PendingDrafts(renameMR); n != 0 {
		t.Errorf("%d draft notes created despite the invalid position", n)
	}
}

func TestSubmitSelection(t *testing.T) {
	c, _ := newFake(t, fake.Free)
	ctx := context.Background()
	before := discussionCount(t, c)
	ds := drafts(t, 3, 5, 7, 8)
	ds[1].Source, ds[1].State = review.SourceAgent, review.StatePending  // untriaged agent draft: held back
	ds[2].Source, ds[2].State = review.SourceAgent, review.StateAccepted // accepted agent draft: sent
	ds[3].State = review.StateDenied                                     // denied: never sent

	res, err := review.Submit(ctx, c, renameMR, ds, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Published != 2 || discussionCount(t, c) != before+2 {
		t.Errorf("result = %+v", res)
	}
	for _, i := range []int{1, 3} {
		if res.Drafts[i].RemoteID != 0 || res.Drafts[i].Published {
			t.Errorf("draft %d was sent: %+v", i, res.Drafts[i])
		}
	}

	// A draft uploaded and then denied is taken back before the publish.
	c2, srv2 := newFake(t, fake.Free)
	w := &flaky{MRWriter: c2, failPublish: true}
	r1, err := review.Submit(ctx, w, renameMR, drafts(t, 3, 5), "", false)
	if !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	r1.Drafts[0].State = review.StateDenied
	r2, err := review.Submit(ctx, w, renameMR, r1.Drafts, "", false)
	if err != nil || r2.Published != 1 {
		t.Fatalf("after denying one: %+v, %v", r2, err)
	}
	if n, _ := srv2.PendingDrafts(renameMR); n != 0 {
		t.Errorf("%d draft notes left", n)
	}
	if n, _ := srv2.Publishes(renameMR); n != 1 {
		t.Errorf("publishes = %d", n)
	}
	if got := discussionCount(t, c2); got != 1+1 { // seeded thread + the one kept comment
		t.Errorf("discussions = %d, want 2", got)
	}
}

func TestSubmitDryRunWritesNothing(t *testing.T) {
	c, srv := newFake(t, fake.Free)
	log, _ := audit.Open(t.TempDir(), "user")
	res, err := review.Submit(context.Background(), c, renameMR, drafts(t, 3, 5), "", true, review.WithDryRun(), review.WithAudit(log))
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Uploaded != 2 || res.Published != 2 || !res.Approved {
		t.Errorf("result = %+v", res)
	}
	if n, _ := srv.PendingDrafts(renameMR); n != 0 {
		t.Error("dry run created draft notes")
	}
	if n, _ := srv.Publishes(renameMR); n != 0 {
		t.Error("dry run published")
	}
	if a, _ := srv.Approvers(renameMR); len(a) != 0 {
		t.Error("dry run approved")
	}
	es, _ := log.Query(audit.Filter{Action: "review.submit"})
	if len(es) != 1 || !es[0].DryRun || es[0].Outcome != "skipped" {
		t.Errorf("audit = %+v", es)
	}
	// A dry run still validates positions.
	if _, err := review.Submit(context.Background(), c, renameMR, drafts(t, 99), "", false, review.WithDryRun()); !errors.Is(err, review.ErrNotInDiff) {
		t.Errorf("dry run accepted a bad line: %v", err)
	}
}

func TestSubmitAuditAndEvent(t *testing.T) {
	c, _ := newFake(t, fake.Free)
	log, _ := audit.Open(t.TempDir(), "user")
	bus := events.NewBus()
	ch, cancel := bus.Subscribe(4)
	defer cancel()

	if _, err := review.Submit(context.Background(), c, renameMR, drafts(t, 3, 5, 7), "", true, review.WithAudit(log), review.WithBus(bus)); err != nil {
		t.Fatal(err)
	}
	es, _ := log.Query(audit.Filter{Action: "review.submit"})
	if len(es) != 1 {
		t.Fatalf("audit entries = %+v", es)
	}
	e := es[0]
	if e.Item != renameMR.String() || e.Outcome != "ok" || e.Params["uploaded"] != float64(3) || e.Params["published"] != float64(3) || e.Params["approved"] != true {
		t.Errorf("entry = %+v", e)
	}
	select {
	case ev := <-ch:
		if ev.Kind != "review.submitted" || ev.Subject != renameMR.String() || !ev.Transient || !strings.Contains(ev.Title, "3 comments") || !strings.Contains(ev.Title, "approved") {
			t.Errorf("event = %+v", ev)
		}
	default:
		t.Error("no review.submitted event")
	}

	// A failure is audited as an error and raises no event.
	w := &flaky{MRWriter: c, failAdd: 1}
	if _, err := review.Submit(context.Background(), w, renameMR, drafts(t, 8), "", false, review.WithAudit(log), review.WithBus(bus)); err == nil {
		t.Fatal("want failure")
	}
	es, _ = log.Query(audit.Filter{Action: "review.submit"})
	if len(es) != 2 || es[1].Outcome != "error" || !strings.Contains(es[1].Error, "boom") {
		t.Errorf("entries = %+v", es)
	}
	select {
	case ev := <-ch:
		t.Errorf("event on failure: %+v", ev)
	default:
	}
}

func TestSubmitForeignDrafts(t *testing.T) {
	c, _ := newFake(t, fake.Free)
	ctx := context.Background()
	d, _ := c.MRDiff(ctx, renameMR)
	pos, _ := review.LineFor(d, "limit/token_bucket.go", 10)
	if _, err := c.AddDraftNote(ctx, renameMR, "started in the web UI", &pos, ""); err != nil {
		t.Fatal(err)
	}
	res, err := review.Submit(ctx, c, renameMR, drafts(t, 3), "", false)
	if err != nil || res.Foreign != 1 {
		t.Errorf("result = %+v, %v (the web draft goes out with the batch and should be reported)", res, err)
	}
}

func TestSubmitNeedsNothingForOverviewOnly(t *testing.T) {
	c, srv := newFake(t, fake.Free)
	before := discussionCount(t, c)
	res, err := review.Submit(context.Background(), c, renameMR, nil, "Just a note.", false)
	if err != nil || res.Published != 1 || discussionCount(t, c) != before+1 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if n, _ := srv.Publishes(renameMR); n != 1 {
		t.Errorf("publishes = %d", n)
	}
	for _, r := range srv.Requests() {
		if strings.HasSuffix(r.Path, "/diffs") || strings.HasSuffix(r.Path, "/versions") {
			t.Errorf("fetched the diff for an overview-only review: %s", r.Path)
		}
	}
}
