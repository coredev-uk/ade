package cli

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
)

func cacheApp(t *testing.T) *app.App {
	t.Helper()
	dir := t.TempDir()
	rec := filepath.Join(dir, "records")
	al, err := audit.Open(rec, "user")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	a := &app.App{Config: &cfg, Root: filepath.Join(dir, "root"), RecordsDir: rec, Audit: al, Bus: events.NewBus()}
	a.Paths.CacheDir = filepath.Join(dir, "cache")
	a.Paths.DataDir = filepath.Join(dir, "data")
	t.Cleanup(func() { a.Close() })
	return a
}

func seedCache(t *testing.T, a *app.App) *sql.DB {
	t.Helper()
	st, err := a.Cache()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO inbox (id, time, kind, severity, title, read) VALUES ('a', 1, 'ci.failed', 'warn', 'one', 0), ('b', 2, 'ci.passed', 'info', 'two', 1)`,
		`INSERT INTO review_drafts VALUES ('g/api!1', 'd1', 'a.go', '', 3, 'new', 'nit', 'nit', 'manual', 'pending', 0, 0, '2026-03-01T00:00:00Z'),
		   ('g/api!1', 'd2', 'a.go', '', 4, 'new', 'sent', 'nit', 'manual', 'pending', 9, 1, '2026-03-01T00:00:00Z')`,
		`INSERT INTO kv (k, v) VALUES ('marker', 'x')`,
	} {
		if _, err := st.DB.Exec(q); err != nil {
			t.Fatalf("%v: %s", err, q)
		}
	}
	return st.DB
}

func TestResetCacheDeletesRebuildsAndAudits(t *testing.T) {
	a := cacheApp(t)
	seedCache(t, a)
	// One paid call in the audit log: the ledger comes back from it.
	if err := a.Audit.Append(audit.Entry{Action: "ai.summarize", AI: &audit.AIInfo{Profile: "p", Model: "m", CostUSD: 0.01, CostSource: "reported"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.Paths.CacheDir, "cache.db")

	var asked cacheLoss
	res, err := resetCache(context.Background(), a, cacheOptions{Confirm: func(l cacheLoss) (bool, error) { asked = l; return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if asked.ReviewDrafts != 1 || asked.UnreadNotification != 1 || asked.Path != path {
		t.Errorf("the question must say what is lost (unsubmitted drafts, unread): %+v", asked)
	}
	if !res.Reset || res.LedgerEntries != 1 {
		t.Fatalf("result %+v", res)
	}

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for q, want := range map[string]int{
		`SELECT count(*) FROM kv WHERE k = 'marker'`: 0,
		`SELECT count(*) FROM inbox`:                 0,
		`SELECT count(*) FROM review_drafts`:         0,
		`SELECT count(*) FROM ledger_entries`:        1,
	} {
		var got int
		if err := db.QueryRow(q).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %d (%v), want %d", q, got, err, want)
		}
	}
	es, _ := a.Audit.Query(audit.Filter{Action: "cache.reset"})
	if len(es) != 1 || es[0].Params["review_drafts"] != float64(1) && es[0].Params["review_drafts"] != 1 {
		t.Fatalf("audit: %+v", es)
	}
}

func TestResetCacheCancelAndDryRunTouchNothing(t *testing.T) {
	a := cacheApp(t)
	db := seedCache(t, a)
	for name, o := range map[string]cacheOptions{
		"declined": {Confirm: func(cacheLoss) (bool, error) { return false, nil }},
		"dry-run":  {DryRun: true, Confirm: func(cacheLoss) (bool, error) { t.Fatal("dry-run must not ask"); return true, nil }},
	} {
		res, err := resetCache(context.Background(), a, o)
		if err != nil || res.Reset {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM kv`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s: cache changed (%d, %v)", name, n, err)
		}
	}
	if es, _ := a.Audit.Query(audit.Filter{Action: "cache.reset"}); len(es) != 0 {
		t.Fatalf("a reset that did not happen was audited: %v", es)
	}
}

func TestConfirmCacheResetNeedsATerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "in")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = confirmCacheReset(f, &strings.Builder{}, cacheLoss{})
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
}
