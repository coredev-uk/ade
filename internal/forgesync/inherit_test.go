package forgesync

import (
	"context"
	"maps"
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

// A manifest that spells the tracker path in another case than the forge
// ("g/Tracker" for g/tracker) still finds the tracker parent of my issue in a
// code repo: the child shows under it with its facets, though the parent is
// not mine and outside the sprint window. GitLab paths are case-insensitive.
func TestSyncFindsTheParentWhenTheManifestSpellsTheTrackerInAnotherCase(t *testing.T) {
	h := newHarness(t, manifest("demo", "demo", "Tracker", "milestones"))
	seed(h.f)
	h.f.update(func(f *memForge) {
		parent := issue("g/tracker#7", "Trim exports", nil)
		parent.Labels = []string{"type::chore", "app::web", "weight::2"}
		child := issue("g/api#8", "Trim the export worker", nil, "me")
		child.Labels = nil
		f.issues = append(f.issues, parent, child)
		f.links[ref("g/api#8")] = []forge.Link{{Ref: ref("g/tracker#7"), Type: "relates_to"}}
	})

	items := byRef(h.sync()[0])
	if _, own := items["g/api#8"]; own {
		t.Fatalf("g/api#8 is an entry of its own, want it under g/tracker#7: %v", slices.Collect(maps.Keys(items)))
	}
	it, ok := items["g/tracker#7"]
	if !ok || len(it.Children) != 1 || it.Children[0].Ref != ref("g/api#8") {
		t.Fatalf("g/tracker#7 = %+v (found %v), want it with the child g/api#8", it.Children, ok)
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

// A teammate's issue whose tracker parent is outside the sprint window still
// gives its review row the parent's facets, and the parent is not a work item.
func TestReviewRowsReadAParentOutsideTheWindow(t *testing.T) {
	h := newHarness(t, manifest("demo", "demo", "tracker", "milestones"))
	seed(h.f)
	h.f.update(func(f *memForge) {
		for i := range f.issues {
			if f.issues[i].Ref == ref("g/tracker#5") {
				f.issues[i].Labels = []string{"type::chore", "app::web", "weight::2"}
			}
		}
		child := issue("g/api#10", "Teammate's chore", nil, "rae")
		child.Labels = nil
		child.Description = "Parent: g/tracker#5"
		f.issues = append(f.issues, child)
		mr := forge.MR{Ref: ref("g/api!10"), Title: "Chore", State: "opened", SourceBranch: "10-chore", Author: user("rae"), Reviewers: []forge.User{user("me")}}
		f.mrs = append(f.mrs, mr)
		f.mrDetail[mr.Ref] = mr
	})

	r := h.sync()[0]
	f, ok := r.ReviewFacets["g/api!10"]
	if !ok {
		t.Fatalf("no facets for g/api!10: %v", r.ReviewFacets)
	}
	assertParentFacets(t, f.Type, f.Apps, f.Weight, nil)
	if _, ok := byRef(r)["g/tracker#5"]; ok {
		t.Error("the parent read for the facets became a work item")
	}
}
