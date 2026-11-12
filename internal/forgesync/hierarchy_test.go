package forgesync

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// hierarchyHarness syncs a fake GitLab seeded with the four ways a child can
// belong to a tracker issue without a Parent: line.
func hierarchyHarness(t *testing.T, opts ...fake.Option) (*harness, *fake.Server, fake.HierarchyCases) {
	t.Helper()
	srv := fake.New(fake.Free, opts...)
	t.Cleanup(srv.Close)
	cases := srv.SeedHierarchyCases()
	h := newHarness(t, manifestFile{"demo-gl", sandboxManifest})
	c := gitlab.New(srv.URL, fake.Token)
	h.svc.Open = func(context.Context, string, config.Forge) (forge.Forge, error) { return c, nil }
	h.svc.Now = func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }
	return h, srv, cases
}

func namedPairs(c fake.HierarchyCases) map[string]fake.HierarchyPair {
	return map[string]fake.HierarchyPair{
		"hierarchy only":         c.HierarchyOnly,
		"link only":              c.LinkOnly,
		"hierarchy, tracker out": c.HierarchyOutside,
		"link, tracker out":      c.LinkOutside,
	}
}

// A child that belongs to a tracker issue through the hierarchy or a link is
// one My-work entry: the tracker's, with the child under it.
func TestSyncGroupsAChildUnderItsTrackerWhateverTiesThemTogether(t *testing.T) {
	h, _, cases := hierarchyHarness(t)
	res := h.sync()
	if len(res) != 1 || res[0].Err != "" {
		t.Fatalf("results = %+v", res)
	}
	items := byRef(res[0])
	for name, pair := range namedPairs(cases) {
		t.Run(name, func(t *testing.T) {
			if _, own := items[pair.Child.String()]; own {
				t.Errorf("%s is listed as its own entry", pair.Child)
			}
			tr, ok := items[pair.Tracker.String()]
			if !ok {
				t.Fatalf("%s is missing from %v", pair.Tracker, refs(res[0]))
			}
			found := false
			for _, k := range tr.Children {
				found = found || k.Ref == pair.Child
			}
			if !found {
				t.Errorf("%s has children %v, want %s", pair.Tracker, tr.Children, pair.Child)
			}
		})
	}
}

// Without a hierarchy the link still groups, and a hierarchy-only child is
// what it always was: an entry of its own.
func TestSyncWithoutHierarchyFallsBackToLinks(t *testing.T) {
	h, _, cases := hierarchyHarness(t, fake.WithoutHierarchy())
	res := h.sync()
	if len(res) != 1 || res[0].Err != "" {
		t.Fatalf("results = %+v", res)
	}
	items := byRef(res[0])
	for _, pair := range []fake.HierarchyPair{cases.LinkOnly, cases.LinkOutside} {
		if _, own := items[pair.Child.String()]; own {
			t.Errorf("%s is listed as its own entry", pair.Child)
		}
		if _, ok := items[pair.Tracker.String()]; !ok {
			t.Errorf("%s is missing", pair.Tracker)
		}
	}
	if _, own := items[cases.HierarchyOnly.Child.String()]; !own {
		t.Errorf("%s has no readable parent and should stand alone", cases.HierarchyOnly.Child)
	}
}

// Refreshing the tracker keeps a hierarchy-only child, and finds one a sync
// has not seen.
func TestRefreshItemKeepsTheHierarchyEdge(t *testing.T) {
	h, srv, cases := hierarchyHarness(t)
	h.sync()
	f, _ := h.svc.Open(context.Background(), "f", config.Forge{})
	m := manifestOf(t, h)
	for _, pair := range []fake.HierarchyPair{cases.HierarchyOnly, cases.HierarchyOutside} {
		got, err := h.svc.RefreshItem(context.Background(), m, f, pair.Tracker)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Children) != 1 || got.Children[0].Ref != pair.Child {
			t.Errorf("%s children after a refresh = %v, want %s", pair.Tracker, got.Children, pair.Child)
		}
	}
	// A hierarchy child added after the sync is found by the refresh.
	late := cases.LinkOnly
	if err := srv.SetParent(late.Child, cases.HierarchyOnly.Tracker); err != nil {
		t.Fatal(err)
	}
	got, err := h.svc.RefreshItem(context.Background(), m, f, cases.HierarchyOnly.Tracker)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Children) != 2 {
		t.Errorf("children = %v, want the synced child and %s", got.Children, late.Child)
	}
}

const webProject = "ade-demo-env/web"

// addHierarchyChild adds a code-repo issue under tracker in the hierarchy
// only: no link, no Parent: line.
func addHierarchyChild(t *testing.T, srv *fake.Server, tracker forge.Ref, spec fake.IssueSpec) forge.Ref {
	t.Helper()
	spec.Project = webProject
	child, err := srv.AddIssue(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.SetParent(child, tracker); err != nil {
		t.Fatal(err)
	}
	return child
}

func childRefs(wi domain.WorkItem) []forge.Ref {
	var out []forge.Ref
	for _, k := range wi.Children {
		out = append(out, k.Ref)
	}
	return out
}

// A closed child, and one nobody holds, have no link or Parent: line: only
// the hierarchy ties them to the tracker, for a sync as for a refresh.
func TestSyncAndRefreshKeepHierarchyChildrenThatAreClosedOrNobodys(t *testing.T) {
	h, srv, cases := hierarchyHarness(t)
	tracker := cases.HierarchyOnly.Tracker
	closedAt := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	closed := addHierarchyChild(t, srv, tracker, fake.IssueSpec{Title: "Tailwind: old config", Assigned: true, Closed: &closedAt})
	unowned := addHierarchyChild(t, srv, tracker, fake.IssueSpec{Title: "Tailwind: docs"})
	want := []forge.Ref{cases.HierarchyOnly.Child, closed, unowned}

	check := func(when string, wi domain.WorkItem) {
		t.Helper()
		got := childRefs(wi)
		for _, w := range want {
			if !slices.Contains(got, w) {
				t.Errorf("%s: %s lost child %s (have %v)", when, tracker, w, got)
			}
		}
	}
	for range 2 { // the second sync has a previous result to compare with
		res := h.sync()
		if len(res) != 1 || res[0].Err != "" {
			t.Fatalf("results = %+v", res)
		}
		items := byRef(res[0])
		check("sync", items[tracker.String()])
		for _, k := range []forge.Ref{closed, unowned} {
			if _, own := items[k.String()]; own {
				t.Errorf("%s is listed as its own entry", k)
			}
		}
	}
	f, _ := h.svc.Open(context.Background(), "f", config.Forge{})
	got, err := h.svc.RefreshItem(context.Background(), manifestOf(t, h), f, tracker)
	if err != nil {
		t.Fatal(err)
	}
	check("refresh", got)
}

// A parent that is Done is not listed, so a child of mine that is still open
// stays an entry of its own instead of vanishing with it.
func TestSyncKeepsAnOpenChildOfADoneTrackerAsItsOwnEntry(t *testing.T) {
	h, srv, cases := hierarchyHarness(t)
	pair := cases.HierarchyOutside
	if err := srv.CloseIssue(pair.Tracker); err != nil {
		t.Fatal(err)
	}
	res := h.sync()
	if len(res) != 1 || res[0].Err != "" {
		t.Fatalf("results = %+v", res)
	}
	items := byRef(res[0])
	child, ok := items[pair.Child.String()]
	if !ok || !child.Assigned || child.Stage == domain.StageDone {
		t.Fatalf("the open child of a closed tracker must be listed: %v", refs(res[0]))
	}
	if tr, ok := items[pair.Tracker.String()]; ok && tr.Stage != domain.StageDone {
		t.Errorf("closed tracker has stage %v", tr.Stage)
	}
}

// parenter wraps a client, failing Parents with err once set.
type parenter struct {
	*gitlab.Client
	err error
}

func (p *parenter) Parents(ctx context.Context, refs []forge.Ref) (map[forge.Ref]forge.Ref, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.Client.Parents(ctx, refs)
}

// A failing hierarchy read is not a missing hierarchy: the edges of the last
// sync stay, with a warning. Only ErrUnavailable is silent, and drops them.
func TestSyncKeepsCachedParentsWhenTheHierarchyReadFails(t *testing.T) {
	h, srv, cases := hierarchyHarness(t)
	flaky := &parenter{Client: gitlab.New(srv.URL, fake.Token)}
	h.svc.Open = func(context.Context, string, config.Forge) (forge.Forge, error) { return flaky, nil }
	pair := cases.HierarchyOnly
	h.sync()

	flaky.err = errors.New("500 Internal Server Error")
	res := h.sync()[0]
	if res.Err != "" {
		t.Fatalf("a failing hierarchy read must not fail the sync: %s", res.Err)
	}
	if !slices.Contains(childRefs(byRef(res)[pair.Tracker.String()]), pair.Child) {
		t.Errorf("%s lost %s after a failed read", pair.Tracker, pair.Child)
	}
	if !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "issue hierarchy") }) {
		t.Errorf("no warning: %v", res.Warnings)
	}

	flaky.err = forge.ErrUnavailable
	res = h.sync()[0]
	if slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "issue hierarchy") }) {
		t.Errorf("an instance without a hierarchy is not worth a warning: %v", res.Warnings)
	}
	if _, own := byRef(res)[pair.Child.String()]; !own {
		t.Errorf("%s has no readable parent and should stand alone", pair.Child)
	}
}
