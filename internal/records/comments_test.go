package records

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// The comments file holds what the snapshot lists: human comments only, the
// newest max, oldest first (a reply in an older thread by its own time), with
// secrets redacted.
func TestCommentsKeepHumanOnesOldestFirst(t *testing.T) {
	s, _ := newStore(t)
	ref := item("T").Ref
	at := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	in := []forge.Note{
		{Author: forge.User{Username: "rae", Name: "Rae Reviewer"}, Body: "Thread start", CreatedAt: at(1)},
		{Author: forge.User{Username: "kim"}, Body: "Late reply, token glpat-abcdefghij1234567890", CreatedAt: at(5)},
		{Author: forge.User{Username: "bot"}, Body: "added ~bug label", System: true, CreatedAt: at(2)},
		{Author: forge.User{Username: "sam", Name: "Sam Smith"}, Body: "  Second thread\n\nmore  ", CreatedAt: at(3)},
	}
	if s.CommentsSynced("gl", ref) {
		t.Fatal("no comments file yet")
	}
	if got, err := s.ReadComments("gl", ref); err != nil || got != nil {
		t.Fatalf("missing file = %v, %v; want none", got, err)
	}
	if err := s.WriteComments("gl", ref, in, 5); err != nil {
		t.Fatal(err)
	}
	if !s.CommentsSynced("gl", ref) {
		t.Error("comments file not reported")
	}
	got, err := s.ReadComments("gl", ref)
	if err != nil {
		t.Fatal(err)
	}
	var authors []string
	for _, c := range got {
		authors = append(authors, c.Author)
	}
	if strings.Join(authors, ",") != "Rae Reviewer,Sam Smith,kim" {
		t.Errorf("authors = %v, want human comments by time", authors)
	}
	if got[1].Body != "Second thread\n\nmore" || !got[1].At.Equal(at(3)) || got[1].Username != "sam" {
		t.Errorf("comment = %+v", got[1])
	}
	if strings.Contains(got[2].Body, "glpat-") {
		t.Errorf("secret kept: %q", got[2].Body)
	}

	// The newest max only; none at all is an empty list, not a missing file.
	if err := s.WriteComments("gl", ref, in, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadComments("gl", ref); len(got) != 1 || got[0].Author != "kim" {
		t.Errorf("max 1 = %+v", got)
	}
	if err := s.WriteComments("gl", ref, nil, 5); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadComments("gl", ref); err != nil || len(got) != 0 || !s.CommentsSynced("gl", ref) {
		t.Errorf("no comments = %v, %v", got, err)
	}
	if fi, err := os.Stat(s.CommentsPath("gl", ref)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("comments file mode: %v %v", fi, err)
	}
}
