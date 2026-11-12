// Package swtest is test support for start work: a git remote that follows the
// fake forge's branches, a forge wrapper that mirrors branch creation into it,
// and a clone laid out the way `ade project add` leaves a repo. Tests in
// startwork and tui share it.
package swtest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/startwork"
)

// GitEnv makes git deterministic and independent of the user's configuration.
func GitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

// Git runs git in dir and fails the test on error.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Remote is the git side of one forge project: a bare repo with a main branch.
type Remote struct {
	t    *testing.T
	Bare string
}

// NewRemote creates <base>/<name>.git with one commit on main.
func NewRemote(t *testing.T, base, name string) *Remote {
	t.Helper()
	r := &Remote{t: t, Bare: filepath.Join(base, name+".git")}
	work := filepath.Join(base, name+"-work")
	Git(t, base, "init", "--bare", "-b", "main", r.Bare)
	Git(t, base, "clone", "--quiet", r.Bare, work)
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	Git(t, work, "add", "README")
	Git(t, work, "commit", "--quiet", "-m", "init")
	Git(t, work, "push", "--quiet", "origin", "main")
	return r
}

// Branch creates a branch at main, the way the forge does for CreateBranch.
func (r *Remote) Branch(name string) { Git(r.t, r.Bare, "branch", name, "main") }

// Clone lays a repo out as `ade project add` does: <root>/<project>/<repo>/
// with a prepared .bare and an upstream-tracking baseline tree.
func Clone(t *testing.T, root, project, repo string, r *Remote) {
	t.Helper()
	ctx := context.Background()
	repoDir := filepath.Join(root, project, repo)
	bare := filepath.Join(repoDir, ".bare")
	gc := git.Client{}
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := gc.CloneBare(ctx, "file://"+r.Bare, bare); err != nil {
		t.Fatal(err)
	}
	if _, err := gc.PrepareBare(ctx, bare, "main"); err != nil {
		t.Fatal(err)
	}
	if err := git.WriteGitFile(repoDir); err != nil {
		t.Fatal(err)
	}
	if err := gc.AddBaseline(ctx, bare, filepath.Join(repoDir, "main"), "main"); err != nil {
		t.Fatal(err)
	}
}

// Mirror wraps a forge so the git remote of each project follows the forge's
// branches, and lets a test make chosen calls fail once.
type Mirror struct {
	startwork.Forge
	// Remotes maps forge project path to its remote.
	Remotes map[string]*Remote

	mu   sync.Mutex
	fail map[string]error
}

// NewMirror wraps f.
func NewMirror(f startwork.Forge) *Mirror {
	return &Mirror{Forge: f, Remotes: map[string]*Remote{}, fail: map[string]error{}}
}

// FailNext makes the next call of method (CreateBranch, LinkIssues, CreateMR)
// return err.
func (m *Mirror) FailNext(method string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail[method] = err
}

func (m *Mirror) take(method string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	err := m.fail[method]
	delete(m.fail, method)
	return err
}

func (m *Mirror) CreateBranch(ctx context.Context, project, name, from string) (forge.Branch, error) {
	if err := m.take("CreateBranch"); err != nil {
		return forge.Branch{}, err
	}
	b, err := m.Forge.CreateBranch(ctx, project, name, from)
	if err == nil {
		m.Remotes[project].Branch(name)
	}
	return b, err
}

func (m *Mirror) LinkIssues(ctx context.Context, a, b forge.Ref, kind string) error {
	if err := m.take("LinkIssues"); err != nil {
		return err
	}
	return m.Forge.LinkIssues(ctx, a, b, kind)
}

func (m *Mirror) CreateMR(ctx context.Context, d forge.MRDraft) (forge.MR, error) {
	if err := m.take("CreateMR"); err != nil {
		return forge.MR{}, err
	}
	return m.Forge.CreateMR(ctx, d)
}

// SetMilestone forwards to the wrapped forge, which the embedded interface
// would otherwise hide.
func (m *Mirror) SetMilestone(ctx context.Context, ref forge.Ref, id int64) error {
	mw, ok := m.Forge.(forge.MilestoneWriter)
	if !ok {
		return forge.ErrUnavailable
	}
	return mw.SetMilestone(ctx, ref, id)
}

// The hierarchy methods forward to the wrapped forge, which the embedded
// interface would otherwise hide; a forge without one is unavailable.

func (m *Mirror) Parents(ctx context.Context, refs []forge.Ref) (map[forge.Ref]forge.Ref, error) {
	pr, ok := m.Forge.(forge.Parenter)
	if !ok {
		return nil, forge.ErrUnavailable
	}
	return pr.Parents(ctx, refs)
}

func (m *Mirror) Children(ctx context.Context, parent forge.Ref) ([]forge.Ref, error) {
	pr, ok := m.Forge.(forge.Parenter)
	if !ok {
		return nil, forge.ErrUnavailable
	}
	return pr.Children(ctx, parent)
}

func (m *Mirror) SetParent(ctx context.Context, child, parent forge.Ref) error {
	if err := m.take("SetParent"); err != nil {
		return err
	}
	pr, ok := m.Forge.(forge.Parenter)
	if !ok {
		return forge.ErrUnavailable
	}
	return pr.SetParent(ctx, child, parent)
}

// ReviewerGroupMembers forwards to the wrapped forge, which the embedded
// interface would otherwise hide; ErrUnavailable when it has none.
func (m *Mirror) ReviewerGroupMembers(ctx context.Context, group string) ([]forge.User, error) {
	rg, ok := m.Forge.(forge.ReviewerGroupReader)
	if !ok {
		return nil, forge.ErrUnavailable
	}
	return rg.ReviewerGroupMembers(ctx, group)
}
