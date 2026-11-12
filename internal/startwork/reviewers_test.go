package startwork_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/startwork"
)

const reviewerGroup = "acme/reviewers"

// reviewerHarness is a harness whose manifest asks reviewerGroup for review.
// The group has the starting user, two reviewers, and members that must never
// be asked: a blocked user and a bot.
func reviewerHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, fake.Free)
	h.m.Conventions.MR.ReviewerGroup = reviewerGroup
	for _, u := range []string{"alice", "bob", "carol", "ci-bot"} {
		h.srv.AddUser(u, u)
	}
	h.srv.AddGroup(reviewerGroup,
		fake.GroupMember{Username: "sandbox-dev"},
		fake.GroupMember{Username: "bob"},
		fake.GroupMember{Username: "alice"},
		fake.GroupMember{Username: "carol", State: "blocked"},
		fake.GroupMember{Username: "ci-bot", Bot: true},
	)
	return h
}

func mrStepText(t *testing.T, p startwork.Plan) string {
	t.Helper()
	for _, s := range p.Steps {
		if s.Kind == startwork.StepMR {
			return s.Text
		}
	}
	t.Fatalf("no MR step in %+v", p.Steps)
	return ""
}

func TestDryRunShowsReviewerGroup(t *testing.T) {
	h := reviewerHarness(t)
	before := h.writes()
	plan, res, err := h.env.Start(context.Background(), h.resolve(3), draftApi(), true)
	if err != nil {
		t.Fatal(err)
	}
	want := "request review from acme/reviewers: alice, bob (3 members, you excluded)"
	if text := mrStepText(t, plan); !strings.Contains(text, want) {
		t.Errorf("MR step = %q, want it to contain %q", text, want)
	}
	spec := plan.Targets[0].MR
	if spec.ReviewerGroup != reviewerGroup || len(spec.ReviewerUsers) != 2 || spec.ReviewerMembers != 3 {
		t.Errorf("MR spec reviewers = %+v", spec)
	}
	if len(res.Notes) != 0 || h.writes() != before {
		t.Errorf("dry run: notes %v, writes %d→%d", res.Notes, before, h.writes())
	}
}

func TestStartRequestsReviewFromGroup(t *testing.T) {
	h := reviewerHarness(t)
	_, res, err := h.start(3, draftApi())
	if err != nil {
		t.Fatalf("start: %v\n%+v", err, res.Steps)
	}
	mr := *target(t, res, "api").MR
	got, err := h.srv.MRReviewers(mr)
	if err != nil {
		t.Fatal(err)
	}
	// The template's /assign_reviewer still applies next to the group.
	slices.Sort(got)
	if want := []string{"alice", "bob", "reviewer-rae"}; !slices.Equal(got, want) {
		t.Errorf("reviewers = %v, want %v", got, want)
	}

	es, err := h.app.Audit.Query(audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.Action != "forge.mr.create" {
			continue
		}
		if e.Params["reviewer_group"] != reviewerGroup || !slices.Equal(e.Params["reviewers"].([]any), []any{"alice", "bob"}) {
			t.Errorf("forge.mr.create params = %v, want the group and its reviewers", e.Params)
		}
		return
	}
	t.Error("no forge.mr.create audit entry")
}

func TestUnreadableReviewerGroupWarnsAndStartsWithoutReviewers(t *testing.T) {
	h := reviewerHarness(t)
	h.m.Conventions.MR.ReviewerGroup = "acme/missing"
	plan, res, err := h.start(3, draftApi())
	if err != nil {
		t.Fatalf("start: %v\n%+v", err, res.Steps)
	}
	if !slices.ContainsFunc(plan.Warnings, func(w string) bool {
		return strings.Contains(w, "acme/missing") && strings.Contains(w, "without reviewers")
	}) {
		t.Errorf("warnings = %v, want one about acme/missing", plan.Warnings)
	}
	if text := mrStepText(t, plan); strings.Contains(text, "request review") {
		t.Errorf("MR step = %q, want no review request", text)
	}
	got, _ := h.srv.MRReviewers(*target(t, res, "api").MR)
	if want := []string{"reviewer-rae"}; !slices.Equal(got, want) {
		t.Errorf("reviewers = %v, want only the template's %v", got, want)
	}
}

func TestReviewerGroupOfOnlyYouRequestsNobody(t *testing.T) {
	h := reviewerHarness(t)
	h.srv.AddGroup(reviewerGroup, fake.GroupMember{Username: "sandbox-dev"})
	plan, res, err := h.start(3, draftApi())
	if err != nil {
		t.Fatalf("start: %v\n%+v", err, res.Steps)
	}
	if text := mrStepText(t, plan); !strings.Contains(text, "you are the only member of acme/reviewers") {
		t.Errorf("MR step = %q", text)
	}
	got, _ := h.srv.MRReviewers(*target(t, res, "api").MR)
	if want := []string{"reviewer-rae"}; !slices.Equal(got, want) {
		t.Errorf("reviewers = %v, want %v", got, want)
	}
}

func TestExistingMRKeepsItsReviewers(t *testing.T) {
	h := reviewerHarness(t)
	if _, _, err := h.start(3, draftApi()); err != nil {
		t.Fatal(err)
	}
	h.srv.AddGroup(reviewerGroup, fake.GroupMember{Username: "sandbox-dev"}, fake.GroupMember{Username: "carol"})
	plan, res, err := h.start(3, draftApi())
	if err != nil {
		t.Fatal(err)
	}
	if text := mrStepText(t, plan); strings.Contains(text, "request review") {
		t.Errorf("MR step on re-run = %q", text)
	}
	got, _ := h.srv.MRReviewers(*target(t, res, "api").MR)
	slices.Sort(got)
	if want := []string{"alice", "bob", "reviewer-rae"}; !slices.Equal(got, want) {
		t.Errorf("reviewers after re-run = %v, want %v", got, want)
	}
}

func TestNoReviewerGroupChangesNothing(t *testing.T) {
	h := newHarness(t, fake.Free)
	plan, _, err := h.env.Start(context.Background(), h.resolve(3), draftApi(), true)
	if err != nil {
		t.Fatal(err)
	}
	if text := mrStepText(t, plan); strings.Contains(text, "review") {
		t.Errorf("MR step = %q", text)
	}
}
