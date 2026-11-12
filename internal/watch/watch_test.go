package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

var failingWatcher = func() (*fsnotify.Watcher, error) { return nil, errors.New("inotify limit reached") }

// fast is the options of a test: short debounce and poll interval.
func fast(o Options) Options {
	if o.Debounce == 0 {
		o.Debounce = 50 * time.Millisecond
	}
	if o.PollInterval == 0 {
		o.PollInterval = 50 * time.Millisecond
	}
	return o
}

func start(t *testing.T, o Options, ts ...Target) *Watcher {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w, err := Watch(ctx, ts, fast(o))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func signalled(w *Watcher, within time.Duration) bool {
	select {
	case _, ok := <-w.C():
		return ok
	case <-time.After(within):
		return false
	}
}

// both runs a test on the fsnotify watcher and on the polling fallback.
func both(t *testing.T, f func(t *testing.T, o Options, wantMode string)) {
	t.Run("fsnotify", func(t *testing.T) { f(t, Options{}, ModeFS) })
	t.Run("poll", func(t *testing.T) { f(t, Options{NewWatcher: failingWatcher}, ModePoll) })
}

func TestAWriteIsSignalledWithinASecond(t *testing.T) {
	both(t, func(t *testing.T, o Options, mode string) {
		dir := t.TempDir()
		w := start(t, o, Target{Path: dir})
		if w.Mode() != mode {
			t.Fatalf("mode = %q, want %q", w.Mode(), mode)
		}
		if signalled(w, 300*time.Millisecond) {
			t.Fatal("signalled with nothing written")
		}
		write(t, filepath.Join(dir, "cache.db-wal"), "x")
		if !signalled(w, time.Second) {
			t.Fatal("no signal within 1s of a write")
		}
		write(t, filepath.Join(dir, "cache.db-wal"), "xy")
		if !signalled(w, time.Second) {
			t.Fatal("no signal for the second write")
		}
	})
}

func TestSharedMemoryFileChangesAreNoise(t *testing.T) {
	both(t, func(t *testing.T, o Options, _ string) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "cache.db-shm"), "x")
		w := start(t, o, Target{Path: dir})
		write(t, filepath.Join(dir, "cache.db-shm"), "xyz")
		if signalled(w, 400*time.Millisecond) {
			t.Fatal("a -shm change signalled")
		}
	})
}

func TestABurstIsOneSignal(t *testing.T) {
	dir := t.TempDir()
	w := start(t, Options{Debounce: 200 * time.Millisecond}, Target{Path: dir})
	for i := range 20 {
		write(t, filepath.Join(dir, "f"), string(rune('a'+i)))
		time.Sleep(5 * time.Millisecond)
	}
	if !signalled(w, time.Second) {
		t.Fatal("burst not signalled")
	}
	if signalled(w, 500*time.Millisecond) {
		t.Fatal("burst produced more than one signal")
	}
}

func TestABurstThatNeverGoesQuietStillSignals(t *testing.T) {
	dir := t.TempDir()
	w := start(t, Options{Debounce: 100 * time.Millisecond}, Target{Path: dir})
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				os.WriteFile(filepath.Join(dir, "f"), []byte{byte(i)}, 0o600) //nolint:errcheck // test noise
			}
		}
	}()
	if !signalled(w, 2*time.Second) {
		t.Fatal("a continuous stream of writes never signalled")
	}
}

func TestADirectoryThatAppearsLaterIsWatched(t *testing.T) {
	both(t, func(t *testing.T, o Options, _ string) {
		root := t.TempDir()
		target := filepath.Join(root, "data", "records")
		w := start(t, o, Target{Path: target, Recursive: true})

		write(t, filepath.Join(root, "unrelated"), "x")
		if signalled(w, 300*time.Millisecond) {
			t.Fatal("an unrelated file next to the missing directory signalled")
		}

		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if !signalled(w, time.Second) {
			t.Fatal("no signal when the directory appeared")
		}
		write(t, filepath.Join(target, "forge", "item", "item.md"), "# one")
		if !signalled(w, time.Second) {
			t.Fatal("no signal for a write inside the new directory tree")
		}
		write(t, filepath.Join(target, "forge", "item", "item.md"), "# two")
		if !signalled(w, time.Second) {
			t.Fatal("no signal for a rewrite inside the new directory tree")
		}
	})
}

func TestRecursiveTargetsSeeNestedWrites(t *testing.T) {
	both(t, func(t *testing.T, o Options, _ string) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "a", "b", "item.md"), "1")
		w := start(t, o, Target{Path: dir, Recursive: true})
		write(t, filepath.Join(dir, "a", "b", "item.md"), "22")
		if !signalled(w, time.Second) {
			t.Fatal("no signal for a nested write")
		}
	})
}

func TestNonRecursiveTargetsIgnoreSubdirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "sub", "f"), "1")
	w := start(t, Options{}, Target{Path: dir})
	write(t, filepath.Join(dir, "sub", "f"), "22")
	if signalled(w, 400*time.Millisecond) {
		t.Fatal("a write in a subdirectory of a flat target signalled")
	}
}

func TestEveryTargetIsWatched(t *testing.T) {
	both(t, func(t *testing.T, o Options, _ string) {
		a, b := t.TempDir(), t.TempDir()
		w := start(t, o, Target{Path: a}, Target{Path: b})
		write(t, filepath.Join(b, "f"), "x")
		if !signalled(w, time.Second) {
			t.Fatal("no signal from the second target")
		}
	})
}

func TestACancelledContextClosesTheChannelAndLeaksNoGoroutines(t *testing.T) {
	both(t, func(t *testing.T, o Options, _ string) {
		before := runtime.NumGoroutine()
		ctx, cancel := context.WithCancel(context.Background())
		w, err := Watch(ctx, []Target{{Path: t.TempDir(), Recursive: true}}, fast(o))
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case _, ok := <-w.C():
			if ok {
				t.Fatal("signal after cancel")
			}
		case <-time.After(time.Second):
			t.Fatal("channel not closed after cancel")
		}
		deadline := time.Now().Add(time.Second)
		for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if n := runtime.NumGoroutine(); n > before {
			t.Fatalf("goroutines: %d before, %d after cancel", before, n)
		}
	})
}

func TestWatchRejectsNothingToWatch(t *testing.T) {
	if _, err := Watch(context.Background(), nil, Options{}); err == nil {
		t.Error("no targets: want an error")
	}
	if _, err := Watch(context.Background(), []Target{{}}, Options{}); err == nil {
		t.Error("empty path: want an error")
	}
}

func TestDeletingAndRecreatingAWatchedDirectoryKeepsSignalling(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "cache")
	write(t, filepath.Join(dir, "cache.db"), "1")
	w := start(t, Options{}, Target{Path: dir})
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if !signalled(w, time.Second) {
		t.Fatal("no signal for the removal")
	}
	write(t, filepath.Join(dir, "cache.db"), "2")
	if !signalled(w, time.Second) {
		t.Fatal("no signal for the recreated directory")
	}
	write(t, filepath.Join(dir, "cache.db"), "33")
	if !signalled(w, time.Second) {
		t.Fatal("no signal for a write in the recreated directory")
	}
}

func TestProbeSaysHowADirectoryWouldBeWatched(t *testing.T) {
	if mode, reason := Probe(t.TempDir()); mode != ModeFS || reason != "" {
		t.Errorf("Probe = %q, %q; want fsnotify on a temp dir", mode, reason)
	}
	if mode, _ := Probe(filepath.Join(t.TempDir(), "missing")); mode != ModeFS {
		t.Errorf("a missing directory is watched through its parent, got %q", mode)
	}
}
