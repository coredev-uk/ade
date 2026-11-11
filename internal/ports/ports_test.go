package ports

import (
	"net"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/coredev-uk/ade/internal/store"
)

func newRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cache.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return NewRegistry(s), path
}

func TestAssignIsStablePerKeyAndAcrossReopen(t *testing.T) {
	r, path := newRegistry(t)
	a, err := r.Assign("api.dev@/trees/api/main", false)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := r.Assign("api.dev@/trees/api/main", false)
	if a != again {
		t.Fatalf("port changed between calls: %d then %d", a, again)
	}
	s2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if p, ok, err := NewRegistry(s2).Lookup("api.dev@/trees/api/main"); err != nil || !ok || p != a {
		t.Fatalf("allocation not persisted: %d %v %v", p, ok, err)
	}
}

func TestDistinctKeysGetDistinctPorts(t *testing.T) {
	r, _ := newRegistry(t)
	seen := map[int]string{}
	for _, k := range []string{"web.dev@/a", "web.dev@/b", "api.dev@/c", "services.db@/d"} {
		p, err := r.Assign(k, false)
		if err != nil {
			t.Fatal(err)
		}
		if other, dup := seen[p]; dup {
			t.Fatalf("%s and %s share port %d", k, other, p)
		}
		seen[p] = k
	}
}

func TestBusyPortIsReplacedUnlessHeld(t *testing.T) {
	r, _ := newRegistry(t)
	p, err := r.Assign("web.dev@/a", false)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Our own live instance is the one bound to it: keep the port.
	if got, _ := r.Assign("web.dev@/a", true); got != p {
		t.Fatalf("held port was moved: %d -> %d", p, got)
	}
	// Someone else is bound to it: move.
	got, err := r.Assign("web.dev@/a", false)
	if err != nil {
		t.Fatal(err)
	}
	if got == p {
		t.Fatalf("port %d is busy but was handed out again", p)
	}
	if stored, _, _ := r.Lookup("web.dev@/a"); stored != got {
		t.Fatalf("replacement not persisted: stored %d, returned %d", stored, got)
	}
}

func TestAvailable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if Available(port) {
		t.Fatal("bound port reported available")
	}
	ln.Close()
	if !Available(port) {
		t.Fatal("released port reported busy")
	}
}
