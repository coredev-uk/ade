package sprintmove_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/sprintmove"
)

const (
	tracker = fake.GroupPath + "/tracker"
	apiProj = fake.GroupPath + "/api"
)

var now = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

func iref(project string, iid int) forge.Ref {
	return forge.Ref{Project: project, IID: iid, Kind: forge.KindIssue}
}

type harness struct {
	t   *testing.T
	c   *gitlab.Client
	srv *fake.Server
	app *app.App
	m   *config.Manifest
	bus <-chan events.Event
}

func newHarness(t *testing.T, tier fake.Tier, src config.IterationsSource) *harness {
	t.Helper()
	srv := fake.New(tier, fake.WithClock(now))
	t.Cleanup(srv.Close)
	log, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{Audit: log, Bus: events.NewBus(), Paths: paths.Paths{CacheDir: t.TempDir()}}
	t.Cleanup(func() { a.Close() })
	bus, cancel := a.Bus.Subscribe(16)
	t.Cleanup(cancel)
	return &harness{
		t: t, srv: srv, c: gitlab.New(srv.URL, fake.Token, gitlab.WithClock(func() time.Time { return now })), app: a, bus: bus,
		m: &config.Manifest{Name: "demo", Forge: "gl", Tracker: config.Tracker{Project: tracker, Iterations: src}},
	}
}

func (h *harness) target(iid int) *sprintmove.Target {
	h.t.Helper()
	tg, err := sprintmove.ResolveWith(context.Background(), h.c, h.m, iref(tracker, iid), now)
	if err != nil {
		h.t.Fatal(err)
	}
	return tg
}

func (h *harness) move(iid int, to string, ch sprintmove.Children) []sprintmove.Result {
	h.t.Helper()
	tg := h.target(iid)
	dest, err := tg.Find(to)
	if err != nil {
		h.t.Fatal(err)
	}
	plans, err := tg.Plans(dest, ch)
	if err != nil {
		h.t.Fatal(err)
	}
	res, err := sprintmove.Apply(context.Background(), h.c, h.app, "demo", "", plans)
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

func (h *harness) milestone(ref forge.Ref) string {
	h.t.Helper()
	is, err := h.c.Issue(context.Background(), ref)
	if err != nil {
		h.t.Fatal(err)
	}
	if is.Milestone == nil {
		return ""
	}
	return is.Milestone.Title
}

func (h *harness) audits() []audit.Entry {
	h.t.Helper()
	es, err := h.app.Audit.Query(audit.Filter{Action: sprintmove.Action})
	if err != nil {
		h.t.Fatal(err)
	}
	return es
}

func TestMilestonesMoveTheIssueAndItsChildToTheNextSprintAndBack(t *testing.T) {
	h := newHarness(t, fake.Free, config.IterationsSource{Source: "milestones"})
	tg := h.target(1) // Sprint 42, child api#1 in Sprint 42
	if cur := tg.Current(); cur == nil || cur.Title != "Sprint 42" {
		t.Fatalf("current = %+v, want Sprint 42", cur)
	}
	if len(tg.Children) != 1 {
		t.Fatalf("children = %d, want 1", len(tg.Children))
	}
	next, err := tg.Next()
	if err != nil || next.Title != "Sprint 43" {
		t.Fatalf("next = %+v, %v; want Sprint 43", next, err)
	}

	// A dry run (plans only) changes nothing.
	plans, err := tg.Plans(next, sprintmove.ChildrenSame)
	if err != nil || len(plans) != 2 || !plans[1].Child || plans[0].Mode != sprintmove.ModeMilestone {
		t.Fatalf("plans = %+v, %v", plans, err)
	}
	if got := h.milestone(iref(tracker, 1)); got != "Sprint 42" {
		t.Fatalf("planning wrote: milestone = %q", got)
	}

	res := h.move(1, "next", sprintmove.ChildrenSame)
	if len(res) != 2 || !res[0].Applied || !res[1].Applied {
		t.Fatalf("results = %+v", res)
	}
	for _, ref := range []forge.Ref{iref(tracker, 1), iref(apiProj, 1)} {
		if got := h.milestone(ref); got != "Sprint 43" {
			t.Errorf("%s milestone = %q, want Sprint 43", ref, got)
		}
	}
	es := h.audits()
	if len(es) != 2 || es[0].Target != tracker+"#1" || es[0].Outcome != "ok" ||
		es[0].Params["from"] != "Sprint 42" || es[0].Params["to"] != "Sprint 43" || es[0].Params["mode"] != sprintmove.ModeMilestone || es[1].Params["child"] != true {
		t.Errorf("audit = %+v", es)
	}
	// The move is announced at once, once, for the tracker issue only.
	var evs []events.Event
	for len(h.bus) > 0 {
		evs = append(evs, <-h.bus)
	}
	if len(evs) != 1 || evs[0].Kind != forgesync.EvIterationMoved || evs[0].Subject != tracker+"#1" || !strings.Contains(evs[0].Title, "Sprint 43") {
		t.Errorf("events = %+v", evs)
	}

	// Moving again is a no-op: nothing written, audited or announced.
	again := h.move(1, "Sprint 43", sprintmove.ChildrenSame)
	if again[0].Applied || again[1].Applied || len(h.audits()) != 2 || len(h.bus) != 0 {
		t.Errorf("repeat wrote: %+v, audits %d, events %d", again, len(h.audits()), len(h.bus))
	}

	// And back, by title.
	h.move(1, "sprint 42", sprintmove.ChildrenSame)
	for _, ref := range []forge.Ref{iref(tracker, 1), iref(apiProj, 1)} {
		if got := h.milestone(ref); got != "Sprint 42" {
			t.Errorf("%s milestone after moving back = %q", ref, got)
		}
	}
}

func TestChildInAnotherSprintStaysUnlessAskedFor(t *testing.T) {
	h := newHarness(t, fake.Free, config.IterationsSource{Source: "milestones"})
	ctx := context.Background()
	ms, err := h.c.Milestones(ctx, fake.GroupPath)
	if err != nil {
		t.Fatal(err)
	}
	var s41 forge.Milestone
	for _, m := range ms {
		if m.Title == "Sprint 41" {
			s41 = m
		}
	}
	if err := h.c.SetMilestone(ctx, iref(apiProj, 1), s41.ID); err != nil {
		t.Fatal(err)
	}

	plans, err := h.target(1).Plans(mustNext(t, h.target(1)), sprintmove.ChildrenSame)
	if err != nil || len(plans) != 1 || len(plans[0].Notes) != 1 || !strings.Contains(plans[0].Notes[0], "Sprint 41") {
		t.Fatalf("plans = %+v, %v; want the tracker issue only, with a note about the child", plans, err)
	}
	if plans, _ = h.target(1).Plans(mustNext(t, h.target(1)), sprintmove.ChildrenAll); len(plans) != 2 {
		t.Fatalf("with every child: %d plans, want 2", len(plans))
	}
	if plans, _ = h.target(1).Plans(mustNext(t, h.target(1)), sprintmove.ChildrenNone); len(plans) != 1 || len(plans[0].Notes) != 0 {
		t.Fatalf("no children: %+v", plans)
	}
	h.move(1, "next", sprintmove.ChildrenAll)
	if got := h.milestone(iref(apiProj, 1)); got != "Sprint 43" {
		t.Errorf("child milestone = %q, want Sprint 43", got)
	}
}

func mustNext(t *testing.T, tg *sprintmove.Target) domain.Sprint {
	t.Helper()
	next, err := tg.Next()
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestPremiumMovesTheIteration(t *testing.T) {
	h := newHarness(t, fake.Premium, config.IterationsSource{Source: "gitlab-iterations", Cadence: "Delivery"})
	ctx := context.Background()
	tg := h.target(1)
	cur := tg.Current()
	if cur == nil || cur.Source != "gitlab-iterations" {
		t.Fatalf("current = %+v, want an iteration", cur)
	}
	next, err := tg.Next()
	if err != nil || next.Start.Compare(cur.Due.Time) <= 0 {
		t.Fatalf("next = %+v, %v; want an iteration after %s", next, err, cur.Due)
	}
	res := h.move(1, "", sprintmove.ChildrenSame)
	if len(res) != 2 || res[0].Plan.Mode != sprintmove.ModeIteration || !res[0].Applied || !res[1].Applied {
		t.Fatalf("results = %+v", res)
	}
	for _, ref := range []forge.Ref{iref(tracker, 1), iref(apiProj, 1)} {
		is, err := h.c.Issue(ctx, ref)
		if err != nil || is.Iteration == nil || is.Iteration.Start.String() != next.Start.String() {
			t.Errorf("%s iteration = %+v, %v; want the one starting %s", ref, is.Iteration, err, next.Start)
		}
	}
	// Back by key.
	h.move(1, cur.Key, sprintmove.ChildrenSame)
	if is, _ := h.c.Issue(ctx, iref(tracker, 1)); is.Iteration == nil || is.Iteration.Start.String() != cur.Start.String() {
		t.Errorf("iteration after moving back = %+v", is.Iteration)
	}
}

func TestNoNextSprintSaysWhatToCreate(t *testing.T) {
	// tracker#7 is in Sprint 43, the last milestone.
	h := newHarness(t, fake.Free, config.IterationsSource{Source: "milestones"})
	_, err := h.target(7).Next()
	if err == nil {
		t.Fatal("Next succeeded with no sprint after Sprint 43")
	}
	for _, want := range []string{"after Sprint 43 (ends 2026-11-27)", "create the next milestone", fake.GroupPath, "creates no sprints"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if _, err := h.target(7).Find("Sprint 99"); err == nil || !strings.Contains(err.Error(), "no sprint \"Sprint 99\"") {
		t.Errorf("Find(unknown) = %v", err)
	}
}

func TestWorkLeftInAPastSprintGoesToTheCurrentOne(t *testing.T) {
	h := newHarness(t, fake.Free, config.IterationsSource{Source: "milestones"})
	ctx := context.Background()
	ms, _ := h.c.Milestones(ctx, fake.GroupPath)
	for _, m := range ms {
		if m.Title == "Sprint 41" {
			if err := h.c.SetMilestone(ctx, iref(tracker, 3), m.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if next := mustNext(t, h.target(3)); next.Title != "Sprint 42" {
		t.Errorf("next of work left in Sprint 41 = %s, want the current Sprint 42", next.Title)
	}
}

func TestClosedIssueStaysInItsSprint(t *testing.T) {
	h := newHarness(t, fake.Free, config.IterationsSource{Source: "milestones"})
	tg := h.target(9) // closed
	if _, err := tg.Plans(mustNext(t, tg), sprintmove.ChildrenSame); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("Plans(closed) = %v, want a refusal", err)
	}
}

func TestLabelSprintsSwapTheLabel(t *testing.T) {
	h := newHarness(t, fake.Free, config.IterationsSource{Source: "labels", Pattern: "Sprint::{n}", Start: "2026-10-19", Length: "2w"})
	ctx := context.Background()
	// Sprint::2 is 2 Nov–15 Nov; Sprint::3 exists once a label names it.
	if err := h.c.SetLabels(ctx, iref(tracker, 1), []string{"Sprint::2"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.c.SetLabels(ctx, iref(tracker, 2), []string{"Sprint::3"}, nil); err != nil {
		t.Fatal(err)
	}
	res := h.move(1, "next", sprintmove.ChildrenNone)
	if len(res) != 1 || res[0].Plan.Mode != sprintmove.ModeLabel || !slices.Equal(res[0].Plan.Add, []string{"Sprint::3"}) || !slices.Equal(res[0].Plan.Remove, []string{"Sprint::2"}) {
		t.Fatalf("results = %+v", res)
	}
	ls, _ := h.srv.IssueLabels(iref(tracker, 1))
	if !slices.Contains(ls, "Sprint::3") || slices.Contains(ls, "Sprint::2") || !slices.Contains(ls, "type::story") {
		t.Errorf("labels = %v, want Sprint::3 instead of Sprint::2 and the others kept", ls)
	}
	// With no later label there is nothing to move to.
	if _, err := h.target(1).Next(); err == nil || !strings.Contains(err.Error(), "Sprint::{n}") {
		t.Errorf("Next after the last label = %v, want the label fix", err)
	}
}

func TestFailedWriteIsAuditedAndStops(t *testing.T) {
	h := newHarness(t, fake.Free, config.IterationsSource{Source: "milestones"})
	tg := h.target(1)
	next := mustNext(t, tg)
	good, _ := tg.Plans(next, sprintmove.ChildrenNone)
	bad := good[0]
	bad.Ref = iref(tracker, 999)
	res, err := sprintmove.Apply(context.Background(), h.c, h.app, "demo", "", []sprintmove.Plan{bad, good[0]})
	if err == nil || len(res) != 1 || res[0].Applied || res[0].Error == "" {
		t.Fatalf("res = %+v, err = %v; want one failed result and the error", res, err)
	}
	if got := h.milestone(iref(tracker, 1)); got != "Sprint 42" {
		t.Errorf("the plan after the failure was applied: %q", got)
	}
	if es := h.audits(); len(es) != 1 || es[0].Outcome != "error" || es[0].Error == "" {
		t.Errorf("audit = %+v, want one error entry", es)
	}
	if len(h.bus) != 0 {
		t.Error("a failed move was announced")
	}
}
