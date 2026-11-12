package tui

import (
	"context"
	"os"
	"path/filepath"
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
)

const hierarchyManifest = `
name  = "demo-gl"
forge = "f"

[tracker]
project    = "ade-demo-env/tracker"
iterations = { source = "auto" }

[labels.type]
pattern = "type::{value}"

[labels.app]
pattern = "app::{value}"

[repos.api]
path = "ade-demo-env/api"
app  = ["api"]

[repos.web]
path = "ade-demo-env/web"
app  = ["web"]
`

// syncHierarchy syncs a fake GitLab seeded with the hierarchy cases.
func syncHierarchy(t *testing.T, mutate func(*fake.Server, fake.HierarchyCases)) (forgesync.Result, fake.HierarchyCases) {
	t.Helper()
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	cases := srv.SeedHierarchyCases()
	if mutate != nil {
		mutate(srv, cases)
	}
	dir := t.TempDir()
	manifest := filepath.Join(dir, "demo-gl.ade.toml")
	if err := os.WriteFile(manifest, []byte(hierarchyManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Forges = map[string]config.Forge{"f": {Kind: "gitlab"}}
	cfg.Projects = []config.ProjectRef{{Source: manifest}}
	rec := filepath.Join(dir, "data")
	al, err := audit.Open(rec, "user")
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{Paths: paths.Paths{CacheDir: filepath.Join(dir, "cache"), DataDir: rec}, Config: &cfg, RecordsDir: rec, Audit: al, Bus: events.NewBus()}
	t.Cleanup(func() { a.Close() })
	c := gitlab.New(srv.URL, fake.Token)
	svc := &forgesync.Service{
		App: a, Now: func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) },
		Open: func(context.Context, string, config.Forge) (forge.Forge, error) { return c, nil },
	}
	res, err := svc.Sync(context.Background())
	if err != nil || len(res) != 1 || res[0].Err != "" {
		t.Fatalf("sync: %+v %v", res, err)
	}
	return res[0], cases
}

func sectionOf(rows []homeRow, ref forge.Ref) (homeSection, bool) {
	for _, r := range rows {
		if r.item != nil && r.item.Ref == ref {
			return r.section, true
		}
	}
	return 0, false
}

// Work whose tracker issue is neither mine nor in a sprint is still in My work,
// as the tracker entry with the child under it; the child is not listed again.
func TestHomeListsAChildsTrackerUnderMyWorkWhateverTiesThemTogether(t *testing.T) {
	res, cases := syncHierarchy(t, nil)
	rows := buildHomeRows([]forgesync.Result{res}, hfAll)
	for name, pair := range map[string]fake.HierarchyPair{
		"hierarchy only":         cases.HierarchyOnly,
		"link only":              cases.LinkOnly,
		"hierarchy, tracker out": cases.HierarchyOutside,
		"link, tracker out":      cases.LinkOutside,
	} {
		t.Run(name, func(t *testing.T) {
			if sec, ok := sectionOf(rows, pair.Tracker); !ok || sec != secMine {
				t.Errorf("%s is in section %v (listed: %v), want My work", pair.Tracker, sec, ok)
			}
			if _, ok := sectionOf(rows, pair.Child); ok {
				t.Errorf("%s is listed beside its tracker", pair.Child)
			}
		})
	}
}

// A Done parent is not listed, so the open child of mine stays a row of its own.
func TestHomeKeepsAnOpenChildOfADoneParent(t *testing.T) {
	res, cases := syncHierarchy(t, func(srv *fake.Server, c fake.HierarchyCases) {
		for _, pair := range []fake.HierarchyPair{c.HierarchyOutside, c.LinkOutside} {
			if err := srv.CloseIssue(pair.Tracker); err != nil {
				t.Fatal(err)
			}
		}
	})
	rows := buildHomeRows([]forgesync.Result{res}, hfAll)
	for _, pair := range []fake.HierarchyPair{cases.HierarchyOutside, cases.LinkOutside} {
		if sec, ok := sectionOf(rows, pair.Child); !ok || sec != secMine {
			t.Errorf("%s (child of the closed %s) is in section %v, listed: %v", pair.Child, pair.Tracker, sec, ok)
		}
		if _, ok := sectionOf(rows, pair.Tracker); ok {
			t.Errorf("the Done tracker %s is listed", pair.Tracker)
		}
	}
}

// With a search active the sprint-open section also lists assigned items; its
// guide counts the unassigned ones only.
func TestSprintOpenGuideCountsOnlyUnassignedWhileSearching(t *testing.T) {
	m := model{g: unicodeGlyphs}
	item := func(iid int, assignees ...forge.User) homeRow {
		var it domain.WorkItem
		it.Ref = forge.Ref{Project: "g/tracker", IID: iid, Kind: forge.KindIssue}
		it.Issue.Assignees = assignees
		return homeRow{section: secSprintOpen, item: &it}
	}
	rows := []homeRow{item(1), item(2, forge.User{Username: "kim"}), item(3)}
	if got := m.sectionGuide(&forgesync.Result{}, secSprintOpen, rows); got != "2 unassigned in the tracker · 1 assigned to others" {
		t.Errorf("guide = %q", got)
	}
	if got := m.sectionGuide(&forgesync.Result{}, secSprintOpen, []homeRow{item(1), item(3)}); got != "2 unassigned in the tracker" {
		t.Errorf("guide = %q", got)
	}
}
