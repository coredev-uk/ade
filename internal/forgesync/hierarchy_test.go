package forgesync

import (
	"context"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
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
