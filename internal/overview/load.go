// Package overview is the live, read-only issue overview behind
// `ade item <ref> --watch`: one work item drawn from the local cache (tracker
// issue, child issues, merge requests, description, acceptance, your notes)
// in a narrow pane. It never touches the network: `ade sync` writes the
// cache and the pane reloads when it changes.
package overview

import (
	"errors"
	"fmt"
	"os"
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
// Safe for concurrent use.
type Loader struct {
	Cache   *store.Store
	Records *records.Store
	Ref     forge.Ref

	mu         sync.Mutex
	fp         fingerprint
	recordPath string
	loaded     bool
}

// fingerprint is what a poll compares: size and modification time of the
// cache database, its write-ahead log (sync writes land there first) and the
// item's record (notes edits).
type fingerprint [3]stamp

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

func (l *Loader) fingerprint() fingerprint {
	return fingerprint{stampOf(l.Cache.Path), stampOf(l.Cache.Path + "-wal"), stampOf(l.recordPath)}
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
	if l.loaded && l.fingerprint() == l.fp {
		return Snapshot{}, false, nil
	}
	snap, err = l.load()
	return snap, true, err
}

// load must be called with l.mu held. The fingerprint is taken before the
// read, so a write that lands during it is seen by the next poll.
func (l *Loader) load() (Snapshot, error) {
	fp := l.fingerprint()
	snap, err := l.read()
	if err == nil || errors.Is(err, ErrNotCached) {
		if snap.NotesPath != "" && snap.NotesPath != l.recordPath {
			l.recordPath = snap.NotesPath
			fp[2] = stampOf(l.recordPath)
		}
		l.fp, l.loaded = fp, true
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
	}
	return snap, nil
}
