package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
)

// tokenServer answers every request with an empty GraphQL/REST body and
// records the PRIVATE-TOKEN of each in order.
func tokenServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("PRIVATE-TOKEN"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// send performs the call named by req against c.
func send(c *Client, req request, doc string) error {
	ctx := context.Background()
	if doc != "" {
		return c.graphql(ctx, doc, nil, &struct{}{})
	}
	resp, err := c.do(ctx, req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func TestRequestsPickReadOrWriteToken(t *testing.T) {
	tests := []struct {
		name string
		req  request
		doc  string
		want string
	}{
		{"GET", request{method: http.MethodGet, path: "/user"}, "", "read"},
		{"HEAD", request{method: http.MethodHead, path: "/user"}, "", "read"},
		{"POST", request{method: http.MethodPost, path: "/projects/1/issues"}, "", "write"},
		{"PUT", request{method: http.MethodPut, path: "/projects/1/issues/2"}, "", "write"},
		{"DELETE", request{method: http.MethodDelete, path: "/projects/1/issues/2"}, "", "write"},
		{"GraphQL query", request{}, "query Caps { group { id } }", "read"},
		{"GraphQL bare query", request{}, "{ group { id } }", "read"},
		{"GraphQL query after comments", request{}, "# mutation in a comment\n\n  # more\nquery Caps { group { id } }", "read"},
		{"GraphQL mutation", request{}, "mutation Do { x { id } }", "write"},
		{"GraphQL mutation after comments", request{}, "# a note\nmutation Do($i: ID!) { x { id } }", "write"},
		{"GraphQL mutation without a space", request{}, "mutation{ x { id } }", "write"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, tokens := tokenServer(t)
			c := New(srv.URL, "read", WithWriteToken(func(context.Context) (string, error) { return "write", nil }))
			if err := send(c, tc.req, tc.doc); err != nil {
				t.Fatal(err)
			}
			if got := tokens(); len(got) != 1 || got[0] != tc.want {
				t.Errorf("tokens sent = %v, want [%s]", got, tc.want)
			}
		})
	}
}

func TestWriteTokenResolvesLazilyOnce(t *testing.T) {
	post := request{method: http.MethodPost, path: "/projects/1/issues"}
	get := request{method: http.MethodGet, path: "/user"}

	t.Run("never when only reads happen", func(t *testing.T) {
		srv, _ := tokenServer(t)
		var calls atomic.Int32
		c := New(srv.URL, "read", WithWriteToken(func(context.Context) (string, error) { calls.Add(1); return "write", nil }))
		for _, err := range []error{send(c, get, ""), send(c, request{}, "query Q { a }")} {
			if err != nil {
				t.Fatal(err)
			}
		}
		if n := calls.Load(); n != 0 {
			t.Errorf("write token resolved %d times, want 0", n)
		}
	})

	t.Run("at most once across writes", func(t *testing.T) {
		srv, tokens := tokenServer(t)
		var calls atomic.Int32
		c := New(srv.URL, "read", WithWriteToken(func(context.Context) (string, error) { calls.Add(1); return "write", nil }))
		for _, err := range []error{send(c, post, ""), send(c, request{}, "mutation M { a }")} {
			if err != nil {
				t.Fatal(err)
			}
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("write token resolved %d times, want 1", n)
		}
		if got := strings.Join(tokens(), ","); got != "write,write" {
			t.Errorf("tokens sent = %s", got)
		}
	})

	t.Run("a failure fails the write, not the reads, and is retried", func(t *testing.T) {
		srv, tokens := tokenServer(t)
		boom := errors.New("vault sealed")
		var calls atomic.Int32
		c := New(srv.URL, "read", WithWriteToken(func(context.Context) (string, error) {
			if calls.Add(1) == 1 {
				return "", boom
			}
			return "write", nil
		}))
		if err := send(c, get, ""); err != nil {
			t.Fatalf("read: %v", err)
		}
		err := send(c, post, "")
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "gitlab POST /projects/1/issues: write token") {
			t.Fatalf("write err = %v", err)
		}
		if err := send(c, get, ""); err != nil {
			t.Fatalf("read after failed write: %v", err)
		}
		if err := send(c, post, ""); err != nil {
			t.Fatalf("second write: %v", err)
		}
		if got := strings.Join(tokens(), ","); got != "read,read,write" {
			t.Errorf("tokens sent = %s; the failed write must not reach the server", got)
		}
	})

	t.Run("without a write source writes use the single token", func(t *testing.T) {
		srv, tokens := tokenServer(t)
		c := New(srv.URL, "only")
		for _, err := range []error{send(c, post, ""), send(c, request{}, "mutation M { a }"), send(c, get, "")} {
			if err != nil {
				t.Fatal(err)
			}
		}
		if got := strings.Join(tokens(), ","); got != "only,only,only" {
			t.Errorf("tokens sent = %s", got)
		}
	})
}

func TestOpenReadAndWriteTokens(t *testing.T) {
	t.Setenv("ADE_TEST_READ_TOKEN", "read")
	t.Setenv("ADE_TEST_WRITE_TOKEN", "write")
	ctx := context.Background()
	srv, tokens := tokenServer(t)

	c, err := Open(ctx, "gl", config.Forge{
		Kind: "gitlab", URL: srv.URL,
		ReadTokenEnv: "ADE_TEST_READ_TOKEN", TokenEnv: "ADE_TEST_WRITE_TOKEN",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []request{
		{method: http.MethodGet, path: "/user"},
		{method: http.MethodPost, path: "/projects/1/issues"},
	} {
		if err := send(c, req, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(tokens(), ","); got != "read,write" {
		t.Errorf("tokens sent = %s", got)
	}

	t.Run("a single token serves both", func(t *testing.T) {
		srv, tokens := tokenServer(t)
		c, err := Open(ctx, "gl", config.Forge{Kind: "gitlab", URL: srv.URL, TokenEnv: "ADE_TEST_WRITE_TOKEN"})
		if err != nil {
			t.Fatal(err)
		}
		for _, req := range []request{{method: http.MethodGet, path: "/user"}, {method: http.MethodPost, path: "/x"}} {
			if err := send(c, req, ""); err != nil {
				t.Fatal(err)
			}
		}
		if got := strings.Join(tokens(), ","); got != "write,write" {
			t.Errorf("tokens sent = %s", got)
		}
	})

	t.Run("an unset write variable fails the first write only", func(t *testing.T) {
		srv, _ := tokenServer(t)
		c, err := Open(ctx, "gl", config.Forge{
			Kind: "gitlab", URL: srv.URL,
			ReadTokenEnv: "ADE_TEST_READ_TOKEN", TokenEnv: "ADE_TEST_UNSET_TOKEN",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := send(c, request{method: http.MethodGet, path: "/user"}, ""); err != nil {
			t.Fatalf("read: %v", err)
		}
		err = send(c, request{method: http.MethodPost, path: "/x"}, "")
		if err == nil || !strings.Contains(err.Error(), "forge gl write token") {
			t.Errorf("write err = %v", err)
		}
	})

	t.Run("an unset read variable fails Open", func(t *testing.T) {
		_, err := Open(ctx, "gl", config.Forge{Kind: "gitlab", ReadTokenEnv: "ADE_TEST_UNSET_TOKEN", TokenEnv: "ADE_TEST_WRITE_TOKEN"})
		if err == nil || !strings.Contains(err.Error(), "forge gl:") {
			t.Errorf("err = %v", err)
		}
	})
}
