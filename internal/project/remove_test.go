package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/audit"
)

func (g *gcEnv) plan(ref string) RemovePlan {
	g.t.Helper()
	p, err := g.svc.PlanRemove(context.Background(), ref)
	if err != nil {
		g.t.Fatal(err)
	}
	return p
}

func TestRemoveDeletesCleanProjectAndItsConfigEntryOnly(t *testing.T) {
	g := newGCEnv(t)
	// The user's config: a comment, another project, and the one Add appended.
	cfg := g.app.Config.File
	before, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	other := "\n# my other project\n[[projects]]\nsource = \"/somewhere/else.ade.toml\"\n"
	if err := os.WriteFile(cfg, append([]byte("# my config\n"), append(before, []byte(other)...)...), 0o600); err != nil {
		t.Fatal(err)
	}

	p := g.plan("demo")
	if len(p.Blockers) != 0 || !p.Registered || p.Repos != 1 || p.Dir != filepath.Join(g.app.Root, "demo") {
		t.Fatalf("plan = %+v", p)
	}
	if err := g.svc.Remove(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	if exists(p.Dir) {
		t.Error("project directory still there")
	}
	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if want := "# my config\n\n# my other project\n[[projects]]\nsource = \"/somewhere/else.ade.toml\"\n"; string(got) != want {
		t.Errorf("config after remove:\n%q\nwant\n%q", got, want)
	}
	if n := len(g.app.Config.Projects); n != 0 {
		// the other entry was appended to the file by hand, not loaded into the in-memory config
		t.Errorf("in-memory projects = %d, want the removed one gone", n)
	}
	es, _ := g.app.Audit.Query(audit.Filter{Action: "project.remove"})
	if len(es) != 1 || es[0].Outcome != "ok" || es[0].Project != "demo" {
		t.Errorf("audit = %+v", es)
	}
}

func TestRemoveRefusesWorkThatExistsOnlyHere(t *testing.T) {
	ctx := context.Background()
	t.Run("dirty and unpushed worktrees", func(t *testing.T) {
		g := newGCEnv(t)
		if err := os.WriteFile(filepath.Join(g.tree, "wip.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, g.tree, "add", "wip.txt")
		gitCmd(t, g.tree, "commit", "--quiet", "-m", "local only")
		if err := os.WriteFile(filepath.Join(g.tree, "more.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		p := g.plan("demo")
		if len(p.Blockers) != 1 || p.Blockers[0].Where != filepath.Join("api", "feature-x") {
			t.Fatalf("blockers = %+v", p.Blockers)
		}
		why := strings.Join(p.Blockers[0].Reasons, "; ")
		if !strings.Contains(why, "uncommitted changes") || !strings.Contains(why, "1 commit not pushed") {
			t.Errorf("reasons = %q", why)
		}
		if err := g.svc.Remove(ctx, p, false); err == nil || !exists(p.Dir) {
			t.Fatalf("Remove without force: err %v, dir exists %v", err, exists(p.Dir))
		}
		es, _ := g.app.Audit.Query(audit.Filter{Action: "project.remove"})
		if len(es) != 1 || es[0].Outcome != "error" {
			t.Errorf("a refusal is audited: %+v", es)
		}
		if err := g.svc.Remove(ctx, p, true); err != nil || exists(p.Dir) {
			t.Fatalf("Remove with force: %v (dir exists %v)", err, exists(p.Dir))
		}
	})
	t.Run("a local branch nobody pushed", func(t *testing.T) {
		g := newGCEnv(t)
		bare := filepath.Join(g.app.Root, "demo", "api", ".bare")
		gitCmd(t, g.tree, "commit", "--quiet", "--allow-empty", "-m", "idea")
		gitCmd(t, bare, "branch", "idea", "feature/x") // a branch with that commit, no worktree
		gitCmd(t, g.tree, "reset", "--quiet", "--hard", "origin/feature/x")
		p := g.plan("demo")
		if len(p.Blockers) != 1 || p.Blockers[0].Where != "api branch idea" {
			t.Fatalf("blockers = %+v", p.Blockers)
		}
	})
	t.Run("clean pushed trees are no blocker", func(t *testing.T) {
		g := newGCEnv(t)
		if p := g.plan("demo"); len(p.Blockers) != 0 {
			t.Fatalf("blockers = %+v", p.Blockers)
		}
	})
}

func TestRemoveByManifestSourceAndUnknownName(t *testing.T) {
	g := newGCEnv(t)
	if _, err := g.svc.PlanRemove(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "not cloned") {
		t.Errorf("unknown project: %v", err)
	}
	src := filepath.Join(g.dir, "demo.ade.toml")
	if p := g.plan(src); p.Name != "demo" {
		t.Errorf("by source: %+v", p)
	}
}

func TestRemoveOnlyDeletesProjectDirectories(t *testing.T) {
	g := newGCEnv(t)
	victim := filepath.Join(g.dir, "precious")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []RemovePlan{
		{Name: "demo", Dir: victim},
		{Name: "../precious", Dir: filepath.Join(g.app.Root, "..", "precious")},
		{Name: "no-snapshot", Dir: filepath.Join(g.app.Root, "no-snapshot")},
	} {
		if err := g.svc.Remove(context.Background(), p, true); err == nil {
			t.Errorf("Remove(%+v) did not refuse", p)
		}
	}
	if !exists(victim) {
		t.Error("an unrelated directory was deleted")
	}
}
