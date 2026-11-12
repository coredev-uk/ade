// Package records keeps one markdown file per work item under the data dir:
// an ADE-owned snapshot block (rewritten on sync) next to the user's own
// notes, which are never touched.
package records

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

const (
	// ItemFile is the record; SnapshotFile receives the snapshot instead when
	// the user removed the markers from ItemFile.
	ItemFile     = "item.md"
	SnapshotFile = "item.snapshot.md"

	beginPrefix = "<!-- ade:snapshot:begin"
	beginMarker = beginPrefix + " (ADE rewrites everything between the markers; write below) -->"
	endMarker   = "<!-- ade:snapshot:end -->"
	notesHeader = "## Notes"

	// lockFile serialises the read-modify-rename paths on one record, so a
	// sync cannot drop a note added while it ran (and the other way round).
	lockFile = ".item.lock"
)

// Store writes item records below dir/items.
type Store struct {
	dir string
	now func() time.Time
}

// New returns a Store rooted at dir (the ADE data dir).
func New(dir string) *Store { return &Store{dir: dir, now: time.Now} }

// segment makes one path component safe: no traversal, no separators.
func segment(s string) string {
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	return strings.ReplaceAll(s, string(filepath.Separator), "_")
}

// ItemDir is <dir>/items/<forge>/<project path>/<iid>/, with an "mr-" prefix
// on the iid of merge requests.
func (s *Store) ItemDir(forgeName string, ref forge.Ref) string {
	parts := []string{s.dir, "items", segment(forgeName)}
	for p := range strings.SplitSeq(ref.Project, "/") {
		parts = append(parts, segment(p))
	}
	name := strconv.Itoa(ref.IID)
	if ref.Kind == forge.KindMR {
		name = "mr-" + name
	}
	return filepath.Join(append(parts, name)...)
}

// WriteSnapshot refreshes the snapshot block of wi's record. changed is false
// (and the file untouched) when nothing but the synced time would differ. If
// the user deleted the markers, the snapshot goes to SnapshotFile instead and
// the returned path says so.
func (s *Store) WriteSnapshot(forgeName string, wi domain.WorkItem, comments []forge.Note, maxComments int) (path string, changed bool, err error) {
	dir := s.ItemDir(forgeName, wi.Ref)
	path = filepath.Join(dir, ItemFile)
	block := snapshotBlock(wi, comments, maxComments)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return path, false, fmt.Errorf("create %s: %w", dir, err)
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return path, false, err
	}
	defer unlock()

	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return path, false, fmt.Errorf("read %s: %w", path, err)
	}
	fm, body, _ := parseFrontmatter(string(existing))
	oldSynced := fm.Scalars["synced"]

	var pre, post string
	if existing == nil {
		pre, post = "\n", "\n"+notesHeader+"\n\n"
	} else {
		var ok bool
		if pre, post, ok = splitAround(body); !ok {
			return s.writeAside(dir, block)
		}
	}

	build := func(synced string) []byte {
		return []byte(renderFrontmatter(forgeName, wi, synced) + pre + block + post)
	}
	if oldSynced != "" && bytes.Equal(build(oldSynced), existing) {
		return path, false, nil
	}
	if err := writeAtomic(path, build(s.now().UTC().Format(time.RFC3339))); err != nil {
		return path, false, err
	}
	return path, true, nil
}

// writeAside stores the block in SnapshotFile when ItemFile has no markers.
func (s *Store) writeAside(dir, block string) (string, bool, error) {
	path := filepath.Join(dir, SnapshotFile)
	content := []byte(block)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, content) {
		return path, false, nil
	}
	if err := writeAtomic(path, content); err != nil {
		return path, false, err
	}
	return path, true, nil
}

// splitAround returns the text before the begin marker line and after the end
// marker line (newline included), or ok=false if either marker is missing.
func splitAround(body string) (pre, post string, ok bool) {
	begin, end := -1, -1
	for off := 0; off < len(body); {
		line, _, _ := strings.Cut(body[off:], "\n")
		next := off + len(line) + 1
		switch {
		case begin < 0 && strings.HasPrefix(line, beginPrefix):
			begin = off
		case begin >= 0 && strings.TrimRight(line, " \r") == endMarker:
			end = min(next, len(body))
		}
		if end >= 0 {
			break
		}
		off = next
	}
	if begin < 0 || end < 0 {
		return "", "", false
	}
	return body[:begin], body[end:], true
}

func renderFrontmatter(forgeName string, wi domain.WorkItem, synced string) string {
	var b strings.Builder
	kv := func(k, v string) { b.WriteString(k + ": " + quote(v) + "\n") }
	list := func(k string, refs []string) { b.WriteString(k + ": " + listValue(refs) + "\n") }

	b.WriteString(fence + "\n")
	kv("ref", wi.Ref.String())
	kv("forge", forgeName)
	if wi.Project != "" {
		kv("project", wi.Project)
	}
	children := make([]string, len(wi.Children))
	for i, c := range wi.Children {
		children[i] = c.Ref.String()
	}
	list("children", children)
	mrs := make([]string, len(wi.MRs))
	for i, m := range wi.MRs {
		mrs[i] = m.Ref.String()
	}
	list("mrs", mrs)
	kv("stage", wi.Stage.String())
	kv("court", string(wi.Court))
	if t := iterationTitle(wi); t != "" {
		kv("iteration", t)
	}
	if wi.Weight != nil {
		kv("weight", strconv.Itoa(*wi.Weight))
	}
	kv("synced", synced)
	b.WriteString(fence + "\n")
	return b.String()
}

func iterationTitle(wi domain.WorkItem) string {
	switch {
	case wi.Sprint != nil:
		return wi.Sprint.Title
	case wi.Issue.Iteration != nil:
		return wi.Issue.Iteration.Title
	}
	return ""
}

func snapshotBlock(wi domain.WorkItem, comments []forge.Note, maxComments int) string {
	var b strings.Builder
	b.WriteString(beginMarker + "\n")
	fmt.Fprintf(&b, "# %s\n\n", oneLine(wi.Issue.Title))
	if len(wi.Issue.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(wi.Issue.Labels, ", "))
	}
	if len(wi.Issue.Assignees) > 0 {
		names := make([]string, len(wi.Issue.Assignees))
		for i, u := range wi.Issue.Assignees {
			names[i] = u.Display()
		}
		fmt.Fprintf(&b, "Assignees: %s\n", strings.Join(names, ", "))
	}
	if desc := strings.TrimSpace(wi.Issue.Description); desc != "" {
		b.WriteString("\n" + desc + "\n")
	}
	if kept := lastComments(comments, maxComments); len(kept) > 0 {
		b.WriteString("\n## Comments\n")
		for _, n := range kept {
			fmt.Fprintf(&b, "\n**%s** · %s\n\n%s\n", n.Author.Display(), n.CreatedAt.UTC().Format("2006-01-02 15:04"), strings.TrimSpace(n.Body))
		}
	}
	b.WriteString("\n" + endMarker + "\n")
	return audit.Redact(b.String())
}

// lastComments keeps the newest max human comments, oldest first.
func lastComments(comments []forge.Note, max int) []forge.Note {
	var human []forge.Note
	for _, n := range comments {
		if !n.System {
			human = append(human, n)
		}
	}
	if max <= 0 {
		return nil
	}
	if len(human) > max {
		human = human[len(human)-max:]
	}
	return human
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// ReadNotes returns the user's text after the "## Notes" heading, or "" when
// the record or heading does not exist.
func (s *Store) ReadNotes(forgeName string, ref forge.Ref) (string, error) {
	data, err := os.ReadFile(s.ItemPath(forgeName, ref))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read notes: %w", err)
	}
	_, notes, _ := notesSplit(string(data))
	return notes, nil
}

// lockDir takes an exclusive flock on the record lock file in dir, waiting
// for a competing holder; the holders only read and rename one small file.
// A missing dir is ErrNoRecord: locking never creates a record.
func lockDir(dir string) (unlock func(), err error) {
	path := filepath.Join(dir, lockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoRecord
	}
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
		f.Close()
	}, nil
}

// writeAtomic writes via a temp file and rename so readers never see a
// partial record.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".item-*.tmp")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// Entry is one record found on disk.
type Entry struct {
	Forge       string
	Dir         string
	Frontmatter Frontmatter
}

// Index lists every item record with its frontmatter, for rebuilding the
// cache. Unreadable or frontmatter-less records are skipped.
func (s *Store) Index() ([]Entry, error) {
	root := filepath.Join(s.dir, "items")
	var out []Entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || d.Name() != ItemFile {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		fm, ok := ParseFrontmatter(string(data))
		if !ok {
			return nil
		}
		rel, _ := filepath.Rel(root, filepath.Dir(p))
		forgeName, _, _ := strings.Cut(rel, string(filepath.Separator))
		out = append(out, Entry{Forge: forgeName, Dir: filepath.Dir(p), Frontmatter: fm})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("index records: %w", err)
	}
	return out, nil
}
