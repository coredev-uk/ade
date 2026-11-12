package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
)

func mrOf(state, head string) forge.MR {
	return forge.MR{Ref: forge.Ref{Project: "grp/api", IID: 7, Kind: forge.KindMR}, State: state, HeadSHA: head}
}

// answer is an MRLookup returning the same MRs for every branch.
func answer(mrs ...forge.MR) MRLookup {
	return func(context.Context, *config.Manifest, string, string) ([]forge.MR, error) { return mrs, nil }
}

// gcEnv is a project "demo" with one repo and one feature worktree.
type gcEnv struct {
	*env
	r    *remote
	tree string
}

func newGCEnv(t *testing.T) *gcEnv {
	t.Helper()
	e := newEnv(t)
	r := newRemote(t, "api")
	r.branch("feature/x")
	if _, err := e.svc.Add(context.Background(), e.manifest("demo", map[string]*remote{"api": r}, map[string]string{"api": `path = "grp/api"`})); err != nil {
		t.Fatal(err)
	}
	return &gcEnv{env: e, r: r, tree: e.featureTree("demo", "api", "feature/x")}
}

func (g *gcEnv) trees(lookup MRLookup) []Tree {
	g.t.Helper()
	ts, err := g.svc.Trees(context.Background(), "", lookup)
	if err != nil {
		g.t.Fatal(err)
	}
	return ts
}

func (g *gcEnv) one(lookup MRLookup) Tree {
	g.t.Helper()
	ts := g.trees(lookup)
	if len(ts) != 1 {
		g.t.Fatalf("trees = %+v, want the one feature worktree (the baseline never counts)", ts)
	}
	return ts[0]
}

func wantKept(t *testing.T, tr Tree, reason string) {
	t.Helper()
	if tr.Removable() {
		t.Fatalf("%s is removable, want kept for %q", tr.Label(), reason)
	}
	if !strings.Contains(strings.Join(tr.Keep, "; "), reason) {
		t.Fatalf("%s kept for %v, want %q", tr.Label(), tr.Keep, reason)
	}
}

func TestGCRemovesMergedCleanPushedWorktree(t *testing.T) {
	g := newGCEnv(t)
	tr := g.one(answer(mrOf("merged", "")))
	if !tr.Removable() || tr.MR != "api!7 merged" || tr.Branch != "feature/x" || tr.Label() != "demo/api/feature-x" {
		t.Fatalf("tree = %+v", tr)
	}
	if got := tr.RemoveCommand(); !strings.Contains(got, "worktree remove "+g.tree) {
		t.Errorf("command = %q", got)
	}

	kept, err := g.svc.RemoveTree(context.Background(), tr)
	if err != nil {
		t.Fatal(err)
	}
	if kept {
		t.Error("the merged-and-pushed branch should have been deleted too")
	}
	if exists(g.tree) {
		t.Error("worktree directory still there")
	}
	if out := gitCmd(t, filepath.Join(g.app.Root, "demo", "api", ".bare"), "branch", "--list", "feature/x"); out != "" {
		t.Errorf("local branch survived: %q", out)
	}
	if len(g.trees(answer(mrOf("merged", "")))) != 0 {
		t.Error("the removed worktree is still listed")
	}
	audited, err := g.app.Audit.Query(audit.Filter{Action: "worktree.remove"})
	if err != nil || len(audited) != 1 || audited[0].Outcome != "ok" || audited[0].Project != "demo" {
		t.Errorf("audit = %+v %v", audited, err)
	}
}

func TestGCRemovesClosedMRWorktrees(t *testing.T) {
	g := newGCEnv(t)
	if tr := g.one(answer(mrOf("closed", ""))); !tr.Removable() || tr.MR != "api!7 closed" {
		t.Fatalf("tree = %+v", tr)
	}
}

func TestGCKeepsAndExplains(t *testing.T) {
	ctx := context.Background()
	t.Run("open MR", func(t *testing.T) {
		g := newGCEnv(t)
		wantKept(t, g.one(answer(mrOf("opened", ""))), "api!7 is still open")
	})
	t.Run("an open MR beats an older merged one", func(t *testing.T) {
		g := newGCEnv(t)
		wantKept(t, g.one(answer(mrOf("merged", ""), mrOf("opened", ""))), "still open")
	})
	t.Run("no MR", func(t *testing.T) {
		g := newGCEnv(t)
		wantKept(t, g.one(answer()), "no merge request for this branch")
	})
	t.Run("forge unreachable", func(t *testing.T) {
		g := newGCEnv(t)
		wantKept(t, g.one(func(context.Context, *config.Manifest, string, string) ([]forge.MR, error) {
			return nil, errors.New("connection refused")
		}), "could not read merge requests: connection refused")
	})
	t.Run("no forge at all", func(t *testing.T) {
		g := newGCEnv(t)
		wantKept(t, g.one(nil), "no forge to ask")
	})
	t.Run("uncommitted changes", func(t *testing.T) {
		g := newGCEnv(t)
		if err := os.WriteFile(filepath.Join(g.tree, "wip.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		wantKept(t, g.one(answer(mrOf("merged", ""))), "uncommitted changes")
	})
	t.Run("unpushed commits", func(t *testing.T) {
		g := newGCEnv(t)
		gitCmd(t, g.tree, "commit", "--quiet", "--allow-empty", "-m", "local only")
		wantKept(t, g.one(answer(mrOf("merged", ""))), "1 commit not pushed")
		if _, err := g.svc.RemoveTree(ctx, g.one(answer(mrOf("merged", "")))); err == nil || !exists(g.tree) {
			t.Errorf("RemoveTree must refuse a kept worktree (err %v)", err)
		}
	})
	t.Run("detached HEAD", func(t *testing.T) {
		g := newGCEnv(t)
		gitCmd(t, g.tree, "checkout", "--quiet", "--detach")
		wantKept(t, g.one(answer(mrOf("merged", ""))), "detached HEAD")
	})
	t.Run("a worktree that went dirty after the verdict is not forced", func(t *testing.T) {
		g := newGCEnv(t)
		tr := g.one(answer(mrOf("merged", "")))
		if err := os.WriteFile(filepath.Join(g.tree, "late.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := g.svc.RemoveTree(ctx, tr); err == nil || !exists(g.tree) {
			t.Errorf("git must refuse a dirty tree (err %v, exists %v)", err, exists(g.tree))
		}
	})
}

// After a squash merge with "delete source branch", the remote branch is gone:
// pruning leaves the tree without an upstream and its commits on no remote
// branch. The forge's final head commit is what proves they were pushed.
func TestGCMergedBranchDeletedOnTheRemote(t *testing.T) {
	g := newGCEnv(t)
	gitCmd(t, g.tree, "commit", "--quiet", "--allow-empty", "-m", "the change")
	gitCmd(t, g.tree, "push", "--quiet", "origin", "feature/x")
	head := gitCmd(t, g.tree, "rev-parse", "HEAD")
	gitCmd(t, g.r.work, "push", "--quiet", "origin", "--delete", "feature/x")
	if _, err := g.svc.Update(context.Background()); err != nil { // fetch --prune
		t.Fatal(err)
	}

	wantKept(t, g.one(answer(mrOf("merged", "0000000000000000000000000000000000000000"))), "never pushed")
	tr := g.one(answer(mrOf("merged", head)))
	if !tr.Removable() {
		t.Fatalf("tree on the MR's final head = %+v", tr)
	}
	kept, err := g.svc.RemoveTree(context.Background(), tr)
	if err != nil || exists(g.tree) {
		t.Fatalf("remove: %v (tree exists %v)", err, exists(g.tree))
	}
	if !kept {
		t.Error("git does not consider an unmerged-looking branch merged: it must stay and be reported")
	}
}

// noUpstreamTree commits locally on the feature tree and drops its upstream,
// the state of a migrated worktree; it returns the new HEAD.
func (g *gcEnv) noUpstreamTree() string {
	g.t.Helper()
	gitCmd(g.t, g.tree, "commit", "--quiet", "--allow-empty", "-m", "local only")
	gitCmd(g.t, g.tree, "branch", "--unset-upstream")
	return gitCmd(g.t, g.tree, "rev-parse", "HEAD")
}

func TestGCBranchWithoutUpstream(t *testing.T) {
	t.Run("a local-only commit keeps the tree", func(t *testing.T) {
		g := newGCEnv(t)
		g.noUpstreamTree()
		wantKept(t, g.one(answer(mrOf("merged", ""))), "never pushed")
	})
	t.Run("a local-only commit is not the MR's head", func(t *testing.T) {
		g := newGCEnv(t)
		g.noUpstreamTree()
		wantKept(t, g.one(answer(mrOf("merged", "0000000000000000000000000000000000000000"))), "never pushed")
	})
	t.Run("HEAD is the head of the MR", func(t *testing.T) {
		g := newGCEnv(t)
		head := g.noUpstreamTree()
		tr := g.one(answer(mrOf("merged", head)))
		if !tr.Removable() || tr.Pushed != "HEAD is api!7's head" {
			t.Fatalf("tree = %+v", tr)
		}
	})
	t.Run("HEAD is on a remote branch", func(t *testing.T) {
		g := newGCEnv(t)
		g.noUpstreamTree()
		gitCmd(t, g.tree, "push", "--quiet", "origin", "feature/x")
		tr := g.one(answer(mrOf("merged", "")))
		if !tr.Removable() || tr.Pushed != "on origin/feature/x" {
			t.Fatalf("tree = %+v", tr)
		}
	})
	t.Run("a remote-tracking ref the remote no longer has does not count", func(t *testing.T) {
		g := newGCEnv(t)
		g.noUpstreamTree()
		gitCmd(t, g.tree, "push", "--quiet", "origin", "feature/x")
		// The bare clone still has origin/feature/x holding HEAD until a fetch --prune.
		gitCmd(t, g.r.work, "push", "--quiet", "origin", "--delete", "feature/x")
		bare := filepath.Join(g.app.Root, "demo", "api", ".bare")
		if out := gitCmd(t, bare, "branch", "-r", "--contains", "HEAD"); !strings.Contains(out, "origin/feature/x") {
			t.Fatalf("setup: the stale ref is gone: %q", out)
		}
		wantKept(t, g.one(answer(mrOf("merged", ""))), "never pushed")
	})
	t.Run("a local branch as upstream does not count as pushed", func(t *testing.T) {
		g := newGCEnv(t)
		g.noUpstreamTree()
		// The tree's only commit is ahead of local main, which is no evidence about the remote.
		gitCmd(t, g.tree, "branch", "--set-upstream-to=main")
		wantKept(t, g.one(answer(mrOf("merged", ""))), "never pushed")
	})
	t.Run("a failed fetch cannot vouch for the commits", func(t *testing.T) {
		g := newGCEnv(t)
		g.noUpstreamTree()
		gitCmd(t, g.tree, "push", "--quiet", "origin", "feature/x")
		gitCmd(t, filepath.Join(g.app.Root, "demo", "api", ".bare"), "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
		wantKept(t, g.one(answer(mrOf("merged", ""))), "fetching the remote failed")
	})
}

func TestGCKeepsATreeWhoseStatusCannotBeRead(t *testing.T) {
	g := newGCEnv(t)
	// Without its .git file the tree resolves to the bare repo above it, where
	// git status refuses to run.
	if err := os.Remove(filepath.Join(g.tree, ".git")); err != nil {
		t.Fatal(err)
	}
	tr := g.one(answer(mrOf("merged", "")))
	wantKept(t, tr, "cannot read the worktree status")
}

func TestTreeOfFindsTheWorktreeAMergedMRCameFrom(t *testing.T) {
	g := newGCEnv(t)
	mr := mrOf("merged", "")
	mr.SourceBranch = "feature/x"
	fetch := g.svc.NewFetcher()
	tr, ok, err := g.svc.TreeOf(context.Background(), "demo", mr, fetch)
	if err != nil || !ok || !tr.Removable() || tr.Path != g.tree {
		t.Fatalf("TreeOf = %+v %v %v", tr, ok, err)
	}
	other := mr
	other.SourceBranch = "feature/else"
	if _, ok, err := g.svc.TreeOf(context.Background(), "demo", other, fetch); err != nil || ok {
		t.Errorf("a branch with no worktree: %v %v", ok, err)
	}
	other = mr
	other.Ref.Project = "grp/web"
	if _, ok, err := g.svc.TreeOf(context.Background(), "demo", other, fetch); err != nil || ok {
		t.Errorf("another repo's MR: %v %v", ok, err)
	}
}
