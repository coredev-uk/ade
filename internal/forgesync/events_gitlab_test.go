package forgesync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// The fake sandbox user is "sandbox-dev"; "reviewer-rae" is the teammate. api!1
// is mine, reviewed by rae, with a thread rae opened and I answered.
const (
	apiMR1 = "ade-demo-env/api!1"
	meUser = "sandbox-dev"
	rae    = "reviewer-rae"
)

// glEnv syncs the fake GitLab once as a baseline; next syncs again and
// returns the events raised since, by kind.
type glEnv struct {
	t   *testing.T
	h   *harness
	srv *fake.Server
	got func() []events.Event
	c   *gitlab.Client
}

func newGLEnv(t *testing.T, tier fake.Tier) *glEnv {
	t.Helper()
	srv := fake.New(tier)
	t.Cleanup(srv.Close)
	h := newHarness(t, manifestFile{"demo-gl", sandboxManifest})
	c := gitlab.New(srv.URL, fake.Token)
	h.svc.Open = func(context.Context, string, config.Forge) (forge.Forge, error) { return c, nil }
	h.svc.Now = func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }
	g := &glEnv{t: t, h: h, srv: srv, got: h.listen(), c: c}
	if res := h.sync(); res[0].Err != "" {
		t.Fatalf("baseline sync: %s", res[0].Err)
	}
	g.got() // the baseline raises nothing but sync.done
	return g
}

func (g *glEnv) next() map[string][]events.Event {
	g.t.Helper()
	if res := g.h.sync(); res[0].Err != "" {
		g.t.Fatalf("sync: %s", res[0].Err)
	}
	return kinds(g.got())
}

func (g *glEnv) must(err error) {
	g.t.Helper()
	if err != nil {
		g.t.Fatal(err)
	}
}

// wantOnly fails unless exactly the listed kinds were raised (sync.done aside).
func wantOnly(t *testing.T, got map[string][]events.Event, want ...string) {
	t.Helper()
	delete(got, EvSyncDone)
	if len(got) != len(want) {
		t.Fatalf("raised %v, want exactly %v", keysOf(got), want)
	}
	for _, k := range want {
		if len(got[k]) != 1 {
			t.Fatalf("%s raised %d times; all: %v", k, len(got[k]), keysOf(got))
		}
	}
}

func keysOf(m map[string][]events.Event) []string {
	var out []string
	for k, v := range m {
		out = append(out, k+"×"+string(rune('0'+len(v))))
	}
	return out
}

func TestEventCICanceled(t *testing.T) {
	g := newGLEnv(t, fake.Free)
	g.must(g.srv.SetPipelineStatus(ref(apiMR1), "canceled"))
	got := g.next()
	wantOnly(t, got, EvCICanceled)
	if e := got[EvCICanceled][0]; e.Severity != events.Warn || e.Title != "pipeline canceled on api!1" || e.Subject != "ade-demo-env/tracker#1" || e.Link == "" {
		t.Errorf("ci.canceled = %+v", e)
	}
	// Still canceled at the next sync: not news again.
	wantOnly(t, g.next())
}

func TestEventChangesRequested(t *testing.T) {
	g := newGLEnv(t, fake.Free)
	g.must(g.srv.SetReviewState(ref(apiMR1), rae, "requested_changes"))
	got := g.next()
	wantOnly(t, got, EvMRChangesAsked)
	if e := got[EvMRChangesAsked][0]; e.Severity != events.Warn || e.Title != "changes requested on api!1 by Rae Reviewer ("+rae+")" || e.Subject != "ade-demo-env/tracker#1" {
		t.Errorf("mr.changes_requested = %+v", e)
	}
	wantOnly(t, g.next())

	// Rae approving afterwards clears the verdict; asking again is news again.
	g.must(g.srv.SetReviewState(ref(apiMR1), rae, "approved"))
	wantOnly(t, g.next())
	g.must(g.srv.SetReviewState(ref(apiMR1), rae, "requested_changes"))
	wantOnly(t, g.next(), EvMRChangesAsked)
}

func TestEventMyOwnChangesRequestIsNotEchoed(t *testing.T) {
	g := newGLEnv(t, fake.Free)
	g.must(g.srv.SetReviewState(ref(apiMR1), meUser, "requested_changes"))
	got := g.next()
	delete(got, EvReviewAsked) // the fake lists me as a reviewer of my own MR
	wantOnly(t, got)
}

func TestEventCommentCountsRepliesPerDiscussion(t *testing.T) {
	g := newGLEnv(t, fake.Free)
	discs, err := g.c.Discussions(context.Background(), ref(apiMR1))
	g.must(err)
	var open string // the thread rae opened and I answered
	for _, d := range discs {
		if len(d.Notes) == 2 && !d.Notes[0].System {
			open = d.ID
		}
	}
	if open == "" {
		t.Fatalf("no two-note thread in %+v", discs)
	}

	// A reply in an existing thread leaves the thread count unchanged: the
	// old "thread count rose" rule missed it.
	_, err = g.srv.AddNote(ref(apiMR1), open, rae, "Fine, thanks.")
	g.must(err)
	got := g.next()
	wantOnly(t, got, EvMRComment)
	if e := got[EvMRComment][0]; e.Title != "new reply on api!1" || e.Subject != "ade-demo-env/tracker#1" {
		t.Errorf("reply event = %+v", e)
	}

	// A new thread.
	_, err = g.srv.AddNote(ref(apiMR1), "", rae, "One more thing.")
	g.must(err)
	got = g.next()
	wantOnly(t, got, EvMRComment)
	if e := got[EvMRComment][0]; e.Title != "new thread on api!1" {
		t.Errorf("thread event = %+v", e)
	}

	// A thread and a reply in one sync are one event.
	_, err = g.srv.AddNote(ref(apiMR1), open, rae, "Also, ping me.")
	g.must(err)
	_, err = g.srv.AddNote(ref(apiMR1), "", rae, "Second new thread.")
	g.must(err)
	got = g.next()
	wantOnly(t, got, EvMRComment)
	if e := got[EvMRComment][0]; e.Title != "1 new thread and 1 reply on api!1" {
		t.Errorf("mixed event = %+v", e)
	}
}

func TestEventMyOwnCommentsAreNotEchoed(t *testing.T) {
	g := newGLEnv(t, fake.Free)
	discs, err := g.c.Discussions(context.Background(), ref(apiMR1))
	g.must(err)
	var open string
	for _, d := range discs {
		if len(d.Notes) == 1 && !d.Notes[0].System && d.Notes[0].Author.Username == rae {
			open = d.ID
		}
	}
	_, err = g.srv.AddNote(ref(apiMR1), open, meUser, "On it.")
	g.must(err)
	_, err = g.srv.AddNote(ref(apiMR1), "", meUser, "A note to self.")
	g.must(err)
	wantOnly(t, g.next())
}

func TestEventApprovals(t *testing.T) {
	g := newGLEnv(t, fake.Free)
	g.must(g.srv.ApproveAs(ref(apiMR1), meUser))
	wantOnly(t, g.next()) // my own approval is not echoed

	g.must(g.srv.ApproveAs(ref(apiMR1), rae))
	got := g.next()
	wantOnly(t, got, EvMRApproved)
	if e := got[EvMRApproved][0]; e.Title != "api!1 approved by Rae Reviewer ("+rae+")" || e.Severity != events.Success {
		t.Errorf("mr.approved = %+v", e)
	}
}

func TestEventMerge(t *testing.T) {
	t.Run("by a teammate", func(t *testing.T) {
		g := newGLEnv(t, fake.Free)
		g.must(g.srv.MergeMRAs(ref(apiMR1), rae))
		got := g.next()
		if len(got[EvMRMerged]) != 1 || got[EvMRMerged][0].Title != "api!1 merged by Rae Reviewer ("+rae+")" {
			t.Errorf("mr.merged = %+v (all %v)", got[EvMRMerged], keysOf(got))
		}
	})
	t.Run("by me is not echoed", func(t *testing.T) {
		g := newGLEnv(t, fake.Free)
		g.must(g.srv.MergeMRAs(ref(apiMR1), meUser))
		if got := g.next(); len(got[EvMRMerged]) != 0 {
			t.Errorf("my own merge raised %+v", got[EvMRMerged])
		}
	})
}

// TestEventsTitlesNameTheMR keeps the pop-up lines readable: every MR event
// says which MR it is about.
func TestEventsTitlesNameTheMR(t *testing.T) {
	g := newGLEnv(t, fake.Free)
	g.must(g.srv.SetPipelineStatus(ref(apiMR1), "canceled"))
	g.must(g.srv.SetReviewState(ref(apiMR1), rae, "requested_changes"))
	_, err := g.srv.AddNote(ref(apiMR1), "", rae, "Hmm.")
	g.must(err)
	for kind, evs := range g.next() {
		if kind == EvSyncDone {
			continue
		}
		for _, e := range evs {
			if !strings.Contains(e.Title, "api!1") {
				t.Errorf("%s title %q does not name api!1", kind, e.Title)
			}
		}
	}
}

// With labels.workflow.sync unset (ask), a stage change is reported and the
// board is left alone.
func TestSyncReportsUnmovedWorkflowLabel(t *testing.T) {
	g := newGLEnv(t, fake.Free)
	g.must(g.srv.MergeMRAs(ref(apiMR1), rae))
	before := len(g.srv.Requests())
	res := g.h.sync()
	if res[0].Err != "" {
		t.Fatalf("sync: %s", res[0].Err)
	}
	var warned bool
	for _, w := range res[0].Warnings {
		warned = warned || strings.HasPrefix(w, "workflow label not moved: ade-demo-env/tracker#1")
	}
	if !warned {
		t.Errorf("warnings = %q, want one saying tracker#1's workflow label was not moved", res[0].Warnings)
	}
	for _, r := range g.srv.Requests()[before:] {
		if r.Method != "GET" && r.Path != "/api/graphql" {
			t.Errorf("sync wrote with ask: %s %s", r.Method, r.Path)
		}
	}
}
