package gitlab_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

func graphqlRequests(srv *fake.Server) int {
	n := 0
	for _, r := range srv.Requests() {
		if r.Method == "POST" && r.Path == "/api/graphql" {
			n++
		}
	}
	return n
}

func allMRs(t *testing.T, c *gitlab.Client) []forge.MR {
	t.Helper()
	var all []forge.MR
	for _, p := range []string{apiProj, webProj} {
		mrs, err := c.ListMRs(context.Background(), forge.MRQuery{Project: p, State: "all"})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, mrs...)
	}
	if len(all) < 2 {
		t.Fatalf("the seed has %d merge requests, want several", len(all))
	}
	return all
}

type mrDetail struct {
	Pipeline           *forge.Pipeline
	Approvals          forge.Approvals
	Unresolved         int
	Resolved           int
	Threads            []forge.ThreadState
	ChangesRequestedBy []forge.User
}

func detailOf(m forge.MR) mrDetail {
	return mrDetail{m.Pipeline, m.Approvals, m.Unresolved, m.Resolved, m.Threads, m.ChangesRequestedBy}
}

// The batch must report what four REST calls per merge request report, on
// both licence tiers, in two requests (one per project) instead of 4 per MR.
func TestMRDetailsMatchMR(t *testing.T) {
	forEachTier(t, func(t *testing.T, tier fake.Tier, c *gitlab.Client, srv *fake.Server) {
		ctx := context.Background()
		mrs := allMRs(t, c)
		caps, err := c.Capabilities(ctx, group, "")
		if err != nil {
			t.Fatal(err)
		}
		before := len(srv.Requests())
		got, err := c.MRDetails(ctx, mrs, caps)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(srv.Requests()) - before; n != 2 || graphqlRequests(srv) != 2+1 /* the capability probe */ {
			t.Errorf("%d requests for %d MRs of two projects, want 2 GraphQL", n, len(mrs))
		}
		if len(got) != len(mrs) {
			t.Fatalf("%d details for %d MRs", len(got), len(mrs))
		}
		for i, mr := range mrs {
			if got[i].Ref != mr.Ref || got[i].Title != mr.Title {
				t.Fatalf("MR %d: got %v %q, want %v %q: order or base fields lost", i, got[i].Ref, got[i].Title, mr.Ref, mr.Title)
			}
			want, err := c.MR(ctx, mr.Ref)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(detailOf(got[i]), detailOf(want)) {
				t.Errorf("%s:\n batch %+v\n   MR  %+v", mr.Ref, detailOf(got[i]), detailOf(want))
			}
		}
	})
}

// An instance that cannot answer the query (an older self-managed GitLab)
// costs one failed probe; afterwards every call goes straight to REST.
func TestMRDetailsFallsBackToREST(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithClock(clock), fake.WithoutMRDetails())
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, fake.Token)
	ctx := context.Background()
	mrs := allMRs(t, c)
	caps, err := c.Capabilities(ctx, group, "")
	if err != nil {
		t.Fatal(err)
	}
	probes := graphqlRequests(srv)
	got, err := c.MRDetails(ctx, mrs, caps)
	if err != nil {
		t.Fatal(err)
	}
	for i, mr := range mrs {
		want, err := c.MR(ctx, mr.Ref)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got[i], want) {
			t.Errorf("%s: fallback differs from MR:\n%+v\n%+v", mr.Ref, got[i], want)
		}
	}
	if n := graphqlRequests(srv) - probes; n != 1 {
		t.Errorf("first call sent %d GraphQL requests, want the one failed attempt that marks the instance", n)
	}
	before := graphqlRequests(srv)
	if _, err := c.MRDetails(ctx, mrs, caps); err != nil {
		t.Fatal(err)
	}
	if n := graphqlRequests(srv) - before; n != 0 {
		t.Errorf("second call sent %d GraphQL requests to an instance known not to answer", n)
	}
}

// Over 25 MRs of one project are read in several requests.
func TestMRDetailsChunks(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithClock(clock))
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, fake.Token)
	mrs := allMRs(t, c)
	var many []forge.MR
	for len(many) < 30 {
		many = append(many, mrs[0])
	}
	before := graphqlRequests(srv)
	got, err := c.MRDetails(context.Background(), many, forge.Caps{})
	if err != nil {
		t.Fatal(err)
	}
	if n := graphqlRequests(srv) - before; n != 2 {
		t.Errorf("30 MRs of a project: %d requests, want 2 (25 + 5)", n)
	}
	for i := range got {
		if got[i].Ref != mrs[0].Ref {
			t.Fatalf("got[%d] = %v, want %v", i, got[i].Ref, mrs[0].Ref)
		}
	}
}

// Free reports approvals as unknown (-1), never as "0 required".
func TestMRDetailsApprovalsUnknownWithoutLicence(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithClock(clock))
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, fake.Token)
	got, err := c.MRDetails(context.Background(), allMRs(t, c), forge.Caps{ApprovalRules: false})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		if m.Approvals.Required != -1 {
			t.Errorf("%s: Required = %d, want -1 on an unlicensed instance", m.Ref, m.Approvals.Required)
		}
	}
}

func sleeper() (gitlab.Option, func() []time.Duration) {
	var (
		mu     sync.Mutex
		sleeps []time.Duration
	)
	opt := gitlab.WithSleep(func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		sleeps = append(sleeps, d)
		return nil
	})
	return opt, func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Duration(nil), sleeps...)
	}
}

// A sync stops short of the end of the server's rate-limit window, leaving
// the rest to the user's dialogs: those never wait, and are never the request
// that finds the window closed.
func TestBackgroundRequestsLeaveAReserve(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithRateLimit(10, time.Hour))
	t.Cleanup(srv.Close)
	syncOpt, syncSleeps := sleeper()
	syncC := gitlab.New(srv.URL, fake.Token, syncOpt)
	bg := forge.Background(context.Background())

	for i := range 7 { // 10 - 7 = 3 left: the reserve (30%)
		if _, err := syncC.Me(bg); err != nil {
			t.Fatal(err)
		}
		if s := syncSleeps(); len(s) != 0 {
			t.Fatalf("request %d paused (%v) before the reserve was reached", i+1, s)
		}
	}
	if _, err := syncC.Me(bg); err != nil {
		t.Fatal(err)
	}
	if s := syncSleeps(); len(s) != 1 || s[0] <= 0 {
		t.Fatalf("background request past the reserve slept %v, want one wait for the next window", s)
	}

	// A dialog's client (a different Client, the same host and token) is
	// foreground: no wait, and the server still has room.
	dlgOpt, dlgSleeps := sleeper()
	dlg := gitlab.New(srv.URL, fake.Token, dlgOpt)
	for range 2 {
		if _, err := dlg.Me(context.Background()); err != nil {
			t.Fatalf("foreground request: %v", err)
		}
	}
	if s := dlgSleeps(); len(s) != 0 {
		t.Errorf("foreground requests waited %v", s)
	}
}

// The 429 window belongs to the user, not to one client: a client opened
// after another one was refused waits the same Retry-After.
func TestRateLimitGateIsSharedBetweenClients(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	srv.Throttle(1, "30")
	aOpt, _ := sleeper()
	if _, err := gitlab.New(srv.URL, fake.Token, aOpt).Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	bOpt, bSleeps := sleeper()
	if _, err := gitlab.New(srv.URL, fake.Token, bOpt).Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := bSleeps(); len(s) != 1 || s[0] <= 0 {
		t.Errorf("a second client slept %v inside the closed window, want one gate wait", s)
	}
}

// Servers that send no RateLimit-* headers (most self-managed instances) are
// never paced: only Retry-After applies.
func TestNoRateHeadersNoPacing(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	opt, sleeps := sleeper()
	c := gitlab.New(srv.URL, fake.Token, opt)
	bg := forge.Background(context.Background())
	for range 20 {
		if _, err := c.Me(bg); err != nil {
			t.Fatal(err)
		}
	}
	if s := sleeps(); len(s) != 0 {
		t.Errorf("slept %v against a server without rate-limit headers", s)
	}
}
