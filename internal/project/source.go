package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
)

const maxManifestBytes = 1 << 20

func isRemote(source string) bool {
	return strings.HasPrefix(source, "git+") || strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://")
}

// NormalizeSource makes a user-typed local path absolute against the current
// directory, so it still means the same file once stored in the config.
func NormalizeSource(source string) (string, error) {
	if isRemote(source) || strings.HasPrefix(source, "~") {
		return source, nil
	}
	return filepath.Abs(source)
}

// canonical resolves a configured source to the form recorded in snapshots:
// remote sources verbatim, local paths resolved against the config file.
func (s *Service) canonical(source string) (string, error) {
	if isRemote(source) {
		return source, nil
	}
	return s.App.Config.ResolvePath(source)
}

// fetchParsed fetches and parses the manifest at a canonical source.
func (s *Service) fetchParsed(ctx context.Context, source string) (*config.Manifest, []byte, error) {
	raw, err := s.fetchRaw(ctx, source)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch manifest %s: %w", source, err)
	}
	m, err := config.ParseManifest(raw, source)
	if err != nil {
		return nil, nil, err
	}
	return m, raw, nil
}

// loadManifest fetches, parses and fully validates the manifest at a canonical
// source. Any validation error rejects it before ADE touches the disk.
func (s *Service) loadManifest(ctx context.Context, source string) (*config.Manifest, []byte, error) {
	m, raw, err := s.fetchParsed(ctx, source)
	if err != nil {
		return nil, nil, err
	}
	if err := validate(m, s.App.Config); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", source, err)
	}
	return m, raw, nil
}

func (s *Service) fetchRaw(ctx context.Context, source string) ([]byte, error) {
	switch {
	case strings.HasPrefix(source, "git+"):
		return s.fetchGit(ctx, strings.TrimPrefix(source, "git+"))
	case strings.HasPrefix(source, "https://"), strings.HasPrefix(source, "http://"):
		return s.fetchHTTP(ctx, source)
	}
	return os.ReadFile(source)
}

// fetchGit reads <path> from a shallow clone of the repo in spec ("<url>#<path>"),
// cached under <cache>/manifests/ so later updates only fetch.
func (s *Service) fetchGit(ctx context.Context, spec string) ([]byte, error) {
	i := strings.LastIndex(spec, "#")
	if i < 0 || i == len(spec)-1 {
		return nil, fmt.Errorf("git source %q needs #<path-in-repo>", spec)
	}
	repoURL, file := spec[:i], spec[i+1:]
	if !filepath.IsLocal(filepath.FromSlash(file)) {
		return nil, fmt.Errorf("manifest path %q escapes the repository", file)
	}
	c, err := s.client(ctx, "", repoURL)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(repoURL))
	dir := filepath.Join(s.App.Paths.CacheDir, "manifests", hex.EncodeToString(sum[:6]))
	if exists(filepath.Join(dir, ".git")) {
		if _, err := c.Run(ctx, dir, "fetch", "--quiet", "--depth", "1", "origin", "HEAD"); err != nil {
			return nil, err
		}
		if _, err := c.Run(ctx, dir, "reset", "--quiet", "--hard", "FETCH_HEAD"); err != nil {
			return nil, err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return nil, err
		}
		if _, err := c.Run(ctx, "", "clone", "--quiet", "--depth", "1", repoURL, dir); err != nil {
			os.RemoveAll(dir) //nolint:errcheck // a partial clone must not look cached
			return nil, err
		}
	}
	return os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
}

func (s *Service) fetchHTTP(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// Credentials only travel over TLS, and only to the forge's own host.
	if strings.HasPrefix(rawURL, "https://") {
		if name, f, ok := matchForge(s.App.Config, "", rawURL); ok {
			tok, err := s.tokens.get(ctx, name, f)
			if err != nil {
				return nil, fmt.Errorf("forge %s token: %w", name, err)
			}
			if f.Kind == "gitlab" {
				req.Header.Set("PRIVATE-TOKEN", tok)
			} else {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
		}
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxManifestBytes {
		return nil, fmt.Errorf("manifest larger than %d bytes", maxManifestBytes)
	}
	return body, nil
}

// FetchManifest fetches and parses the manifest a registered project points
// at, without validating it or touching any project on disk (git sources use
// the manifest cache). Callers validate with Manifest.Validate / ValidateAll.
func FetchManifest(ctx context.Context, a *app.App, ref config.ProjectRef) (*config.Manifest, error) {
	s := New(a, "")
	canon, err := s.canonical(ref.Source)
	if err != nil {
		return nil, err
	}
	m, _, err := s.fetchParsed(ctx, canon)
	return m, err
}
