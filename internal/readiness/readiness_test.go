package readiness

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPBecomesReady(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) < 3 {
			http.Error(w, "warming up", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent) // any 2xx counts
	}))
	defer srv.Close()
	err := Wait(t.Context(), Spec{Kind: HTTP, Target: srv.URL, Timeout: 5 * time.Second}, "", nil)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if hits.Load() < 3 {
		t.Fatalf("passed after %d hits, before the endpoint answered 2xx", hits.Load())
	}
}

func TestHTTPNon2xxTimesOutWithStatus(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	err := Wait(t.Context(), Spec{Kind: HTTP, Target: srv.URL, Timeout: 300 * time.Millisecond}, "", nil)
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want TimeoutError, got %v", err)
	}
	if !strings.Contains(te.Detail, "404") {
		t.Fatalf("detail %q lacks the status", te.Detail)
	}
}

func TestHTTPRedirectIsNotFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	if ok, _ := Check(t.Context(), Spec{Kind: HTTP, Target: srv.URL}, ""); ok {
		t.Fatal("a 302 must not count as ready")
	}
}

func TestTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if ok, _ := Check(t.Context(), Spec{Kind: TCP, Target: addr}, ""); !ok {
		t.Fatal("open port not ready")
	}
	ln.Close()
	if ok, detail := Check(t.Context(), Spec{Kind: TCP, Target: addr}, ""); ok || detail == "" {
		t.Fatalf("closed port: ok=%v detail=%q", ok, detail)
	}
}

func TestLogMatchesLaterOutput(t *testing.T) {
	log := filepath.Join(t.TempDir(), "out.log")
	go func() {
		time.Sleep(200 * time.Millisecond)
		os.WriteFile(log, []byte("compiling\nready in 412 ms\n"), 0o600) //nolint:errcheck
	}()
	if err := Wait(t.Context(), Spec{Kind: Log, Target: `ready in \d+ ms`, Timeout: 5 * time.Second}, log, nil); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestLogTimeoutReportsLastLine(t *testing.T) {
	log := filepath.Join(t.TempDir(), "out.log")
	os.WriteFile(log, []byte("starting\nconnecting to database\n\n"), 0o600) //nolint:errcheck
	err := Wait(t.Context(), Spec{Kind: Log, Target: "listening", Timeout: 250 * time.Millisecond}, log, nil)
	var te *TimeoutError
	if !errors.As(err, &te) || !strings.Contains(te.Detail, "connecting to database") {
		t.Fatalf("want timeout naming the last log line, got %v", err)
	}
}

func TestDeadProcessFailsFast(t *testing.T) {
	log := filepath.Join(t.TempDir(), "out.log")
	os.WriteFile(log, []byte("panic: address already in use\n"), 0o600) //nolint:errcheck
	start := time.Now()
	err := Wait(t.Context(), Spec{Kind: Log, Target: "listening", Timeout: time.Minute}, log, func() bool { return true })
	var ee *ExitedError
	if !errors.As(err, &ee) || ee.Last != "panic: address already in use" {
		t.Fatalf("want ExitedError with the last line, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("a dead process must not wait out the timeout")
	}
}

func TestDeadProcessThatLoggedReadinessIsReady(t *testing.T) {
	log := filepath.Join(t.TempDir(), "out.log")
	os.WriteFile(log, []byte("listening\n"), 0o600) //nolint:errcheck
	if err := Wait(t.Context(), Spec{Kind: Log, Target: "listening"}, log, func() bool { return true }); err != nil {
		t.Fatalf("log already matched: %v", err)
	}
}

func TestContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := Wait(ctx, Spec{Kind: TCP, Target: "127.0.0.1:1", Timeout: time.Minute}, "", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestBadLogRegexpIsAnError(t *testing.T) {
	if err := Wait(t.Context(), Spec{Kind: Log, Target: "("}, "", nil); err == nil {
		t.Fatal("invalid regexp must fail immediately")
	}
}
