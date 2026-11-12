package onboard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLockSerialises(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "setup.lock")
	var inside, overlaps, waits atomic.Int32
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := Lock(context.Background(), path, func() { waits.Add(1) })
			if err != nil {
				t.Error(err)
				return
			}
			if inside.Add(1) > 1 {
				overlaps.Add(1)
			}
			time.Sleep(30 * time.Millisecond)
			inside.Add(-1)
			unlock()
		}()
	}
	wg.Wait()
	if overlaps.Load() != 0 {
		t.Errorf("%d holders overlapped", overlaps.Load())
	}
	if waits.Load() == 0 {
		t.Error("no contender ever reported waiting")
	}
}

func TestLockHonoursContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l")
	unlock, err := Lock(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := Lock(ctx, path, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
}

func TestMissingAdvice(t *testing.T) {
	have := map[string]bool{"git": true, "lazygit": true}
	look := func(n string) (string, error) {
		if have[n] {
			return "/bin/" + n, nil
		}
		return "", errors.New("nope")
	}
	lines, cmd := MissingAdvice(CheckTools(look, t.TempDir()))
	if cmd != "nix profile install nixpkgs#zellij nixpkgs#direnv" {
		t.Errorf("nix command = %q", cmd)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"zellij missing", "nix missing", "https://nixos.org/download", "omp missing", "code missing"} {
		if !strings.Contains(joined, want) {
			t.Errorf("advice lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "git missing") || strings.Contains(joined, "lazygit missing") {
		t.Errorf("present tools reported missing:\n%s", joined)
	}
}

func TestCheckToolsFindsRemoteCodeCLI(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".vscode-server", "bin", "abc123", "bin", "remote-cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "code"), nil, 0o755); err != nil { //nolint:gosec // executable stub
		t.Fatal(err)
	}
	for _, s := range CheckTools(func(string) (string, error) { return "", errors.New("no") }, home) {
		if s.Name == "code" && (s.Missing || !strings.HasSuffix(s.Path, "remote-cli/code")) {
			t.Errorf("code = %+v, want found in the VS Code server dir", s)
		}
	}
}

func TestSplitList(t *testing.T) {
	got := SplitList("a.toml,\n b.toml  git+https://h/x.git#.ade.toml\r\n")
	if len(got) != 3 || got[2] != "git+https://h/x.git#.ade.toml" {
		t.Errorf("got %q", got)
	}
}

func TestFetchTemplate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.toml")
	if err := os.WriteFile(p, []byte("x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := FetchTemplate(context.Background(), p)
	if err != nil || string(b) != "x = 1\n" {
		t.Errorf("local: %q %v", b, err)
	}
	if _, err := FetchTemplate(context.Background(), filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestFetchTemplateURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/team.toml" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("y = 2\n"))
	}))
	defer srv.Close()
	b, err := FetchTemplate(context.Background(), srv.URL+"/team.toml")
	if err != nil || string(b) != "y = 2\n" {
		t.Errorf("got %q %v", b, err)
	}
	if _, err := FetchTemplate(context.Background(), srv.URL+"/nope"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v, want the 404", err)
	}
}
