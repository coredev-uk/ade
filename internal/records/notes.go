package records

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// ErrNoRecord means the item has no record yet: it is written by the first
// sync that sees the item.
var ErrNoRecord = errors.New("no item record yet (run `ade sync`)")

// ItemPath is the item's record file.
func (s *Store) ItemPath(forgeName string, ref forge.Ref) string {
	return filepath.Join(s.ItemDir(forgeName, ref), ItemFile)
}

// Record is an item record split into what ADE owns and what the user wrote.
type Record struct {
	Path        string
	Frontmatter Frontmatter
	// Snapshot is the ADE-owned block between the markers, without them; when
	// the user removed the markers it is the aside copy (item.snapshot.md).
	Snapshot string
	// Notes is the user's text after the "## Notes" heading.
	Notes string
	// NotesLine is the 1-based line where the notes start (the line after the
	// heading), for opening an editor there; 0 when the record has no heading.
	NotesLine int
}

// Read loads the item's record. A missing record is ErrNoRecord.
func (s *Store) Read(forgeName string, ref forge.Ref) (Record, error) {
	path := s.ItemPath(forgeName, ref)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{Path: path}, ErrNoRecord
	}
	if err != nil {
		return Record{Path: path}, fmt.Errorf("read %s: %w", path, err)
	}
	fm, body, _ := parseFrontmatter(string(data))
	rec := Record{Path: path, Frontmatter: fm}
	rec.Snapshot = snapshotText(body)
	if rec.Snapshot == "" {
		if aside, err := os.ReadFile(filepath.Join(filepath.Dir(path), SnapshotFile)); err == nil {
			rec.Snapshot = snapshotText(string(aside))
		}
	}
	head, notes, found := notesSplit(string(data))
	rec.Notes = notes
	if found {
		rec.NotesLine = strings.Count(head, "\n") + 1
	}
	return rec, nil
}

// snapshotText returns what lies between the snapshot markers, trimmed.
func snapshotText(body string) string {
	pre, post, ok := splitAround(body)
	if !ok {
		return ""
	}
	inner := body[len(pre) : len(body)-len(post)]
	_, inner, _ = strings.Cut(inner, "\n") // the begin marker line
	if i := strings.LastIndex(inner, endMarker); i >= 0 {
		inner = inner[:i]
	}
	return strings.TrimSpace(inner)
}

// notesSplit cuts a whole record after its "## Notes" heading line: head is
// everything through that line, notes the user's text (one leading blank line
// dropped, as ReadNotes does). found is false when there is no heading.
func notesSplit(data string) (head, notes string, found bool) {
	_, body, _ := parseFrontmatter(data)
	if _, post, ok := splitAround(body); ok {
		body = post
	}
	start := len(data) - len(body)
	for off := 0; off < len(body); {
		line, _, _ := strings.Cut(body[off:], "\n")
		off += len(line) + 1
		if strings.TrimRight(line, " \r") == notesHeader {
			cut := start + min(off, len(body))
			return data[:cut], strings.TrimPrefix(data[cut:], "\n"), true
		}
	}
	return data, "", false
}

// WriteNotes replaces the user's notes in the item's record, leaving
// everything before the "## Notes" heading (frontmatter and snapshot) as it
// is. A record without the heading gets one. It never creates the record: the
// snapshot belongs to sync.
func (s *Store) WriteNotes(forgeName string, ref forge.Ref, notes string) error {
	path := s.ItemPath(forgeName, ref)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNoRecord
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	head, _, found := notesSplit(string(data))
	if !found {
		head = strings.TrimRight(head, "\n") + "\n\n" + notesHeader + "\n"
	}
	out := head + "\n"
	if n := strings.TrimSpace(notes); n != "" {
		out += strings.TrimRight(notes, " \t\r\n") + "\n"
	}
	return writeAtomic(path, []byte(out))
}

// noteStamp is the date prefix of an appended note line.
const noteStamp = "2006-01-02 15:04"

// blockedTag marks a note that says what is stopping progress.
const blockedTag = "[blocked] "

// AppendNote adds a dated line ("- 2026-11-12 14:20 text", with "[blocked] "
// before the text when blocked) at the end of the item's "## Notes" section.
// Text the user wrote stays as it is; a record without the heading gets one.
// It never creates the record (ErrNoRecord): callers make it from the cache
// first, as the notes dialog does.
func (s *Store) AppendNote(forgeName string, ref forge.Ref, text string, now time.Time, blocked bool) error {
	text = oneLine(text)
	if text == "" {
		return errors.New("note is empty")
	}
	path := s.ItemPath(forgeName, ref)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNoRecord
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	head, notes, found := notesSplit(string(data))
	if !found {
		head = strings.TrimRight(head, "\n") + "\n\n" + notesHeader + "\n"
	}
	line := "- " + now.Format(noteStamp) + " "
	if blocked {
		line += blockedTag
	}
	out := head + "\n"
	if strings.TrimSpace(notes) != "" {
		out += strings.TrimRight(notes, " \t\r\n") + "\n"
	}
	return writeAtomic(path, []byte(out+line+text+"\n"))
}

// Note is one dated line of the user's notes.
type Note struct {
	At      time.Time
	Blocked bool
	Text    string
}

// ParseNotes returns the dated lines ("- 2026-11-12 14:20 [blocked] text") of
// the notes text, newest first (the later line first among equal times). Any
// other text is the user's own and is ignored.
func ParseNotes(text string) []Note {
	var out []Note
	for _, line := range strings.Split(text, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "- ")
		if !ok || len(rest) <= len(noteStamp) {
			continue
		}
		at, err := time.Parse(noteStamp, rest[:len(noteStamp)])
		if err != nil {
			continue
		}
		body := strings.TrimSpace(rest[len(noteStamp):])
		blocked := strings.HasPrefix(body, strings.TrimSpace(blockedTag))
		if blocked {
			body = strings.TrimSpace(strings.TrimPrefix(body, strings.TrimSpace(blockedTag)))
		}
		out = append(out, Note{At: at, Blocked: blocked, Text: body})
	}
	slices.Reverse(out)
	slices.SortStableFunc(out, func(a, b Note) int { return b.At.Compare(a.At) })
	return out
}

// Blocked reports whether the newest note says work is blocked: a later note
// that is not blocked clears it.
func Blocked(notes []Note) bool { return len(notes) > 0 && notes[0].Blocked }
