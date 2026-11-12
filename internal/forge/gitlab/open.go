package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/httpdebug"
	"github.com/coredev-uk/ade/internal/tlsca"
)

// DefaultURL is the public GitLab, used when a forge entry sets no url.
const DefaultURL = "https://gitlab.com"

// Open builds the client for the configured forge called name: its URL (with
// any path prefix such as https://host/gitlab), its token, the CA bundle of a
// self-hosted instance and its request-concurrency cap. Every place ADE talks
// to GitLab goes through it so a self-hosted forge behaves the same
// everywhere. The read token resolves now; a separate write token resolves on
// the first write.
func Open(ctx context.Context, name string, f config.Forge, opts ...Option) (*Client, error) {
	tok, err := f.ReadToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("forge %s: %w", name, err)
	}
	opts = append(Options(f), opts...)
	if f.HasReadToken() {
		opts = append(opts, WithWriteToken(func(ctx context.Context) (string, error) {
			tok, err := f.Token(ctx)
			if err != nil {
				return "", fmt.Errorf("forge %s write token: %w", name, err)
			}
			return tok, nil
		}))
	}
	return New(f.URL, tok, opts...).withDefaultURL(), nil
}

// Options are the client options a forge entry implies, for callers that
// hold a token already (doctor).
func Options(f config.Forge) []Option {
	opts := []Option{WithMaxConcurrency(f.MaxConcurrency)}
	if f.CAFile != "" {
		opts = append(opts, withCAFile(f.CAFile))
	}
	return opts
}

// withCAFile makes the HTTP client trust the PEM bundle at path on top of the
// system pool. A bundle that cannot be read surfaces on the first request.
func withCAFile(path string) Option {
	return func(c *Client) {
		tr, err := tlsca.Transport(path)
		if err != nil {
			c.hc = &http.Client{Timeout: 30 * time.Second, Transport: failingTransport{err}}
			return
		}
		c.hc = &http.Client{Timeout: 30 * time.Second, Transport: httpdebug.Transport(tr)}
	}
}

// failingTransport reports a bad ca_file on every request, so the message
// reaches the user through the normal "forge unreachable" paths.
type failingTransport struct{ err error }

func (t failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

// withDefaultURL falls back to gitlab.com for hand-built configs without a url.
func (c *Client) withDefaultURL() *Client {
	if c.base == "" {
		c.base = DefaultURL
	}
	return c
}
