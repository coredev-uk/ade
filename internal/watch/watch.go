// Package watch tells a long-running view that files it reads have changed:
// the cache database (and its write-ahead log), item records, the audit log.
//
// It uses fsnotify and falls back to polling the files' size and modification
// time where fsnotify cannot work (some network filesystems, an exhausted
// inotify limit). Either way the consumer sees the same thing: a bursty
// stream of changes coalesced into occasional signals on a channel. A signal
// says "something under the watched paths changed"; the consumer re-reads.
package watch

import (
	"context"
	"errors"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Modes a Watcher reports.
const (
	ModeFS   = "fsnotify"
	ModePoll = "poll"
)

// Defaults for Options.
const (
	DefaultDebounce     = 150 * time.Millisecond
	DefaultPollInterval = 2 * time.Second

	// maxDirs bounds the directories one recursive target may hold watches
	// on; past it the watcher falls back to polling instead of eating the
	// inotify limit.
	maxDirs = 4096
)

// Target is a directory to watch. It need not exist yet: the nearest existing
// parent is watched until it appears. Recursive also covers its subdirectories.
type Target struct {
	Path      string
	Recursive bool
}

// Options tune a Watcher; the zero value is right for production.
type Options struct {
	// Debounce is how long a burst of changes must go quiet before it is
	// signalled (DefaultDebounce when zero). A burst that never goes quiet is
	// signalled every five debounce periods.
	Debounce time.Duration
	// PollInterval is the stat interval in poll mode (DefaultPollInterval
	// when zero).
	PollInterval time.Duration
	// NewWatcher builds the fsnotify watcher; fsnotify.NewWatcher when nil.
	// An error selects poll mode.
	NewWatcher func() (*fsnotify.Watcher, error)
}

// Watcher signals on C when something under its targets changed.
type Watcher struct {
	c    chan struct{}
	mode atomic.Value // string
}

// C yields one value per coalesced burst of changes. It is closed once the
// context passed to Watch is done and every resource is released. Nothing is
// signalled for the state at the time Watch was called: start watching, then
// read.
func (w *Watcher) C() <-chan struct{} { return w.c }

// Mode is ModeFS or ModePoll. It can change from fsnotify to poll while
// running, when a watch cannot be added (an inotify limit reached later).
func (w *Watcher) Mode() string { return w.mode.Load().(string) }

// Watch starts watching targets until ctx is done.
func Watch(ctx context.Context, targets []Target, opts Options) (*Watcher, error) {
	if len(targets) == 0 {
		return nil, errors.New("watch: no paths")
	}
	ts := make([]Target, len(targets))
	for i, t := range targets {
		if t.Path == "" {
			return nil, errors.New("watch: empty path")
		}
		ts[i] = Target{Path: filepath.Clean(t.Path), Recursive: t.Recursive}
	}
	if opts.Debounce <= 0 {
		opts.Debounce = DefaultDebounce
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.NewWatcher == nil {
		opts.NewWatcher = fsnotify.NewWatcher
	}

	w := &Watcher{c: make(chan struct{}, 1)}
	set, err := openSet(ts, opts.NewWatcher)
	if err != nil {
		w.mode.Store(ModePoll)
		last := fingerprint(ts) // before returning: a change after Watch is seen
		go func() {
			defer close(w.c)
			w.poll(ctx, ts, opts.PollInterval, last, false)
		}()
		return w, nil
	}
	w.mode.Store(ModeFS)
	go func() {
		defer close(w.c)
		if w.runFS(ctx, set, opts) {
			w.mode.Store(ModePoll)
			w.poll(ctx, ts, opts.PollInterval, fingerprint(ts), true)
		}
	}()
	return w, nil
}

func (w *Watcher) emit() {
	select {
	case w.c <- struct{}{}:
	default: // one is already pending: coalesced
	}
}

// ignored reports changes that are noise: the shared-memory file of SQLite's
// WAL mode changes on reads, and what matters in it lands in the -wal file.
func ignored(name string) bool { return strings.HasSuffix(name, "-shm") }

// --- fsnotify ----------------------------------------------------------------

// watchSet keeps fsnotify watches on the directories the targets need now.
type watchSet struct {
	fsw     *fsnotify.Watcher
	targets []Target
	have    map[string]struct{}
}

func openSet(ts []Target, factory func() (*fsnotify.Watcher, error)) (*watchSet, error) {
	fsw, err := factory()
	if err != nil {
		return nil, err
	}
	s := &watchSet{fsw: fsw, targets: ts, have: map[string]struct{}{}}
	if err := s.sync(); err != nil {
		fsw.Close() //nolint:errcheck // falling back to polling
		return nil, err
	}
	return s, nil
}

// sync brings the watches in line with the disk: each target's directory when
// it exists (with its subdirectories when recursive), otherwise its nearest
// existing parent.
func (s *watchSet) sync() error {
	want := map[string]struct{}{}
	for _, t := range s.targets {
		dir := nearestDir(t.Path)
		want[dir] = struct{}{}
		if t.Recursive && dir == t.Path {
			err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					if errors.Is(err, fs.ErrNotExist) {
						return nil // removed while walking
					}
					return err
				}
				if d.IsDir() {
					if len(want) >= maxDirs {
						return errors.New("watch: too many directories")
					}
					want[p] = struct{}{}
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	for d := range s.have { // deleted directories lose their watch by themselves
		if _, err := os.Stat(d); err != nil {
			delete(s.have, d)
		}
	}
	for d := range want {
		if _, ok := s.have[d]; ok {
			continue
		}
		if err := s.fsw.Add(d); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // gone again; the next event syncs
			}
			return err
		}
		s.have[d] = struct{}{}
	}
	for d := range s.have {
		if _, ok := want[d]; !ok {
			s.fsw.Remove(d) //nolint:errcheck // already gone is fine
			delete(s.have, d)
		}
	}
	return nil
}

// relevant reports whether name is a target, inside one, or on the way to one
// (an ancestor watched while its target does not exist yet); other entries of
// such an ancestor are not our business.
func (s *watchSet) relevant(name string) bool {
	for _, t := range s.targets {
		if name == t.Path || strings.HasPrefix(name, t.Path+string(filepath.Separator)) || strings.HasPrefix(t.Path, name+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// nearestDir is path when it is a directory, else its closest existing parent.
func nearestDir(path string) string {
	for p := path; ; p = filepath.Dir(p) {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
		if filepath.Dir(p) == p {
			return p
		}
	}
}

// runFS serves events until ctx is done (false) or a watch cannot be kept (true:
// the caller continues in poll mode).
func (w *Watcher) runFS(ctx context.Context, s *watchSet, opts Options) (fallback bool) {
	defer s.fsw.Close() //nolint:errcheck // nothing to do about it

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var first time.Time // start of the pending burst
	var resync bool
	pending := func() bool { return !first.IsZero() }
	mark := func() {
		now := time.Now()
		if !pending() {
			first = now
			timer.Reset(opts.Debounce)
		} else if now.Sub(first) < 5*opts.Debounce {
			timer.Reset(opts.Debounce)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return false
		case ev, ok := <-s.fsw.Events:
			if !ok {
				return true
			}
			if ignored(ev.Name) || ev.Op == fsnotify.Chmod || !s.relevant(ev.Name) {
				continue
			}
			if ev.Has(fsnotify.Create) || ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
				resync = true
			}
			mark()
		case _, ok := <-s.fsw.Errors:
			if !ok {
				return true
			}
			mark() // an overflow loses events: say something changed
		case <-timer.C:
			first = time.Time{}
			if resync {
				resync = false
				// Before the signal, so what appeared in a new directory is
				// either watched now or already visible to the re-read.
				if err := s.sync(); err != nil {
					w.emit()
					return true
				}
			}
			w.emit()
		}
	}
}

// --- polling -----------------------------------------------------------------

// poll stats the targets every interval and signals when anything differs.
// changed signals once on entry (the state between the last fsnotify event
// and now is unknown).
func (w *Watcher) poll(ctx context.Context, ts []Target, interval time.Duration, last uint64, changed bool) {
	if changed {
		w.emit()
	}
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			if fp := fingerprint(ts); fp != last {
				last = fp
				w.emit()
			}
		}
	}
}

// fingerprint hashes name, size and modification time of everything under the
// targets (one level for a non-recursive target).
func fingerprint(ts []Target) uint64 {
	h := fnv.New64a()
	var buf [16]byte
	add := func(path string, fi fs.FileInfo) {
		h.Write([]byte(path))
		sz, mt := uint64(fi.Size()), uint64(fi.ModTime().UnixNano())
		for i := range 8 {
			buf[i], buf[8+i] = byte(sz>>(8*i)), byte(mt>>(8*i))
		}
		h.Write(buf[:])
	}
	for _, t := range ts {
		if !t.Recursive {
			es, err := os.ReadDir(t.Path)
			if err != nil {
				continue
			}
			for _, e := range es {
				if ignored(e.Name()) {
					continue
				}
				if fi, err := e.Info(); err == nil {
					add(filepath.Join(t.Path, e.Name()), fi)
				}
			}
			continue
		}
		filepath.WalkDir(t.Path, func(p string, d fs.DirEntry, err error) error { //nolint:errcheck // a vanished file is a change, not an error
			if err != nil || ignored(p) {
				return nil
			}
			if fi, err := d.Info(); err == nil {
				add(p, fi)
			}
			return nil
		})
	}
	return h.Sum64()
}

// Probe reports how Watch would watch dir right now: ModeFS, or ModePoll with
// the reason fsnotify cannot be used (reason is empty for ModeFS).
func Probe(dir string) (mode, reason string) {
	set, err := openSet([]Target{{Path: dir}}, fsnotify.NewWatcher)
	if err != nil {
		return ModePoll, err.Error()
	}
	set.fsw.Close() //nolint:errcheck // only a probe
	return ModeFS, ""
}
