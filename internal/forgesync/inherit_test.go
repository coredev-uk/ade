package forgesync

import (
	"context"
	"slices"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
)

// seedAppRepoChild makes g/api#8: assigned to me, no labels, a child of the
// closed g/tracker#4, which carries the facets.
func seedAppRepoChild(f *memForge) {
	f.update(func(f *memForge) {
		for i := range f.issues {
			if f.issues[i].Ref == ref("g/tracker#4") {
				f.issues[i].Labels = []string{"type::chore", "app::web", "weight::2"}
			}
		}
		child := issue("g/api#8", "Trim the export worker", nil, "me")
		child.Labels = nil
		child.Description = "Parent: g/tracker#4\n\nImplementation."
		f.issues = append(f.issues, child)
	})
}

func assertParentFacets(t *testing.T, typ string, apps []string, weight *int, triage []string) {
	t.Helper()
	if typ != "chore" || !slices.Equal(apps, []string{"web"}) || weight == nil || *weight != 2 || len(triage) != 0 {
		t.Errorf("type %q apps %v weight %v triage %v, want the parent's chore, web, 2 and no triage", typ, apps, weight, triage)
	}
}

func TestSyncShowsTheParentFacetsOnAnAppRepoIssue(t *testing.T) {
	h := newHarness(t, manifest("demo", "demo", "tracker", "milestones"))
	seed(h.f)
	seedAppRepoChild(h.f)

	it, ok := byRef(h.sync()[0])["g/api#8"]
	if !ok {
		t.Fatal("g/api#8 is not an item")
	}
	assertParentFacets(t, it.Type, it.Apps, it.Weight, it.Triage)
}

func TestRefreshItemShowsTheParentFacetsOnAnAppRepoIssue(t *testing.T) {
	h := newHarness(t, manifest("demo", "demo", "tracker", "milestones"))
	seed(h.f)
	seedAppRepoChild(h.f)
	h.sync()
	f, _ := h.svc.Open(context.Background(), "f", h.app.Config.Forges["f"])
	it, err := h.svc.RefreshItem(context.Background(), manifestOf(t, h), f, ref("g/api#8"))
	if err != nil {
		t.Fatal(err)
	}
	if it.Ref != ref("g/api#8") {
		t.Errorf("ref = %s", it.Ref)
	}
	assertParentFacets(t, it.Type, it.Apps, it.Weight, it.Triage)
}

func TestReviewRowsCarryTheFacetsOfTheirIssueAndItsParent(t *testing.T) {
	h := newHarness(t, manifest("demo", "demo", "tracker", "milestones"))
	seed(h.f)
	seedAppRepoChild(h.f)
	h.f.update(func(f *memForge) {
		for _, mr := range []forge.MR{
			{Ref: ref("g/api!7"), Title: "Trim the worker", State: "opened", SourceBranch: "8-trim-worker", Author: user("rae"), Reviewers: []forge.User{user("me")}},
			{Ref: ref("g/api!8"), Title: "Closes by description", State: "opened", SourceBranch: "trim", Description: "Closes #8", Author: user("rae"), Reviewers: []forge.User{user("me")}},
			{Ref: ref("g/api!9"), Title: "Unknown issue", State: "opened", SourceBranch: "77-other", Author: user("rae"), Reviewers: []forge.User{user("me")}},
		} {
			f.mrs = append(f.mrs, mr)
			f.mrDetail[mr.Ref] = mr
		}
	})

	got := h.sync()[0].ReviewFacets
	for _, r := range []string{"g/api!7", "g/api!8"} {
		f, ok := got[r]
		if !ok {
			t.Fatalf("no facets for %s: %v", r, got)
		}
		assertParentFacets(t, f.Type, f.Apps, f.Weight, nil)
	}
	if _, ok := got["g/api!9"]; ok {
		t.Errorf("facets for an MR whose issue is not loaded: %v", got)
	}
	if _, ok := got["g/api!5"]; ok {
		t.Errorf("facets for an MR that names no issue: %v", got)
	}

	// The facets survive the cache.
	st, _ := h.app.Cache()
	cached, err := Load(st)
	if err != nil || len(cached) != 1 || cached[0].ReviewFacets["g/api!7"].Type != "chore" {
		t.Errorf("cached = %+v, %v", cached, err)
	}
}

// Drafts are not review requests: left out of the queue, and out of the
// events. A draft marked ready is a request, announced once.
func TestReviewsLeaveOutDraftsUntilTheyAreReady(t *testing.T) {
	h := newHarness(t, manifest("demo", "demo", "tracker", "milestones"))
	seed(h.f)
	draft := forge.MR{
		Ref: ref("g/api!6"), Title: "Draft: please review", State: "opened", Draft: true,
		Author: user("rae"), Reviewers: []forge.User{user("me")},
	}
	h.f.update(func(f *memForge) {
		f.mrs = append(f.mrs, draft)
		f.mrDetail[draft.Ref] = draft
	})
	h.sync()
	got := h.listen()

	r := h.sync()[0]
	if len(r.Reviews) != 1 || r.Reviews[0].Ref != ref("g/api!5") {
		t.Fatalf("reviews = %+v, want only api!5", r.Reviews)
	}
	if n := len(kinds(got())[EvReviewAsked]); n != 0 {
		t.Errorf("%d review-requested events for a draft", n)
	}

	h.f.update(func(f *memForge) {
		for i := range f.mrs {
			if f.mrs[i].Ref == draft.Ref {
				f.mrs[i].Draft = false
			}
		}
		d := f.mrDetail[draft.Ref]
		d.Draft = false
		f.mrDetail[draft.Ref] = d
	})
	r = h.sync()[0]
	if len(r.Reviews) != 2 {
		t.Fatalf("reviews = %+v, want api!5 and the ready api!6", r.Reviews)
	}
	ev := kinds(got())[EvReviewAsked]
	if len(ev) != 1 || ev[0].Subject != "g/api!6" {
		t.Errorf("review-requested events = %+v, want one for api!6", ev)
	}
	h.sync()
	if n := len(kinds(got())[EvReviewAsked]); n != 0 {
		t.Errorf("the request was announced again (%d)", n)
	}
}
