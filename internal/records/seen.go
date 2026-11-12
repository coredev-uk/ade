package records

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// SeenFile is the "last looked" marker of an item (or merge request) record:
// how far its comments have been read, so a digest can summarise only what
// came after. It is local to this machine and holds no text of the comments.
const SeenFile = "seen.json"

type seenMarker struct {
	// CommentsUntil is the creation time of the newest comment already seen.
	CommentsUntil time.Time `json:"comments_until"`
}

func (s *Store) seenPath(forgeName string, ref forge.Ref) string {
	return filepath.Join(s.ItemDir(forgeName, ref), SeenFile)
}

// CommentsSeen is the creation time of the newest comment of ref that was
// seen; the zero time when it never was.
func (s *Store) CommentsSeen(forgeName string, ref forge.Ref) (time.Time, error) {
	path := s.seenPath(forgeName, ref)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read %s: %w", path, err)
	}
	var m seenMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", path, err)
	}
	return m.CommentsUntil, nil
}

// MarkCommentsSeen moves the marker forward to until. It never moves it back,
// so two digests running at once cannot make a comment new again, and it
// reports whether the marker moved. The record directory is created when the
// item has no record yet.
func (s *Store) MarkCommentsSeen(forgeName string, ref forge.Ref, until time.Time) (bool, error) {
	cur, err := s.CommentsSeen(forgeName, ref)
	if err != nil {
		return false, err
	}
	if !until.After(cur) {
		return false, nil
	}
	data, err := json.MarshalIndent(seenMarker{CommentsUntil: until.UTC()}, "", "  ")
	if err != nil {
		return false, err
	}
	if err := writeAtomic(s.seenPath(forgeName, ref), append(data, '\n')); err != nil {
		return false, err
	}
	return true, nil
}
