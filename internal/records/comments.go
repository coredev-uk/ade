package records

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forge"
)

// CommentsFile holds, structured, the human comments of the item's tracker
// issue that the snapshot block lists under "## Comments": the dashboard and
// the overview pane read it instead of parsing markdown. It belongs to the
// store and is rewritten by every sync that reads the comments.
const CommentsFile = "comments.json"

// Comment is one human comment on the tracker issue. System notes ("assigned
// to …", label changes) are never kept.
type Comment struct {
	// Author is the name shown (the username when the forge gives no name).
	Author   string    `json:"author"`
	Username string    `json:"username,omitempty"`
	At       time.Time `json:"at"`
	Body     string    `json:"body"`
}

// FirstLine is the body's first non-blank line for a one-line listing:
// whitespace collapsed, a leading heading, quote or list marker and the
// emphasis markers (`**`, `__`, backticks) dropped.
func (c Comment) FirstLine() string {
	for line := range strings.SplitSeq(c.Body, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "#> ")
		for _, p := range []string{"- [ ] ", "- [x] ", "- ", "* ", "+ "} {
			if rest, ok := strings.CutPrefix(line, p); ok {
				line = rest
				break
			}
		}
		line = strings.NewReplacer("**", "", "__", "", "`", "").Replace(line)
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			return line
		}
	}
	return ""
}

// CommentsFrom keeps the newest max human comments of notes, oldest first,
// with secrets redacted: what WriteComments stores.
func CommentsFrom(notes []forge.Note, max int) []Comment {
	kept := lastComments(notes, max)
	out := make([]Comment, 0, len(kept))
	for _, n := range kept {
		name := n.Author.Name
		if name == "" {
			name = n.Author.Username
		}
		out = append(out, Comment{Author: name, Username: n.Author.Username, At: n.CreatedAt.UTC(), Body: audit.Redact(strings.TrimSpace(n.Body))})
	}
	return out
}

// CommentsPath is the item's comments file.
func (s *Store) CommentsPath(forgeName string, ref forge.Ref) string {
	return filepath.Join(s.ItemDir(forgeName, ref), CommentsFile)
}

// WriteComments stores the newest max human comments of notes, the ones the
// snapshot block lists. The file is left alone when it would not change.
func (s *Store) WriteComments(forgeName string, ref forge.Ref, notes []forge.Note, max int) error {
	dir := s.ItemDir(forgeName, ref)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(CommentsFrom(notes, max), "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	unlock, err := lockDir(dir)
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(dir, CommentsFile)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	return writeAtomic(path, data)
}

// ReadComments returns the comments the last sync kept, oldest first; none
// when no sync has written them yet.
func (s *Store) ReadComments(forgeName string, ref forge.Ref) ([]Comment, error) {
	path := s.CommentsPath(forgeName, ref)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var out []Comment
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// CommentsSynced reports whether a sync has written the item's comments file:
// a record from before it existed (or made by the notes dialog) has none, and
// the next sync reads the comments even when the issue did not change.
func (s *Store) CommentsSynced(forgeName string, ref forge.Ref) bool {
	_, err := os.Stat(s.CommentsPath(forgeName, ref))
	return err == nil
}
