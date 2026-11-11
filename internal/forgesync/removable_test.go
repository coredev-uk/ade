package forgesync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/git"
)

// sandboxBranch is the source branch of the fake's api!1.
const sandboxBranch = "1-rate-limit-the-export-endpoint"

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// layout builds <root>/demo-gl/api the way `ade project add` and start work do:
// a bare clone of a remote that has the MR's branch, and a worktree on it.
// It returns the worktree path.
func (g *glEnv) layout(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	base := t.TempDir()
	origin, work := filepath.Join(base, "origin.git"), filepath.Join(base, "work")
	gitIn(t, base, "init", "--bare", "-b", "main", origin)
	gitIn(t, base, "clone", "--quiet", origin, work)
	gitIn(t, work, "commit", "--quiet", "--allow-empty", "-m", "base")
	gitIn(t, work, "push", "--quiet", "origin", "main")
	gitIn(t, work, "checkout", "--quiet", "-b", sandboxBranch)
	gitIn(t, work, "commit", "--quiet", "--allow-empty", "-m", "the change")
	gitIn(t, work, "push", "--quiet", "origin", sandboxBranch)

	root := filepath.Join(g.h.dir, "root")
	g.h.app.Root = root
	projectDir := filepath.Join(root, "demo-gl")
	repoDir := filepath.Join(projectDir, "api")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot := "source = \"demo-gl.ade.toml\"\nfetched_at = 2026-11-11T12:00:00Z\nlast_update = 2026-11-11T12:00:00Z\nmanifest = '''" + sandboxManifest + "'''\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".project.toml"), []byte(snapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(repoDir, ".bare")
	gitIn(t, base, "clone", "--quiet", "--bare", origin, bare)
	ctx := context.Background()
	c := git.Client{}
	if _, err := c.PrepareBare(ctx, bare, "main"); err != nil {
		t.Fatal(err)
	}
	if err := git.WriteGitFile(repoDir); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(repoDir, sandboxBranch)
	if err := c.AddTracking(ctx, bare, tree, sandboxBranch); err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestEventWorktreeRemovableWhenMyMRMerges(t *testing.T) {
	g := newGLEnv(t, fake.Free)
	tree := g.layout(t)
	g.must(g.srv.MergeMRAs(ref(apiMR1), meUser))

	got := g.next()
	if len(got[EvMRMerged]) != 0 {
		t.Errorf("my own merge raised mr.merged: %+v", got[EvMRMerged])
	}
	evs := got[EvWorktreeRemovable]
	if len(evs) != 1 {
		t.Fatalf("worktree.removable raised %d times; all: %v", len(evs), keysOf(got))
	}
	e := evs[0]
	if e.Severity != events.Info || e.Subject != "ade-demo-env/tracker#1" || e.Project != "demo-gl" ||
		!strings.Contains(e.Title, "api!1 merged") || !strings.Contains(e.Title, "demo-gl/api/"+sandboxBranch) || !strings.Contains(e.Title, "ade gc --yes") {
		t.Errorf("event = %+v", e)
	}
	for _, want := range []string{"ade gc --yes", "ade gc --dry-run", "worktree remove " + tree} {
		if !strings.Contains(e.Detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, e.Detail)
		}
	}
}

func TestEventWorktreeRemovableIsOnlyOfferedWhenSafe(t *testing.T) {
	tests := []struct {
		name  string
		spoil func(t *testing.T, tree string)
	}{
		{"uncommitted changes", func(t *testing.T, tree string) {
			if err := os.WriteFile(filepath.Join(tree, "wip.txt"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"unpushed commits", func(t *testing.T, tree string) {
			gitIn(t, tree, "commit", "--quiet", "--allow-empty", "-m", "local only")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newGLEnv(t, fake.Free)
			tc.spoil(t, g.layout(t))
			g.must(g.srv.MergeMRAs(ref(apiMR1), rae))
			got := g.next()
			if len(got[EvWorktreeRemovable]) != 0 {
				t.Errorf("offered to remove a worktree with %s: %+v", tc.name, got[EvWorktreeRemovable])
			}
			if len(got[EvMRMerged]) != 1 {
				t.Errorf("mr.merged = %+v", got[EvMRMerged])
			}
		})
	}
}

func TestEventNoWorktreeNoOffer(t *testing.T) {
	g := newGLEnv(t, fake.Free)
	g.must(g.srv.MergeMRAs(ref(apiMR1), rae))
	if got := g.next(); len(got[EvWorktreeRemovable]) != 0 {
		t.Errorf("no worktree on disk but raised %+v", got[EvWorktreeRemovable])
	}
}
