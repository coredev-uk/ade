package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/records"
)

// cacheItem writes one work item into the cache the way a sync does.
func (e *cliEnv) cacheItem(wi domain.WorkItem) {
	e.t.Helper()
	st, err := e.app.Cache()
	if err != nil {
		e.t.Fatal(err)
	}
	state, _ := json.Marshal(forgesync.Result{Project: "demo", Forge: "sandbox", Tracker: wi.Ref.Project, SyncedAt: time.Now().Add(-3 * time.Minute)})
	b, _ := json.Marshal(wi)
	if _, err := st.DB.Exec(`INSERT INTO work_items (project, ref, json) VALUES ('demo', ?, ?)`, wi.Ref.String(), string(b)); err != nil {
		e.t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO sync_state (project, json) VALUES ('demo', ?)`, string(state)); err != nil {
		e.t.Fatal(err)
	}
}

func watchedItem() domain.WorkItem {
	tr := forge.Ref{Project: "acme/tracker", IID: 15, Kind: forge.KindIssue}
	child := forge.Ref{Project: "acme/api", IID: 3, Kind: forge.KindIssue}
	return domain.WorkItem{
		Project: "demo", Ref: tr,
		Issue:    forge.Issue{Ref: tr, Title: "Rate-limit the export endpoint", State: "opened", Description: "Exports hammer the API.\n\n- [ ] add limiter\n\n## Acceptance\n- [x] 429 over the limit"},
		Children: []forge.Issue{{Ref: child, Title: "Limiter middleware"}},
		MRs: []forge.MR{{
			Ref: forge.Ref{Project: "acme/api", IID: 5, Kind: forge.KindMR}, State: "opened", SourceBranch: "3-limiter",
			Pipeline: &forge.Pipeline{Status: "failed"}, Unresolved: 2,
		}},
		Stage: domain.StageCI, Court: domain.CourtYou, Reason: "pipeline failed", Type: "feature", Apps: []string{"api"},
	}
}

func (e *cliEnv) watch(args ...string) (string, error) {
	return e.item(append(args, "--watch")...)
}

func TestItemWatchOffATerminalPrintsTheViewOnce(t *testing.T) {
	e := newCLIEnv(t)
	e.app.RecordsDir = t.TempDir()
	wi := watchedItem()
	e.cacheItem(wi)
	rs := records.New(e.app.RecordsDir)
	if _, _, err := rs.WriteSnapshot("sandbox", wi, nil, 5); err != nil {
		t.Fatal(err)
	}
	if err := rs.WriteNotes("sandbox", wi.Ref, "ask support about the limit"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COLUMNS", "50")

	out, err := e.watch("acme/tracker#15")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		" acme/tracker#15  Rate-limit the export endpoint\n",
		"◉──○──○  ci\n",
		"you · ✕ pipeline failed\n",
		"┃ api#3  Limiter middleware\n",
		"┃ !5 · open · 3-limiter\n",
		"┃ ✕ pipeline failed · ✉ 2 unresolved threads\n",
		" Description ",
		" [ ] add limiter\n",
		" Acceptance ",
		" [x] 429 over the limit\n",
		" ask support about the limit\n",
		" synced 3m ago\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("output carries terminal escapes off a terminal:\n%q", out)
	}
	for i, l := range strings.Split(out, "\n") {
		if n := len([]rune(l)); n > 50 {
			t.Errorf("line %d is %d cells at COLUMNS=50: %q", i, n, l)
		}
	}

	// A child issue and an MR ref show the same item.
	for _, r := range []string{"acme/api#3", "acme/api!5"} {
		if got, err := e.watch(r); err != nil || got != out {
			t.Errorf("--watch %s: err %v, output differs from the tracker's:\n%s", r, err, got)
		}
	}
}

func TestItemWatchUnknownRefAndBadFlagCombinations(t *testing.T) {
	e := newCLIEnv(t)
	e.app.RecordsDir = t.TempDir()
	e.cacheItem(watchedItem())

	if _, err := e.watch("acme/tracker#99"); err == nil || !strings.Contains(err.Error(), "acme/tracker#99") || !strings.Contains(err.Error(), "ade sync") {
		t.Errorf("unknown ref: err = %v", err)
	}
	if _, err := e.item("acme/tracker#15", "--watch", "--edit"); err == nil || !strings.Contains(err.Error(), "--watch cannot be combined") {
		t.Errorf("--watch --edit: err = %v", err)
	}
	asJSON(t)
	_, err := e.watch("acme/tracker#15")
	jsonOut = false
	if err == nil || !strings.Contains(err.Error(), "--watch cannot be combined") {
		t.Errorf("--watch --json: err = %v", err)
	}
}

func TestItemWithoutWatchIsUnchanged(t *testing.T) {
	e := newCLIEnv(t)
	e.app.RecordsDir = t.TempDir()
	e.cacheItem(watchedItem())
	// The record view still needs the record and a forge; --watch needs neither.
	if _, err := e.item("acme/tracker#15"); err == nil || !strings.Contains(err.Error(), "no item record yet") {
		t.Errorf("err = %v", err)
	}
}
