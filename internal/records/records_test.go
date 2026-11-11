package records

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

func item(title string) domain.WorkItem {
	w := 3
	ref := forge.Ref{Project: "ade-demo-env/tracker", IID: 7}
	return domain.WorkItem{
		Project:  "demo",
		Ref:      ref,
		Issue:    forge.Issue{Ref: ref, Title: title, Description: "Do the thing.", Labels: []string{"type::story", "app::api"}, Assignees: []forge.User{{Username: "paul"}}},
		Children: []forge.Issue{{Ref: forge.Ref{Project: "ade-demo-env/api", IID: 2}}},
		MRs:      []forge.MR{{Ref: forge.Ref{Project: "ade-demo-env/api", IID: 1, Kind: forge.KindMR}}},
		Stage:    domain.StageReview,
		Court:    domain.CourtReviewers,
		Weight:   &w,
		Sprint:   &domain.Sprint{Title: "Sprint 42"},
	}
}

func notes(n int) []forge.Note {
	var out []forge.Note
	for i := 1; i <= n; i++ {
		out = append(out, forge.Note{Author: forge.User{Username: "u"}, Body: "comment " + string(rune('A'+i-1)), CreatedAt: time.Date(2026, 11, i, 9, 0, 0, 0, time.UTC)})
	}
	return out
}

func newStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	s := New(t.TempDir())
	clock := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	return s, &clock
}

func TestFirstWriteLayoutAndPerms(t *testing.T) {
	s, _ := newStore(t)
	path, changed, err := s.WriteSnapshot("gl", item("Add login"), notes(1), 5)
	if err != nil || !changed {
		t.Fatalf("err=%v changed=%v", err, changed)
	}
	want := filepath.Join(s.dir, "items", "gl", "ade-demo-env", "tracker", "7", "item.md")
	if path != want {
		t.Fatalf("path %s want %s", path, want)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file perm %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir perm %v", fi.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	for _, w := range []string{"# Add login", "Labels: type::story, app::api", "Assignees: @paul", "Do the thing.", "**@u** · 2026-11-01 09:00", "## Notes", endMarker} {
		if !strings.Contains(text, w) {
			t.Errorf("missing %q in\n%s", w, text)
		}
	}
	fm, ok := ParseFrontmatter(text)
	if !ok {
		t.Fatal("frontmatter not parsed")
	}
	if fm.Scalars["ref"] != "ade-demo-env/tracker#7" || fm.Scalars["stage"] != "review" || fm.Scalars["court"] != "reviewers" ||
		fm.Scalars["iteration"] != "Sprint 42" || fm.Scalars["weight"] != "3" || fm.Scalars["synced"] != "2026-11-11T10:00:00Z" {
		t.Errorf("scalars: %v", fm.Scalars)
	}
	if got := fm.Lists["children"]; len(got) != 1 || got[0] != "ade-demo-env/api#2" {
		t.Errorf("children %v", got)
	}
	if got := fm.Lists["mrs"]; len(got) != 1 || got[0] != "ade-demo-env/api!1" {
		t.Errorf("mrs %v", got)
	}
}

func TestMRDirAndUnweighted(t *testing.T) {
	s, _ := newStore(t)
	ref := forge.Ref{Project: "g/p", IID: 4, Kind: forge.KindMR}
	if got, want := s.ItemDir("gl", ref), filepath.Join(s.dir, "items", "gl", "g", "p", "mr-4"); got != want {
		t.Errorf("%s want %s", got, want)
	}
	wi := item("x")
	wi.Weight, wi.Sprint = nil, nil
	path, _, _ := s.WriteSnapshot("gl", wi, nil, 5)
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "weight:") || strings.Contains(string(data), "iteration:") {
		t.Errorf("weight/iteration should be omitted:\n%s", data)
	}
	if dir := s.ItemDir("../x", forge.Ref{Project: "../../etc", IID: 1}); !strings.HasPrefix(dir, s.dir) || strings.Contains(strings.TrimPrefix(dir, s.dir), "/../") {
		t.Errorf("traversal: %s", dir)
	}
}

func TestRewritePreservesUserText(t *testing.T) {
	s, clock := newStore(t)
	path, _, _ := s.WriteSnapshot("gl", item("Old title"), nil, 5)
	data, _ := os.ReadFile(path)
	edited := strings.Replace(string(data), beginMarker, "My intro above\n\n"+beginMarker, 1)
	edited += "my private note\n\n## Extra\nkeep me\n"
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Hour)
	_, changed, err := s.WriteSnapshot("gl", item("New title"), nil, 5)
	if err != nil || !changed {
		t.Fatalf("err=%v changed=%v", err, changed)
	}
	got, _ := os.ReadFile(path)
	text := string(got)
	if !strings.Contains(text, "# New title") || strings.Contains(text, "Old title") {
		t.Errorf("snapshot not refreshed:\n%s", text)
	}
	if !strings.Contains(text, "My intro above\n\n"+beginMarker) || !strings.HasSuffix(text, "my private note\n\n## Extra\nkeep me\n") {
		t.Errorf("user text lost:\n%s", text)
	}
	if fm, _ := ParseFrontmatter(text); fm.Scalars["synced"] != "2026-11-11T11:00:00Z" {
		t.Errorf("synced %v", fm.Scalars["synced"])
	}
	notes, err := s.ReadNotes("gl", item("x").Ref)
	if err != nil || notes != "my private note\n\n## Extra\nkeep me\n" {
		t.Errorf("notes %q err=%v", notes, err)
	}
}

func TestUnchangedLeavesFileAlone(t *testing.T) {
	s, clock := newStore(t)
	path, _, _ := s.WriteSnapshot("gl", item("T"), notes(2), 5)
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(48 * time.Hour)
	_, changed, err := s.WriteSnapshot("gl", item("T"), notes(2), 5)
	if err != nil || changed {
		t.Fatalf("err=%v changed=%v", err, changed)
	}
	if fi, _ := os.Stat(path); !fi.ModTime().Equal(old) {
		t.Errorf("mtime moved to %v", fi.ModTime())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "synced: 2026-11-11T10:00:00Z") {
		t.Errorf("synced rewritten:\n%s", data)
	}
}

func TestDeletedMarkersWriteAside(t *testing.T) {
	s, _ := newStore(t)
	path, _, _ := s.WriteSnapshot("gl", item("T"), nil, 5)
	custom := "---\nref: x#1\n---\nI wrote this myself\n"
	if err := os.WriteFile(path, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	p2, changed, err := s.WriteSnapshot("gl", item("T"), nil, 5)
	if err != nil || !changed || filepath.Base(p2) != SnapshotFile {
		t.Fatalf("p=%s changed=%v err=%v", p2, changed, err)
	}
	if got, _ := os.ReadFile(path); string(got) != custom {
		t.Errorf("original modified: %q", got)
	}
	if side, _ := os.ReadFile(p2); !strings.Contains(string(side), "# T") {
		t.Errorf("aside content: %q", side)
	}
	if _, changed, _ := s.WriteSnapshot("gl", item("T"), nil, 5); changed {
		t.Error("second aside write should be unchanged")
	}
}

func TestMaxCommentsTrimsOldest(t *testing.T) {
	s, _ := newStore(t)
	sys := notes(3)
	sys = append(sys, forge.Note{Author: forge.User{Username: "bot"}, Body: "added label", System: true, CreatedAt: time.Now()})
	path, _, _ := s.WriteSnapshot("gl", item("T"), sys, 2)
	data := string(mustRead(t, path))
	if strings.Contains(data, "comment A") || !strings.Contains(data, "comment B") || !strings.Contains(data, "comment C") || strings.Contains(data, "added label") {
		t.Errorf("trim wrong:\n%s", data)
	}
	if strings.Index(data, "comment B") > strings.Index(data, "comment C") {
		t.Error("comments not oldest-first")
	}
	path, _, _ = s.WriteSnapshot("gl", item("T"), sys, 0)
	if strings.Contains(string(mustRead(t, path)), "## Comments") {
		t.Error("max 0 should drop comments")
	}
}

func TestSecretsRedacted(t *testing.T) {
	s, _ := newStore(t)
	wi := item("T")
	wi.Issue.Description = "token glpat-abcdefghij1234567890 leaked"
	path, _, _ := s.WriteSnapshot("gl", wi, nil, 5)
	if strings.Contains(string(mustRead(t, path)), "glpat-") {
		t.Error("secret written")
	}
}

func TestFrontmatterQuoting(t *testing.T) {
	in := "---\na: \"x, y\"\nl: [\"p, q\", r, \"s]\"]\nempty: []\n---\nbody"
	fm, ok := ParseFrontmatter(in)
	if !ok || fm.Scalars["a"] != "x, y" || len(fm.Lists["l"]) != 3 || fm.Lists["l"][0] != "p, q" || fm.Lists["l"][2] != "s]" || len(fm.Lists["empty"]) != 0 {
		t.Errorf("%v %v", fm, ok)
	}
	if _, ok := ParseFrontmatter("no front"); ok {
		t.Error("should not parse")
	}
	if got := quote("a, b"); splitList(strings.Trim(listValue([]string{"a, b", "c"}), "[]"))[0] != "a, b" || got != `"a, b"` {
		t.Errorf("roundtrip %s", got)
	}
}

func TestIndex(t *testing.T) {
	s, _ := newStore(t)
	if got, err := s.Index(); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	s.WriteSnapshot("gl", item("A"), nil, 1)
	mr := item("B")
	mr.Ref = forge.Ref{Project: "ade-demo-env/api", IID: 1, Kind: forge.KindMR}
	s.WriteSnapshot("gl", mr, nil, 1)
	got, err := s.Index()
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	refs := map[string]bool{}
	for _, e := range got {
		if e.Forge != "gl" {
			t.Errorf("forge %q", e.Forge)
		}
		refs[e.Frontmatter.Scalars["ref"]] = true
	}
	if !refs["ade-demo-env/tracker#7"] || !refs["ade-demo-env/api!1"] {
		t.Errorf("refs %v", refs)
	}
}

func TestReadNotesMissing(t *testing.T) {
	s, _ := newStore(t)
	if n, err := s.ReadNotes("gl", forge.Ref{Project: "a/b", IID: 1}); err != nil || n != "" {
		t.Errorf("%q %v", n, err)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
