package records

import (
	"os"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

func TestCommentsSeenStartsEmptyAndOnlyMovesForward(t *testing.T) {
	s := New(t.TempDir())
	ref := forge.Ref{Project: "g/api", IID: 4, Kind: forge.KindMR}
	if got, err := s.CommentsSeen("gl", ref); err != nil || !got.IsZero() {
		t.Fatalf("a never-seen item: %v %v", got, err)
	}
	t1 := time.Date(2026, 11, 10, 9, 0, 0, 123_000_000, time.UTC)
	if moved, err := s.MarkCommentsSeen("gl", ref, t1); err != nil || !moved {
		t.Fatalf("first mark: %v %v", moved, err)
	}
	if got, _ := s.CommentsSeen("gl", ref); !got.Equal(t1) {
		t.Fatalf("read back %v, want %v (sub-second precision kept)", got, t1)
	}
	// Never backwards, never the same time again.
	for _, back := range []time.Time{t1.Add(-time.Hour), t1} {
		if moved, err := s.MarkCommentsSeen("gl", ref, back); err != nil || moved {
			t.Fatalf("mark %v: moved=%v err=%v", back, moved, err)
		}
	}
	t2 := t1.Add(time.Minute)
	if moved, _ := s.MarkCommentsSeen("gl", ref, t2); !moved {
		t.Fatal("a later time must move the marker")
	}
	// Another item and another kind are separate.
	other := forge.Ref{Project: "g/api", IID: 4, Kind: forge.KindIssue}
	if got, _ := s.CommentsSeen("gl", other); !got.IsZero() {
		t.Fatalf("issue 4 shares the marker of MR 4: %v", got)
	}
	if err := os.WriteFile(s.seenPath("gl", ref), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommentsSeen("gl", ref); err == nil {
		t.Fatal("a corrupt marker must be an error, not a silent first look")
	}
}
