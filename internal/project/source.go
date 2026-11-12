package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/httpdebug"
	"github.com/coredev-uk/ade/internal/tlsca"
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
	overlay, err := s.localOverlay(source)
	if err != nil {
		return nil, nil, err
	}
	if overlay.path != "" {
		if raw, err = config.MergeTOML(raw, source, overlay.data, overlay.path); err != nil {
			return nil, nil, err
		}
	}
	m, err := config.ParseManifest(raw, source)
	if err != nil {
		return nil, nil, err
	}
	m.Overlay = overlay.path
	return m, raw, nil
}

type overlayFile struct {
	path string // "" when there is no overlay
	data []byte
}

// localOverlay finds the local override of the manifest at a canonical source
// (PLAN 2): beside the file for a local path; for a git or https source, which
// has no "beside" on this machine, in the config directory under the
// manifest's file name (shop.ade.toml → <config dir>/shop.ade.local.toml). The
// merged text is what the project snapshot stores, so an override takes effect
// at the next update and every later reader sees it.
func (s *Service) localOverlay(source string) (overlayFile, error) {
	var path string
	if isRemote(source) {
		name := source
		if i := strings.LastIndex(name, "#"); i >= 0 {
			name = name[i+1:]
		}
		name, _, _ = strings.Cut(name, "?")
		path = config.LocalPath(filepath.Join(filepath.Dir(s.App.Config.File), filepath.Base(name)))
	} else {
		path = config.LocalPath(source)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return overlayFile{}, nil
	}
	if err != nil {
		return overlayFile{}, err
	}
	return overlayFile{path: path, data: data}, nil
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

// splitGitSpec separates a "<url>#<path>" git source into the repository URL
// and the manifest's path inside it.
func splitGitSpec(spec string) (repoURL, file string, err error) {
	i := strings.LastIndex(spec, "#")
	if i < 0 || i == len(spec)-1 {
		return "", "", fmt.Errorf("git source %q needs #<path-in-repo>", spec)
	}
	repoURL, file = spec[:i], spec[i+1:]
	if !filepath.IsLocal(filepath.FromSlash(file)) {
		return "", "", fmt.Errorf("manifest path %q escapes the repository", file)
	}
	return repoURL, file, nil
}

// manifestCacheDir is the shallow clone of a git manifest source's repository.
func (s *Service) manifestCacheDir(repoURL string) string {
	sum := sha256.Sum256([]byte(repoURL))
	return filepath.Join(s.App.Paths.CacheDir, "manifests", hex.EncodeToString(sum[:6]))
}

// fetchGit reads <path> from a shallow clone of the repo in spec ("<url>#<path>"),
// cached under <cache>/manifests/ so later updates only fetch.
func (s *Service) fetchGit(ctx context.Context, spec string) ([]byte, error) {
	repoURL, file, err := splitGitSpec(spec)
	if err != nil {
		return nil, err
	}
	c, err := s.client(ctx, "", repoURL)
	if err != nil {
		return nil, err
	}
	dir := s.manifestCacheDir(repoURL)
	if exists(filepath.Join(dir, ".git")) {
		if s.cached {
			return os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
		}
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

// httpCopy is where the last manifest fetched from an https source is kept,
// so that readers that must not wait on the network (dialogs) have one.
func (s *Service) httpCopy(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return filepath.Join(s.App.Paths.CacheDir, "manifests", hex.EncodeToString(sum[:6])+".toml")
}

func (s *Service) fetchHTTP(ctx context.Context, rawURL string) ([]byte, error) {
	if s.cached {
		if b, err := os.ReadFile(s.httpCopy(rawURL)); err == nil {
			return b, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// Credentials only travel over TLS, and only to the forge's own host.
	var transport http.RoundTripper
	if strings.HasPrefix(rawURL, "https://") {
		if name, f, ok := matchForge(s.App.Config, "", rawURL); ok {
			tok, err := s.tokens.get(ctx, name, f, false)
			if err != nil {
				return nil, fmt.Errorf("forge %s token: %w", name, err)
			}
			if f.Kind == "gitlab" {
				req.Header.Set("PRIVATE-TOKEN", tok)
			} else {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			if transport, err = tlsca.Transport(f.CAFile); err != nil {
				return nil, fmt.Errorf("forge %s: %w", name, err)
			}
		}
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second, Transport: httpdebug.Transport(transport)}).Do(req)
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
	// Best effort: without the copy a dialog just fetches.
	if dst := s.httpCopy(rawURL); os.MkdirAll(filepath.Dir(dst), 0o700) == nil {
		_ = os.WriteFile(dst, body, 0o600)
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

// CachedManifest is FetchManifest for readers that must not wait on the
// network: a git or https source already fetched once is read from its local
// copy as of the last sync or update, and fetched only when there is none.
// Local files are read as ever.
func CachedManifest(ctx context.Context, a *app.App, ref config.ProjectRef) (*config.Manifest, error) {
	s := New(a, "")
	s.cached = true
	canon, err := s.canonical(ref.Source)
	if err != nil {
		return nil, err
	}
	m, _, err := s.fetchParsed(ctx, canon)
	return m, err
}

// SnapshotManifest reads the last good manifest snapshot of a registered
// project from disk. It never touches the network, not even for a source that
// was never fetched: such a project has no snapshot and is an error.
func SnapshotManifest(a *app.App, ref config.ProjectRef) (*config.Manifest, error) {
	s := New(a, "")
	canon, err := s.canonical(ref.Source)
	if err != nil {
		return nil, err
	}
	_, sn, err := findSnapshot(a.Root, canon)
	if err != nil {
		return nil, err
	}
	if sn == nil {
		return nil, fmt.Errorf("project %s is not cloned: run `ade update`", ref.Source)
	}
	return sn.parse()
}
