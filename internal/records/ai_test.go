package records

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

func TestArtifactRoundTripKeepsEverythingTheCacheNeeds(t *testing.T) {
	s := New(t.TempDir())
	dir := s.ItemDir("sandbox", forge.Ref{Project: "grp/api", IID: 1, Kind: forge.KindMR})
	a := Artifact{
		Feature: "summarize", Profile: "claude-sub", Model: "claude-haiku-4-5", PromptVersion: "summarize-v1",
		InputHash: "abc123", TokensIn: 1840, TokensOut: 160, CostUSD: 0.0026,
		Created: time.Date(2026, 11, 11, 9, 16, 40, 0, time.UTC), AuditID: "01JCQ9",
		// A body that looks like frontmatter must not confuse the parser.
		Body: "- point one\n---\nfeature: injected\n---\nRisk: low",
	}
	rel, err := s.WriteArtifact(dir, "summary-mr1", a)
	if err != nil {
		t.Fatal(err)
	}
	if want := "items/sandbox/grp/api/mr-1/ai/20261111T091640-summary-mr1.md"; rel != want {
		t.Fatalf("path = %s, want %s", rel, want)
	}
	got, err := s.ReadArtifact(rel)
	if err != nil {
		t.Fatal(err)
	}
	a.Path = rel
	if got != a {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, a)
	}
	if info, _ := os.Stat(filepath.Join(s.dir, filepath.FromSlash(rel))); info.Mode().Perm() != 0o600 {
		t.Fatalf("artifacts hold real work content: mode %v", info.Mode().Perm())
	}
}

func TestWriteArtifactNeverOverwrites(t *testing.T) {
	s := New(t.TempDir())
	dir := s.ItemDir("f", forge.Ref{Project: "g/p", IID: 2})
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p1, err1 := s.WriteArtifact(dir, "classify", Artifact{InputHash: "h1", Body: "first", Created: at})
	p2, err2 := s.WriteArtifact(dir, "classify", Artifact{InputHash: "h2", Body: "second", Created: at})
	if err1 != nil || err2 != nil || p1 == p2 {
		t.Fatalf("%s %s %v %v", p1, p2, err1, err2)
	}
	if a, _ := s.ReadArtifact(p1); a.Body != "first" {
		t.Fatalf("first artifact was overwritten: %+v", a)
	}
}

func TestArtifactsIndexOnlyValidAIFilesAtAnyDepth(t *testing.T) {
	s := New(t.TempDir())
	issue := s.ItemDir("f", forge.Ref{Project: "g/tracker", IID: 3})
	mr := s.ItemDir("f", forge.Ref{Project: "g/api", IID: 4, Kind: forge.KindMR})
	if _, err := s.WriteArtifact(issue, "classify", Artifact{InputHash: "h1", Body: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteArtifact(mr, "summary", Artifact{InputHash: "h2", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	// Noise that must be skipped: no frontmatter, no hash, wrong folder, not markdown.
	write := func(p, content string) {
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte(content), 0o600)
	}
	write(filepath.Join(issue, "ai", "notes.md"), "just text\n")
	write(filepath.Join(issue, "ai", "nohash.md"), "---\nfeature: x\n---\nbody\n")
	write(filepath.Join(issue, "plans", "plan.md"), "---\ninput_hash: zzz\n---\nplan\n")
	write(filepath.Join(issue, "ai", "tmp.txt"), "---\ninput_hash: zzz\n---\n")
	arts, err := s.Artifacts()
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, a := range arts {
		hashes = append(hashes, a.InputHash)
	}
	if strings.Join(hashes, ",") != "h2,h1" && strings.Join(hashes, ",") != "h1,h2" || len(arts) != 2 {
		t.Fatalf("indexed %v", hashes)
	}
	if empty, err := New(t.TempDir()).Artifacts(); err != nil || len(empty) != 0 {
		t.Fatalf("no records yet: %v %v", empty, err)
	}
}

func TestReadArtifactRefusesPathsOutsideTheDataDir(t *testing.T) {
	s := New(t.TempDir())
	for _, p := range []string{"../etc/passwd", "/etc/passwd", "items/../../x"} {
		if _, err := s.ReadArtifact(p); err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Errorf("%s: %v", p, err)
		}
	}
}
