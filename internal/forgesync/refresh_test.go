package forgesync

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

func fakeHarness(t *testing.T) (*harness, *fake.Server, *gitlab.Client) {
	t.Helper()
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	h := newHarness(t, manifestFile{"demo-gl", sandboxManifest})
	c := gitlab.New(srv.URL, fake.Token)
	h.svc.Open = func(context.Context, string, config.Forge) (forge.Forge, error) { return c, nil }
	h.svc.Now = func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }
	return h, srv, c
}

func manifestOf(t *testing.T, h *harness) *config.Manifest {
	t.Helper()
	raw, err := os.ReadFile(h.app.Config.Projects[0].Source)
	if err != nil {
		t.Fatal(err)
	}
	m, err := config.ParseManifest(raw, h.app.Config.Projects[0].Source)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func cachedItem(t *testing.T, h *harness, ref string) []byte {
	t.Helper()
	st, err := h.app.Cache()
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := st.DB.QueryRow(`SELECT json FROM work_items WHERE ref = ?`, ref).Scan(&raw); err != nil {
		t.Fatalf("cached %s: %v", ref, err)
	}
	return []byte(raw)
}

// A refreshed item is what a sync would have cached for it, and costs a
// fraction of the requests.
func TestRefreshItemMatchesSync(t *testing.T) {
	h, srv, _ := fakeHarness(t)
	h.sync()
	for _, ref := range []string{"ade-demo-env/tracker#1", "ade-demo-env/tracker#3", "ade-demo-env/tracker#8"} {
		want := cachedItem(t, h, ref)
		before := len(srv.Requests())
		m := manifestOf(t, h)
		f, _ := h.svc.Open(context.Background(), "f", config.Forge{})
		got, err := h.svc.RefreshItem(context.Background(), m, f, mustRef(t, ref))
		if err != nil {
			t.Fatal(err)
		}
		if n := len(srv.Requests()) - before; n > 12 {
			t.Errorf("%s: %d requests for one item, a sync of the whole project takes ~25", ref, n)
		}
		b, _ := json.Marshal(got)
		if string(b) != string(want) {
			t.Errorf("%s: refreshed item differs from the synced one:\n got %s\nwant %s", ref, b, want)
		}
		if string(cachedItem(t, h, ref)) != string(want) {
			t.Errorf("%s: the cached row changed", ref)
		}
	}
}

// What start work leaves behind (a child issue linked to the item, an MR
// closing it) shows up in the cached item after one refresh, with no sync,
// and nothing else in the cache moves.
func TestRefreshItemShowsNewChildAndMR(t *testing.T) {
	h, _, c := fakeHarness(t)
	before := h.sync()[0]
	ctx := context.Background()
	m := manifestOf(t, h)
	parent := forge.Ref{Project: "ade-demo-env/tracker", IID: 8, Kind: forge.KindIssue}

	item0 := byRef(before)[parent.String()]
	if len(item0.Children) != 0 || len(item0.MRs) != 0 {
		t.Fatalf("tracker#8 starts with children %d, MRs %d: pick another fixture", len(item0.Children), len(item0.MRs))
	}

	child, err := c.CreateIssue(ctx, "ade-demo-env/api", forge.IssueDraft{Title: "Child of 8", Description: "Parent: " + parent.String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.LinkIssues(ctx, parent, child.Ref, "relates_to"); err != nil {
		t.Fatal(err)
	}
	branch := "8-child"
	if _, err := c.CreateBranch(ctx, "ade-demo-env/api", branch, "main"); err != nil {
		t.Fatal(err)
	}
	mr, err := c.CreateMR(ctx, forge.MRDraft{Project: "ade-demo-env/api", Source: branch, Target: "main", Title: "Child of 8", Description: "Closes #" + strconv.Itoa(child.Ref.IID), Draft: true})
	if err != nil {
		t.Fatal(err)
	}

	got, err := h.svc.RefreshItem(ctx, m, c, parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Children) != 1 || got.Children[0].Ref != child.Ref {
		t.Fatalf("children after refresh = %+v, want %v", got.Children, child.Ref)
	}
	if len(got.MRs) != 1 || got.MRs[0].Ref != mr.Ref {
		t.Fatalf("MRs after refresh = %+v, want %v", got.MRs, mr.Ref)
	}

	st, _ := h.app.Cache()
	results, err := Load(st)
	if err != nil {
		t.Fatal(err)
	}
	after := byRef(results[0])
	if n := len(after[parent.String()].MRs); n != 1 {
		t.Errorf("cached item has %d MRs", n)
	}
	if len(after) != len(byRef(before)) {
		t.Errorf("refresh changed the item count: %d → %d", len(byRef(before)), len(after))
	}
	if !results[0].SyncedAt.Equal(before.SyncedAt) {
		t.Errorf("a refresh must not pose as a sync: SyncedAt %v → %v", before.SyncedAt, results[0].SyncedAt)
	}
	if results[0].Sprints == nil || results[0].Me.Username == "" {
		t.Errorf("sync state lost: %+v", results[0])
	}
}

func TestRefreshItemAddsAnUncachedItem(t *testing.T) {
	h, _, c := fakeHarness(t)
	h.sync()
	ctx := context.Background()
	st, _ := h.app.Cache()
	if _, err := st.DB.Exec(`DELETE FROM work_items WHERE ref = ?`, "ade-demo-env/tracker#2"); err != nil {
		t.Fatal(err)
	}
	got, err := h.svc.RefreshItem(ctx, manifestOf(t, h), c, forge.Ref{Project: "ade-demo-env/tracker", IID: 2, Kind: forge.KindIssue})
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref.IID != 2 || len(cachedItem(t, h, "ade-demo-env/tracker#2")) == 0 {
		t.Errorf("item not added: %+v", got.Ref)
	}
}

func TestRefreshItemReportsAMissingIssue(t *testing.T) {
	h, _, c := fakeHarness(t)
	h.sync()
	_, err := h.svc.RefreshItem(context.Background(), manifestOf(t, h), c, forge.Ref{Project: "ade-demo-env/tracker", IID: 9999, Kind: forge.KindIssue})
	if err == nil {
		t.Fatal("a refresh of an issue that does not exist succeeded")
	}
}

func mustRef(t *testing.T, s string) forge.Ref {
	t.Helper()
	r, err := forge.ParseRef(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
