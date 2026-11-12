package project

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/tlsca"
)

// forgeBase is the forge web URL, defaulting per kind for hand-built configs.
func forgeBase(f config.Forge) string {
	base := strings.TrimRight(f.URL, "/")
	if base != "" {
		return base
	}
	switch f.Kind {
	case "gitlab":
		return "https://gitlab.com"
	case "github":
		return "https://github.com"
	}
	return ""
}

// cloneURL is the URL a repo is cloned from: Repo.URL, else built from the
// forge's host, its git protocol and the repo's forge path.
func cloneURL(f config.Forge, r config.Repo) (string, error) {
	if r.URL != "" {
		return r.URL, nil
	}
	if r.Path == "" {
		return "", errors.New("repo has neither url nor path")
	}
	base := forgeBase(f)
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("forge url %q is not usable", base)
	}
	path := strings.TrimSuffix(strings.Trim(r.Path, "/"), ".git")
	if f.GitProtocol == "https" {
		return base + "/" + path + ".git", nil
	}
	if f.SSHPort != 0 && f.SSHPort != 22 {
		return "ssh://git@" + net.JoinHostPort(u.Hostname(), strconv.Itoa(f.SSHPort)) + "/" + path + ".git", nil
	}
	return "git@" + u.Hostname() + ":" + path + ".git", nil
}

// gitUser is the HTTP basic username each forge expects alongside a token.
func gitUser(kind string) string {
	if kind == "github" {
		return "x-access-token"
	}
	return "oauth2"
}

// matchForge picks the configured forge whose host serves rawURL. The manifest's
// own forge wins, so a token is only ever sent to the host it belongs to.
func matchForge(cfg *config.Config, preferred, rawURL string) (name string, f config.Forge, ok bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return "", config.Forge{}, false
	}
	matches := func(n string) bool {
		cand, found := cfg.Forges[n]
		if !found {
			return false
		}
		fu, err := url.Parse(forgeBase(cand))
		return err == nil && strings.EqualFold(fu.Hostname(), u.Hostname())
	}
	if matches(preferred) {
		return preferred, cfg.Forges[preferred], true
	}
	names := make([]string, 0, len(cfg.Forges))
	for n := range cfg.Forges {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if matches(n) {
			return n, cfg.Forges[n], true
		}
	}
	return "", config.Forge{}, false
}

// tokenCache resolves each forge's token once per run: token_cmd may be slow
// (`gh auth token`) and every repo of a forge would otherwise repeat it. The
// read and the write token of a forge are cached apart.
type tokenCache struct {
	mu sync.Mutex
	m  map[tokenKey]tokenEntry
}

type tokenKey struct {
	forge string
	write bool
}

type tokenEntry struct {
	token string
	err   error
}

// get returns the forge's write token when write is set, else its read token.
func (c *tokenCache) get(ctx context.Context, name string, f config.Forge, write bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := tokenKey{name, write}
	if e, ok := c.m[key]; ok {
		return e.token, e.err
	}
	resolve := f.ReadToken
	if write {
		resolve = f.Token
	}
	tok, err := resolve(ctx)
	if err != nil {
		// Not cached: a token_cmd that failed once (a cancelled passphrase)
		// may work on the next push.
		return "", err
	}
	if c.m == nil {
		c.m = map[tokenKey]tokenEntry{}
	}
	c.m[key] = tokenEntry{tok, nil}
	return tok, nil
}

// client returns a git client authorised for rawURL: https remotes on a
// configured forge host get that forge's read token; everything else runs bare.
func (s *Service) client(ctx context.Context, preferredForge, rawURL string) (git.Client, error) {
	return s.authorised(ctx, preferredForge, rawURL, false)
}

// pushClient is client with the forge's write token, for `git push`.
func (s *Service) pushClient(ctx context.Context, preferredForge, rawURL string) (git.Client, error) {
	return s.authorised(ctx, preferredForge, rawURL, true)
}

func (s *Service) authorised(ctx context.Context, preferredForge, rawURL string, write bool) (git.Client, error) {
	if !strings.HasPrefix(rawURL, "https://") {
		return git.Client{}, nil
	}
	name, f, ok := matchForge(s.App.Config, preferredForge, rawURL)
	if !ok {
		return git.Client{}, nil
	}
	tok, err := s.tokens.get(ctx, name, f, write)
	if err != nil {
		return git.Client{}, fmt.Errorf("forge %s token: %w: %w", name, git.ErrCredentials, err)
	}
	return git.Client{Env: git.RemoteEnv(rawURL, gitUser(f.Kind), tok, tlsca.GitSettings(rawURL, f.CAFile))}, nil
}
