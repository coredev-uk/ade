package records

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReadSplitsSnapshotAndNotes(t *testing.T) {
	s, _ := newStore(t)
	wi := item("Rate limit")
	if _, _, err := s.WriteSnapshot("gl", wi, notes(2), 5); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Read("gl", wi.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rec.Snapshot, "# Rate limit") || strings.Contains(rec.Snapshot, "ade:snapshot") {
		t.Errorf("snapshot = %q: want the block without markers", rec.Snapshot)
	}
	if rec.Notes != "" || rec.Frontmatter.Scalars["stage"] != "review" || rec.Frontmatter.Scalars["weight"] != "3" {
		t.Errorf("notes %q, frontmatter %v", rec.Notes, rec.Frontmatter.Scalars)
	}
	if want := strings.Count(string(mustRead(t, rec.Path)[:strings.Index(string(mustRead(t, rec.Path)), "## Notes")]), "\n") + 2; rec.NotesLine != want {
		t.Errorf("NotesLine = %d, want %d (the line after the heading)", rec.NotesLine, want)
	}
}

func TestWriteNotesKeepsSnapshotAndSurvivesResync(t *testing.T) {
	s, clock := newStore(t)
	wi := item("Rate limit")
	if _, _, err := s.WriteSnapshot("gl", wi, nil, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteNotes("gl", wi.Ref, "ask support about the 50 req/min number\n\n- check tenant X\n"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadNotes("gl", wi.Ref)
	if err != nil || got != "ask support about the 50 req/min number\n\n- check tenant X\n" {
		t.Fatalf("notes %q, %v", got, err)
	}
	// A later sync rewrites the snapshot and leaves the notes alone.
	*clock = clock.Add(time.Hour)
	if _, changed, err := s.WriteSnapshot("gl", item("Rate limit v2"), nil, 5); err != nil || !changed {
		t.Fatalf("resync: changed=%v err=%v", changed, err)
	}
	rec, _ := s.Read("gl", wi.Ref)
	if !strings.HasPrefix(rec.Snapshot, "# Rate limit v2") || rec.Notes != "ask support about the 50 req/min number\n\n- check tenant X\n" {
		t.Errorf("after resync: snapshot %q notes %q", rec.Snapshot[:20], rec.Notes)
	}
	// Writing again replaces, not appends; empty clears.
	if err := s.WriteNotes("gl", wi.Ref, "second"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.ReadNotes("gl", wi.Ref); got != "second\n" {
		t.Errorf("replaced notes = %q", got)
	}
	if err := s.WriteNotes("gl", wi.Ref, "  \n"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.ReadNotes("gl", wi.Ref); got != "" {
		t.Errorf("cleared notes = %q", got)
	}
	if text := string(mustRead(t, s.ItemPath("gl", wi.Ref))); strings.Count(text, "## Notes") != 1 || !strings.Contains(text, "# Rate limit v2") {
		t.Errorf("record damaged:\n%s", text)
	}
}

func TestWriteNotesAddsAMissingHeadingAndNeverCreatesTheRecord(t *testing.T) {
	s, _ := newStore(t)
	wi := item("T")
	if err := s.WriteNotes("gl", wi.Ref, "x"); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("no record: err = %v, want ErrNoRecord", err)
	}
	if _, err := s.Read("gl", wi.Ref); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("Read of a missing record: %v", err)
	}
	path, _, _ := s.WriteSnapshot("gl", wi, nil, 5)
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "## Notes\n", "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteNotes("gl", wi.Ref, "back again"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadNotes("gl", wi.Ref); got != "back again\n" {
		t.Errorf("notes = %q", got)
	}
}

func TestAppendNoteAddsDatedLinesAtTheEndOfTheNotes(t *testing.T) {
	s, _ := newStore(t)
	wi := item("Rate limit")
	if err := s.AppendNote("gl", wi.Ref, "x", time.Now(), false); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("no record: err = %v, want ErrNoRecord", err)
	}
	if _, _, err := s.WriteSnapshot("gl", wi, nil, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteNotes("gl", wi.Ref, "my own words\n\n- plain bullet\n"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 12, 14, 20, 0, 0, time.UTC)
	if err := s.AppendNote("gl", wi.Ref, "waiting for\nsupport", at, true); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendNote("gl", wi.Ref, "unblocked, retrying", at.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ReadNotes("gl", wi.Ref)
	want := "my own words\n\n- plain bullet\n" +
		"- 2026-11-12 14:20 [blocked] waiting for support\n" +
		"- 2026-11-12 15:20 unblocked, retrying\n"
	if got != want {
		t.Errorf("notes = %q, want %q", got, want)
	}
	rec, _ := s.Read("gl", wi.Ref)
	if !strings.HasPrefix(rec.Snapshot, "# Rate limit") {
		t.Errorf("snapshot changed: %q", rec.Snapshot)
	}
	if err := s.AppendNote("gl", wi.Ref, "  ", at, false); err == nil {
		t.Error("an empty note was accepted")
	}
}

func TestAppendNoteOnARecordWithoutTheHeadingAndEmptyNotes(t *testing.T) {
	s, _ := newStore(t)
	wi := item("Rate limit")
	if _, _, err := s.WriteSnapshot("gl", wi, nil, 5); err != nil {
		t.Fatal(err)
	}
	path := s.ItemPath("gl", wi.Ref)
	data := mustRead(t, path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "## Notes", "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 12, 9, 5, 0, 0, time.UTC)
	if err := s.AppendNote("gl", wi.Ref, "first", at, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadNotes("gl", wi.Ref); got != "- 2026-11-12 09:05 first\n" {
		t.Errorf("notes = %q", got)
	}
}

func TestParseNotesNewestFirstAndIgnoresFreeText(t *testing.T) {
	text := "free text\n- plain bullet\n" +
		"- 2026-11-12 14:20 [blocked] waiting for support\n" +
		"- 2026-11-10 09:00 started on the cache key\n" +
		"- 2026-11-12 16:00 support answered\n" +
		"- 2026-13-40 99:99 not a date\n"
	got := ParseNotes(text)
	if len(got) != 3 {
		t.Fatalf("parsed %d notes: %+v", len(got), got)
	}
	if got[0].Text != "support answered" || got[0].Blocked || got[1].Text != "waiting for support" || !got[1].Blocked || got[2].Text != "started on the cache key" {
		t.Errorf("notes = %+v", got)
	}
	if Blocked(got) {
		t.Error("a later note that is not blocked clears the state")
	}
	if !Blocked(ParseNotes("- 2026-11-12 14:20 [blocked] x")) || Blocked(nil) {
		t.Error("Blocked: newest blocked note should block, none should not")
	}
}
