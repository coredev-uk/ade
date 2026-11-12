// Package git wraps the git binary for ADE's clone/worktree layout (PLAN 2.2).
//
// Every command runs with terminal prompts disabled and stderr captured into
// the returned error, so a failure inside the TUI never blocks on a prompt and
// always carries git's own explanation.
package git

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// FetchRefspec is what a bare clone needs so that `fetch` maintains
// remote-tracking refs: a plain `clone --bare` configures none.
const FetchRefspec = "+refs/heads/*:refs/remotes/origin/*"

// preparedKey marks a bare clone whose refspec, fetch and local-head cleanup
// all completed, so an interrupted clone is resumed instead of half-trusted.
const preparedKey = "ade.prepared"

// ErrNoUpstream means a worktree's branch has no usable upstream.
var ErrNoUpstream = errors.New("no upstream")

// Client runs git with extra environment (typically HTTPS auth, see TokenEnv).
type Client struct {
	Env []string
}

// Error is a failed git invocation with its captured stderr.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.Join(strings.Fields(e.Stderr), " ")
	if msg == "" {
		msg = e.Err.Error()
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return "git " + strings.Join(e.Args, " ") + ": " + msg
}

func (e *Error) Unwrap() error { return e.Err }

// ErrCredentials marks failures to obtain a forge token before git even ran.
var ErrCredentials = errors.New("credentials unavailable")

// Reason condenses err into a few words for summaries ("auth", "network", ...).
func Reason(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, ErrCredentials) {
		return "auth"
	}
	var ge *Error
	if !errors.As(err, &ge) {
		return "failed"
	}
	s := strings.ToLower(ge.Stderr)
	has := func(subs ...string) bool {
		for _, sub := range subs {
			if strings.Contains(s, sub) {
				return true
			}
		}
		return false
	}
	switch {
	case has("authentication failed", "could not read username", "terminal prompts disabled",
		"permission denied", "invalid credentials", "error: 401", "error: 403", "access denied"):
		return "auth"
	case has("repository not found", "does not appear to be a git repository",
		"' not found", "could not be found"):
		return "not found"
	case has("could not resolve host", "connection refused", "timed out", "network is unreachable",
		"unable to access", "could not read from remote"):
		return "network"
	}
	return "failed"
}

// TokenEnv returns environment that makes git send HTTP basic credentials to
// remoteURL. Using GIT_CONFIG_* keeps the token out of argv and .git/config.
//
// It also pins remoteURL to itself with url.<remoteURL>.insteadOf: git picks the
// longest matching insteadOf prefix, so this beats a user rule such as
// "https://host/ -> ssh://git@host/" that would silently bypass the token.
func TokenEnv(remoteURL, user, token string) []string {
	return RemoteEnv(remoteURL, user, token, nil)
}

// RemoteEnv is TokenEnv plus extra git config pairs (key, value), such as the
// scoped http.sslCAInfo of a self-hosted forge (see package tlsca). An empty
// token adds no credentials and no insteadOf pin; only extra applies.
func RemoteEnv(remoteURL, user, token string, extra [][2]string) []string {
	var kv [][2]string
	if token != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
		kv = append(kv,
			[2]string{"http.extraHeader", "Authorization: Basic " + cred},
			[2]string{"url." + remoteURL + ".insteadOf", remoteURL})
	}
	kv = append(kv, extra...)
	if len(kv) == 0 {
		return nil
	}
	env := []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(len(kv))}
	for i, p := range kv {
		n := strconv.Itoa(i)
		env = append(env, "GIT_CONFIG_KEY_"+n+"="+p[0], "GIT_CONFIG_VALUE_"+n+"="+p[1])
	}
	return env
}

// Run executes git in dir (empty means the process cwd) and returns stdout.
func (c Client) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Env = append(cmd.Env, c.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.String(), nil
}

func (c Client) line(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := c.Run(ctx, dir, args...)
	return strings.TrimSpace(out), err
}

// CloneBare clones url into dest as a bare repository.
func (c Client) CloneBare(ctx context.Context, url, dest string) error {
	_, err := c.Run(ctx, "", "clone", "--bare", "--quiet", url, dest)
	return err
}

// RemoteURL returns the configured origin URL of the repository at dir.
func (c Client) RemoteURL(ctx context.Context, dir string) (string, error) {
	return c.line(ctx, dir, "config", "--get", "remote.origin.url")
}

// Prepared reports whether PrepareBare completed for the bare clone.
func (c Client) Prepared(ctx context.Context, bare string) bool {
	v, err := c.line(ctx, bare, "config", "--get", preparedKey)
	return err == nil && v == "true"
}

// PrepareBare applies the bare-clone fixes from PLAN 2.2: set the fetch
// refspec, fetch, point HEAD at the default branch and delete every local head
// the clone created except that branch. defaultBranch may be empty to keep the
// remote's HEAD. It returns the default branch.
func (c Client) PrepareBare(ctx context.Context, bare, defaultBranch string) (string, error) {
	if _, err := c.Run(ctx, bare, "config", "--replace-all", "remote.origin.fetch", FetchRefspec); err != nil {
		return "", err
	}
	if err := c.Fetch(ctx, bare); err != nil {
		return "", err
	}
	if defaultBranch != "" {
		if _, err := c.Run(ctx, bare, "symbolic-ref", "HEAD", "refs/heads/"+defaultBranch); err != nil {
			return "", err
		}
	}
	def, err := c.DefaultBranch(ctx, bare)
	if err != nil {
		return "", err
	}
	if err := c.pruneLocalHeads(ctx, bare, def); err != nil {
		return "", err
	}
	if _, err := c.Run(ctx, bare, "config", preparedKey, "true"); err != nil {
		return "", err
	}
	return def, nil
}

func (c Client) pruneLocalHeads(ctx context.Context, bare, keep string) error {
	out, err := c.line(ctx, bare, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return err
	}
	for _, b := range strings.Fields(out) {
		if b == keep {
			continue
		}
		if _, err := c.Run(ctx, bare, "branch", "-D", b); err != nil {
			return err
		}
	}
	return nil
}

// Fetch runs `git fetch --prune origin` in dir.
func (c Client) Fetch(ctx context.Context, dir string) error {
	_, err := c.Run(ctx, dir, "fetch", "--prune", "--quiet", "origin")
	return err
}

// DefaultBranch returns the branch the bare repository's HEAD points at.
func (c Client) DefaultBranch(ctx context.Context, bare string) (string, error) {
	return c.line(ctx, bare, "symbolic-ref", "--short", "HEAD")
}

// WriteGitFile makes <repoDir> usable for repo-wide git commands by pointing a
// .git file at the sibling .bare directory.
func WriteGitFile(repoDir string) error {
	p := filepath.Join(repoDir, ".git")
	if _, err := os.Lstat(p); err == nil {
		return nil
	}
	return os.WriteFile(p, []byte("gitdir: ./.bare\n"), 0o644)
}

// BranchDir maps a branch name to its worktree directory name.
func BranchDir(branch string) string { return strings.ReplaceAll(branch, "/", "-") }

// AddTracking creates a worktree at dir on a new local branch that tracks
// origin/<branch>.
func (c Client) AddTracking(ctx context.Context, bare, dir, branch string) error {
	_, err := c.Run(ctx, bare, "worktree", "add", "--track", "-b", branch, dir, "origin/"+branch)
	return err
}

// PruneWorktrees drops registrations of worktrees whose directory is gone.
func (c Client) PruneWorktrees(ctx context.Context, bare string) error {
	_, err := c.Run(ctx, bare, "worktree", "prune")
	return err
}

// AddBaseline creates the baseline worktree on the default branch. After
// PrepareBare the local default branch exists but has no upstream, so one is set.
func (c Client) AddBaseline(ctx context.Context, bare, dir, branch string) error {
	if _, err := c.Run(ctx, bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return c.AddTracking(ctx, bare, dir, branch)
	}
	if _, err := c.Run(ctx, bare, "worktree", "add", dir, branch); err != nil {
		return err
	}
	_, err := c.Run(ctx, bare, "branch", "--set-upstream-to=origin/"+branch, branch)
	return err
}

// Worktree is one checked-out tree of a repository.
type Worktree struct {
	Path   string
	Branch string // empty when detached
}

// Worktrees lists the non-bare worktrees of the repository at bare.
func (c Client) Worktrees(ctx context.Context, bare string) ([]Worktree, error) {
	out, err := c.Run(ctx, bare, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var (
		trees []Worktree
		cur   *Worktree
	)
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "worktree "):
			trees = append(trees, Worktree{Path: strings.TrimPrefix(l, "worktree ")})
			cur = &trees[len(trees)-1]
		case cur != nil && strings.HasPrefix(l, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(l, "branch "), "refs/heads/")
		case cur != nil && l == "bare":
			trees = trees[:len(trees)-1]
			cur = nil
		}
	}
	return trees, nil
}

// IsClean reports whether the worktree has no modified or untracked files.
func (c Client) IsClean(ctx context.Context, tree string) (bool, error) {
	out, err := c.Run(ctx, tree, "status", "--porcelain")
	return strings.TrimSpace(out) == "", err
}

// Upstream returns the tracked ref of the tree's branch, or ErrNoUpstream.
func (c Client) Upstream(ctx context.Context, tree string) (string, error) {
	up, err := c.line(ctx, tree, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		if ctx.Err() != nil {
			return "", err
		}
		return "", ErrNoUpstream
	}
	return up, nil
}

// AheadBehind counts commits the tree has that its upstream lacks, and vice versa.
func (c Client) AheadBehind(ctx context.Context, tree string) (ahead, behind int, err error) {
	if _, err := c.Upstream(ctx, tree); err != nil {
		return 0, 0, err
	}
	out, err := c.line(ctx, tree, "rev-list", "--left-right", "--count", "HEAD...@{u}")
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", out)
	}
	if ahead, err = strconv.Atoi(f[0]); err != nil {
		return 0, 0, err
	}
	if behind, err = strconv.Atoi(f[1]); err != nil {
		return 0, 0, err
	}
	return ahead, behind, nil
}

// FastForward advances the tree to its upstream; it fails rather than merging.
func (c Client) FastForward(ctx context.Context, tree string) error {
	_, err := c.Run(ctx, tree, "merge", "--ff-only", "--quiet", "@{u}")
	return err
}

// Head returns the abbreviated commit the tree is at.
func (c Client) Head(ctx context.Context, tree string) (string, error) {
	return c.line(ctx, tree, "rev-parse", "--short", "HEAD")
}

// RunHook runs a manifest hook with `sh -c` in tree and returns its combined
// output. It deliberately does not use a Client, so forge credentials never
// reach hook processes.
func RunHook(ctx context.Context, tree, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = tree
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}
