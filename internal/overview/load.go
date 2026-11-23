// Package overview is the live, read-only issue overview behind
// `ade item <ref> --watch`: one work item drawn from the local cache (tracker
// issue, child issues, merge requests, description, acceptance, your notes)
// in a narrow pane. It never touches the network: `ade sync` writes the
// cache and the pane reloads when it changes (see internal/watch).
package overview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/store"
)

// ErrNotCached is the lookup failure: no cached work item owns the ref.
var ErrNotCached = errors.New("not in the cache (run `ade sync`)")

// Snapshot is everything the view draws, read at one moment.
type Snapshot struct {
	Ref      forge.Ref // as asked for
	Found    bool
	Item     domain.WorkItem
	Window   domain.Window
	Forge    string
	SyncedAt time.Time
	Notes    string
	// NotesPath is the record file ("e" opens it); empty when there is none.
	NotesPath string
	// Comments are the tracker issue's human comments the last sync kept,
	// oldest first.
	Comments []records.Comment
}

// Find returns the cached work item that owns ref: its tracker issue, one of
// its child issues or one of its merge requests.
func Find(results []forgesync.Result, ref forge.Ref) (res forgesync.Result, it domain.WorkItem, ok bool) {
	for _, r := range results {
		for _, wi := range r.Items {
			if owns(wi, ref) {
				return r, wi, true
			}
		}
	}
	return forgesync.Result{}, domain.WorkItem{}, false
}

func owns(wi domain.WorkItem, ref forge.Ref) bool {
	if wi.Ref == ref {
		return true
	}
	for _, c := range wi.Children {
		if c.Ref == ref {
			return true
		}
	}
	for _, mr := range wi.MRs {
		if mr.Ref == ref {
			return true
		}
	}
	return false
}

// Loader reads snapshots and tells, cheaply, when a new one is worth reading.
// Safe for concurrent use. Poll holds one connection of Cache (so a pool
// limited to one, store.OpenReadOnly, does not do) until Close.
type Loader struct {
	Cache   *store.Store
	Records *records.Store
	Ref     forge.Ref

	mu         sync.Mutex
	conn       *sql.Conn // held: PRAGMA data_version is per connection
	fp         fingerprint
	recordPath string
	loaded     bool
}

// fingerprint is what a Poll compares: the cache's data version and the size
// and modification time of the item's record (notes edits) and of its comments
// file. The version moves on every commit by another connection, in this
// process or another. The database files' stamps would not do: a commit writes
// the write-ahead log before readers can see it, so a read in between sees the
// old rows under the new stamps, and no later change of the files says the
// rows moved.
type fingerprint struct {
	data             int64
	record, comments stamp
}

type stamp struct {
	mod  int64
	size int64
}

func stampOf(path string) stamp {
	if path == "" {
		return stamp{}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{fi.ModTime().UnixNano(), fi.Size()}
}

// fingerprint must be called with l.mu held. ok is false when the data
// version cannot be read: the caller then reads the cache regardless.
func (l *Loader) fingerprint() (fp fingerprint, ok bool) {
	fp.record, fp.comments = stampOf(l.recordPath), stampOf(commentsPath(l.recordPath))
	if l.conn == nil {
		c, err := l.Cache.DB.Conn(context.Background())
		if err != nil {
			return fp, false
		}
		l.conn = c
	}
	if err := l.conn.QueryRowContext(context.Background(), "PRAGMA data_version").Scan(&fp.data); err != nil {
		return fp, false
	}
	return fp, true
}

// Close releases the connection the data version is read on.
func (l *Loader) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return nil
	}
	err := l.conn.Close()
	l.conn = nil
	return err
}

// commentsPath is the comments file beside a record file.
func commentsPath(record string) string {
	if record == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(record), records.CommentsFile)
}

// Load reads the cache and the record now.
func (l *Loader) Load() (Snapshot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.load()
}

// Poll loads only when the cache or the record changed since the last load;
// reloaded is false (and the snapshot empty) otherwise.
func (l *Loader) Poll() (snap Snapshot, reloaded bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if fp, ok := l.fingerprint(); ok && l.loaded && fp == l.fp {
		return Snapshot{}, false, nil
	}
	snap, err = l.load()
	return snap, true, err
}

// load must be called with l.mu held. The fingerprint is taken before the
// read, so a write that lands during it is seen by the next poll.
func (l *Loader) load() (Snapshot, error) {
	fp, ok := l.fingerprint()
	snap, err := l.read()
	if err == nil || errors.Is(err, ErrNotCached) {
		if snap.NotesPath != "" && snap.NotesPath != l.recordPath {
			l.recordPath = snap.NotesPath
			fp.record, fp.comments = stampOf(l.recordPath), stampOf(commentsPath(l.recordPath))
		}
		l.fp, l.loaded = fp, ok
	}
	return snap, err
}

func (l *Loader) read() (Snapshot, error) {
	snap := Snapshot{Ref: l.Ref}
	results, err := forgesync.Load(l.Cache)
	if err != nil {
		return snap, err
	}
	res, it, ok := Find(results, l.Ref)
	if !ok {
		return snap, ErrNotCached
	}
	snap.Found, snap.Item, snap.Window, snap.Forge, snap.SyncedAt = true, it, res.Window, res.Forge, res.SyncedAt
	if l.Records != nil {
		rec, err := l.Records.Read(res.Forge, it.Ref)
		switch {
		case err == nil:
			snap.Notes, snap.NotesPath = strings.TrimSpace(rec.Notes), rec.Path
		case errors.Is(err, records.ErrNoRecord):
			snap.NotesPath = rec.Path // the file may appear later; watch it
		default:
			return snap, fmt.Errorf("read notes: %w", err)
		}
		if snap.Comments, err = l.Records.ReadComments(res.Forge, it.Ref); err != nil {
			return snap, fmt.Errorf("read comments: %w", err)
		}
	}
	return snap, nil
}
