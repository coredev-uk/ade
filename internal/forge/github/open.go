package github

import (
	"context"
	"fmt"

	"github.com/coredev-uk/ade/internal/config"
)

// Open builds the client for the configured forge called name: its URL and
// its tokens. Every place ADE talks to GitHub goes through it. The read token
// resolves now; a separate write token resolves on the first write.
func Open(ctx context.Context, name string, f config.Forge, opts ...Option) (*Client, error) {
	tok, err := f.ReadToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("forge %s: %w", name, err)
	}
	if f.HasReadToken() {
		opts = append(opts, WithWriteToken(func(ctx context.Context) (string, error) {
			tok, err := f.Token(ctx)
			if err != nil {
				return "", fmt.Errorf("forge %s write token: %w", name, err)
			}
			return tok, nil
		}))
	}
	return New(f.URL, tok, opts...), nil
}
