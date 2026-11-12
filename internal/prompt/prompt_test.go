package prompt

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/commitprefix"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/store"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// layout builds <root>/acme/api/{.bare,main,412-rate-limit} the way ADE does
// (bare clone, linked worktrees) with the item's git config written by
// ADE's own writer, and returns the root.
func layout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "acme", "api")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "init", "-q", "-b", "main")
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, repo, "clone", "-q", "--bare", seed, ".bare")
	git(t, repo, "--git-dir=.bare", "worktree", "add", "-q", "main", "main")
	git(t, repo, "--git-dir=.bare", "worktree", "add", "-q", "-b", "412-rate-limit", "412-rate-limit", "main")
	if err := commitprefix.WriteWorktreeConfig(context.Background(), filepath.Join(repo, "412-rate-limit"), 412, "GL-412", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "412-rate-limit", "internal", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func mineItem(project string, iid int, court domain.Court, stage domain.Stage, assigned bool) domain.WorkItem {
	r := forge.Ref{Project: "g/tracker", IID: iid, Kind: forge.KindIssue}
	return domain.WorkItem{Project: project, Ref: r, Issue: forge.Issue{Ref: r}, Court: court, Stage: stage, Assigned: assigned}
}

// syncedCache writes a cache.db as a sync leaves it: acme has two items waiting
// on you (one is done, one is not assigned, one waits on reviewers: none count),
// and two review requests; zeta has one item waiting on you.
func syncedCache(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cache.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	put := func(project string, reviews int, items ...domain.WorkItem) {
		var revs []forge.MR
		for i := range reviews {
			revs = append(revs, forge.MR{Ref: forge.Ref{Project: "g/api", IID: 100 + i, Kind: forge.KindMR}})
		}
		state, _ := json.Marshal(forgesync.Result{Project: project, Reviews: revs, SyncedAt: mustTime("2026-11-12T09:30:00Z")})
		if _, err := st.DB.Exec(`INSERT INTO sync_state (project, json) VALUES (?, ?)`, project, string(state)); err != nil {
			t.Fatal(err)
		}
		for _, wi := range items {
			b, _ := json.Marshal(wi)
			if _, err := st.DB.Exec(`INSERT INTO work_items (project, ref, json) VALUES (?, ?, ?)`, project, wi.Ref.String(), string(b)); err != nil {
				t.Fatal(err)
			}
		}
	}
	put("acme", 2,
		mineItem("acme", 1, domain.CourtYou, domain.StageReview, true),
		mineItem("acme", 2, domain.CourtYou, domain.StageStarted, true),
		mineItem("acme", 3, domain.CourtYou, domain.StageDone, true),
		mineItem("acme", 4, domain.CourtYou, domain.StageStarted, false),
		mineItem("acme", 5, domain.CourtReviewers, domain.StageReview, true),
	)
	put("zeta", 0, mineItem("zeta", 9, domain.CourtYou, domain.StageDraft, true))
	return path
}

func TestInTreeWorktree(t *testing.T) {
	root := layout(t)
	db := syncedCache(t)
	for _, sub := range []string{"acme/api/412-rate-limit", "acme/api/412-rate-limit/internal/deep"} {
		info := Read(Options{Root: root, CacheDB: db, Path: filepath.Join(root, sub), AllProjects: true})
		if !info.InTree || info.Project != "acme" || info.Repo != "api" || info.Branch != "412-rate-limit" ||
			info.Parent != 412 || info.Prefix != "GL-412" || info.Ticket != "GL-412" {
			t.Errorf("%s: info = %+v", sub, info)
		}
		a := info.Attention
		if info.Cache != CacheOK || a == nil || a.Items != 3 || a.Reviews != 2 || a.Total != 5 || a.SyncedAt.IsZero() {
			t.Errorf("%s: attention = %v (%s), want 3 items + 2 reviews across both projects", sub, a, info.Cache)
		}
	}
	info := Read(Options{Root: root, CacheDB: db, Path: filepath.Join(root, "acme/api/412-rate-limit")})
	if a := info.Attention; a == nil || a.Items != 2 || a.Reviews != 2 || a.Total != 4 {
		t.Errorf("project scope attention = %+v, want acme only: 2 items + 2 reviews", a)
	}
}

func TestBaselineTreeHasNoTicket(t *testing.T) {
	root := layout(t)
	info := Read(Options{Root: root, CacheDB: syncedCache(t), Path: filepath.Join(root, "acme/api/main")})
	if !info.InTree || info.Branch != "main" || info.Ticket != "" || info.Parent != 0 {
		t.Errorf("baseline = %+v", info)
	}
	if got := Render(DefaultFormat, info, false); got != "!4" {
		t.Errorf("rendered %q, want only the attention count", got)
	}
}

func TestTicketFallsBackToParentIID(t *testing.T) {
	root := layout(t)
	tree := filepath.Join(root, "acme/api/412-rate-limit")
	if err := commitprefix.WriteWorktreeConfig(context.Background(), tree, 412, "", "main"); err != nil {
		t.Fatal(err)
	}
	info := Read(Options{Root: root, CacheDB: filepath.Join(t.TempDir(), "none.db"), Path: tree})
	if info.Prefix != "" || info.Ticket != "#412" {
		t.Errorf("info = %+v, want ticket #412 without a prefix", info)
	}
}

func TestOutsideAnADETreeIsSilent(t *testing.T) {
	root := layout(t)
	db := syncedCache(t)
	outside := t.TempDir()
	notRepo := filepath.Join(root, "acme", "notes") // under the root, but no .bare
	if err := os.MkdirAll(notRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"elsewhere":      outside,
		"the root":       root,
		"project level":  filepath.Join(root, "acme"),
		"not a repo":     notRepo,
		"missing path":   filepath.Join(outside, "gone"),
		"sibling prefix": root + "-other",
	} {
		info := Read(Options{Root: root, CacheDB: db, Path: path})
		if info.InTree || Render(DefaultFormat, info, true) != "" {
			t.Errorf("%s: info = %+v, rendered %q; want nothing", name, info, Render(DefaultFormat, info, true))
		}
	}
	if got := Read(Options{Root: filepath.Join(outside, "no-root"), CacheDB: db, Path: outside}); got.InTree {
		t.Errorf("a root that does not exist matched: %+v", got)
	}
}

func TestReadsTheCacheOnlyAndNeverCreatesIt(t *testing.T) {
	root := layout(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "cache.db")
	info := Read(Options{Root: root, CacheDB: db, Path: filepath.Join(root, "acme/api/412-rate-limit")})
	if info.Cache != CacheMissing || info.Attention != nil || info.Ticket != "GL-412" {
		t.Errorf("no cache: %+v", info)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the prompt created %v", entries)
	}

	// A cache that exists but never synced has no tables: still not an error.
	empty := filepath.Join(dir, "empty.db")
	st, err := store.Open(empty)
	if err != nil {
		t.Fatal(err)
	}
	st.DB.Exec(`DROP TABLE sync_state`)
	st.DB.Exec(`DROP TABLE work_items`)
	st.Close()
	if info := Read(Options{Root: root, CacheDB: empty, Path: filepath.Join(root, "acme/api/412-rate-limit")}); info.Cache != CacheMissing || info.Attention != nil {
		t.Errorf("tableless cache: %+v", info)
	}

	// So does one written by an older ade, before the attention column: the
	// prompt does not migrate, it waits for a writer to.
	old := filepath.Join(dir, "old.db")
	st, err = store.Open(old)
	if err != nil {
		t.Fatal(err)
	}
	st.DB.Exec(`DROP INDEX work_items_attention`)
	st.DB.Exec(`ALTER TABLE work_items DROP COLUMN attention`)
	st.Close()
	if info := Read(Options{Root: root, CacheDB: old, Path: filepath.Join(root, "acme/api/412-rate-limit")}); info.Cache != CacheMissing || info.Attention != nil {
		t.Errorf("unmigrated cache: %+v", info)
	}

	// A file that is not a database reports an error state and keeps the ticket.
	junk := filepath.Join(dir, "junk.db")
	os.WriteFile(junk, []byte("not sqlite, definitely"), 0o600)
	if info := Read(Options{Root: root, CacheDB: junk, Path: filepath.Join(root, "acme/api/412-rate-limit")}); info.Cache != CacheError || info.Ticket != "GL-412" {
		t.Errorf("junk cache: %+v", info)
	}
}

func TestReadDoesNotWriteTheCache(t *testing.T) {
	root := layout(t)
	db := syncedCache(t)
	before, _ := os.ReadFile(db)
	Read(Options{Root: root, CacheDB: db, Path: filepath.Join(root, "acme/api/412-rate-limit"), AllProjects: true})
	if after, _ := os.ReadFile(db); !bytes.Equal(before, after) {
		t.Error("the prompt changed cache.db")
	}
	// And it can read while a writer holds the database open.
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tx, _ := st.DB.Begin()
	defer tx.Rollback() //nolint:errcheck
	tx.Exec(`INSERT INTO kv (k, v) VALUES ('x', 'y')`)
	if info := Read(Options{Root: root, CacheDB: db, Path: filepath.Join(root, "acme/api/412-rate-limit"), AllProjects: true}); info.Cache != CacheOK || info.Attention.Total != 5 {
		t.Errorf("with a writer open: %+v", info)
	}
}

func TestRender(t *testing.T) {
	info := Info{
		InTree: true, Project: "acme", Repo: "api", Branch: "412-rate-limit", Worktree: "/r/acme/api/412-rate-limit", Ticket: "GL-412",
		Cache: CacheOK, Attention: &forgesync.Attention{Items: 2, Reviews: 1, Total: 3},
	}
	none := info
	none.Attention = &forgesync.Attention{}
	noTicket := info
	noTicket.Ticket = ""
	tests := []struct {
		name   string
		format string
		info   Info
		zero   bool
		want   string
	}{
		{"default", DefaultFormat, info, false, "GL-412 !3"},
		{"raw numbers", "{ticket}:{attention_n} ({items}+{reviews})", info, false, "GL-412:3 (2+1)"},
		{"names", "{project}/{repo}@{branch}", info, false, "acme/api@412-rate-limit"},
		{"nothing waits", DefaultFormat, none, false, "GL-412"},
		{"nothing waits, zero shown", DefaultFormat, none, true, "GL-412 !0"},
		{"no ticket", DefaultFormat, noTicket, false, "!3"},
		{"no cache", DefaultFormat, Info{InTree: true, Ticket: "GL-412"}, false, "GL-412"},
		{"outside", DefaultFormat, Info{}, true, ""},
	}
	for _, tt := range tests {
		if got := Render(tt.format, tt.info, tt.zero); got != tt.want {
			t.Errorf("%s: Render = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCheckFormat(t *testing.T) {
	if err := CheckFormat("{ticket} [{attention}] {project}/{repo}"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"{nope}", "{ticket", "x {attention} {Ticket}"} {
		if err := CheckFormat(bad); err == nil {
			t.Errorf("CheckFormat(%q) accepted", bad)
		}
	}
}

func TestGitConfigSection(t *testing.T) {
	src := "[core]\n\tbare = false\n[ade]\n\tparent = 7\n\tcommitPrefix = \"GL \\\"7\\\"\"\n# note\n[ade \"sub\"]\n\tparent = 99\n[other]\n\tparent = 5\n[ADE]\n\ttarget = main\n"
	got := gitConfigSection([]byte(src), "ade")
	if got["parent"] != "7" || got["commitprefix"] != `GL "7"` || got["target"] != "main" || len(got) != 3 {
		t.Errorf("got %v", got)
	}
	if strings.Contains(src, "\r") {
		t.Fatal("test source has CR")
	}
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// BenchmarkRead is the prompt's budget on a warm cache: the whole command aims
// at about 20 ms, and this is everything it does that is not process start.
func BenchmarkRead(b *testing.B) {
	for _, n := range []int{200, 2000} {
		b.Run(strconv.Itoa(n)+"items", func(b *testing.B) {
			root := b.TempDir()
			repo := filepath.Join(root, "acme", "api")
			if err := os.MkdirAll(filepath.Join(repo, ".bare"), 0o755); err != nil {
				b.Fatal(err)
			}
			db := filepath.Join(b.TempDir(), "cache.db")
			st, err := store.Open(db)
			if err != nil {
				b.Fatal(err)
			}
			tx, _ := st.DB.Begin()
			tx.Exec(`INSERT INTO sync_state (project, json) VALUES ('acme', '{"project":"acme"}')`)
			for i := range n {
				wi := mineItem("acme", i, domain.CourtYou, domain.StageReview, i%2 == 0)
				wi.Issue.Description = strings.Repeat("lorem ipsum ", 400) // about 5 KB
				raw, _ := json.Marshal(wi)
				tx.Exec(`INSERT INTO work_items (project, ref, json) VALUES ('acme', ?, ?)`, wi.Ref.String(), string(raw))
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			st.Close()
			b.ResetTimer()
			for range b.N {
				if info := Read(Options{Root: root, CacheDB: db, Path: repo, AllProjects: true}); info.Attention == nil || info.Attention.Items != n/2 {
					b.Fatalf("info = %+v", info)
				}
			}
		})
	}
}
