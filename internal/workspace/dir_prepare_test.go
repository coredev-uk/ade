package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/devshell"
)

// dirManifest: a oneshot in a subdirectory of web, one that prepares itself,
// and a project-level service with its own directory.
const dirManifest = `
name = "demo"
[repos.web]
path = "demo/web"
default_branch = "main"
[repos.web.tasks.where]
cmd  = "pwd > where.txt; echo $ADE_PROBE >> where.txt"
kind = "oneshot"
dir  = "packages/app"
env  = { ADE_PROBE = "{dir}" }
[repos.web.tasks.build]
cmd  = "echo built >> built.txt"
kind = "oneshot"
dir  = "packages/app"
prepare = "mkdir -p node_modules && echo installed >> node_modules/count"
prepare_if_missing = "node_modules"
[repos.web.tasks.always]
cmd  = "true"
kind = "oneshot"
prepare = "echo ran >> prepare-runs"
[repos.web.tasks.broken]
cmd  = "echo started > started.txt"
kind = "oneshot"
prepare = "echo cannot install; exit 7"
prepare_if_missing = "missing"
[repos.web.tasks.nodir]
cmd  = "true"
kind = "oneshot"
dir  = "does/not/exist"
[repos.web.tasks.escape]
cmd  = "true"
kind = "oneshot"
dir  = "link"
[services.db]
cmd  = "pwd > pwd.txt; echo $ADE_PROBE >> pwd.txt"
kind = "oneshot"
dir  = "data"
env  = { ADE_PROBE = "{dir}" }
`

func dirFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t, dirManifest, "web/main", "web/main/packages/app", "data")
	tree := filepath.Join(f.root, "demo", "web", "main")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(tree, "link")); err != nil {
		t.Fatal(err)
	}
	return f, tree
}

func TestNodeDirIsTheWorkingDirectoryOfTasksAndServices(t *testing.T) {
	f, tree := dirFixture(t)
	plan, res := f.up("web/main", PlanOptions{Nodes: []string{"web.where", "services.db"}})
	for _, r := range res {
		if r.Outcome != OutcomeReady {
			t.Fatalf("%s: %s %s", r.Node, r.Outcome, r.Detail)
		}
	}
	// {dir} stays the worktree root; the process runs in the node's dir.
	appDir := filepath.Join(tree, "packages", "app")
	if got, want := readFile(t, filepath.Join(appDir, "where.txt")), appDir+"\n"+tree+"\n"; got != want {
		t.Errorf("web.where saw %q, want %q", got, want)
	}
	dataDir := filepath.Join(f.root, "demo", "data")
	if got, want := readFile(t, filepath.Join(dataDir, "pwd.txt")), dataDir+"\n"+filepath.Join(f.root, "demo")+"\n"; got != want {
		t.Errorf("services.db saw %q, want %q", got, want)
	}
	in, _ := plan.Instance("web.where")
	if in.Dir != appDir || in.Tree.Path != tree {
		t.Errorf("instance dir = %s, tree = %s", in.Dir, in.Tree.Path)
	}
	// A result and a row name the worktree, not the subdirectory.
	if r := byNode(res, "web.where"); r.Tree != tree {
		t.Errorf("result tree = %s, want %s", r.Tree, tree)
	}
	rows, err := f.mgr.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Node == "web.where" && r.Tree != tree {
			t.Errorf("row tree = %s, want %s", r.Tree, tree)
		}
	}
}

func TestNodeDirThatDoesNotExistFailsTheNodeWithAReason(t *testing.T) {
	f, tree := dirFixture(t)
	_, res := f.up("web/main", PlanOptions{Nodes: []string{"web.nodir"}})
	r := byNode(res, "web.nodir")
	if r.Outcome != OutcomeFailed || !strings.Contains(r.Detail, filepath.Join(tree, "does", "not", "exist")) || !strings.Contains(r.Detail, "does not exist") {
		t.Fatalf("result: %+v", r)
	}
}

func TestNodeDirCannotLeaveTheTreeThroughASymlink(t *testing.T) {
	f, _ := dirFixture(t)
	_, err := f.mgr.Plan(context.Background(), f.loc("web/main"), PlanOptions{Nodes: []string{"web.escape"}})
	if err == nil || !strings.Contains(err.Error(), "outside the worktree") {
		t.Fatalf("err = %v", err)
	}
}

func TestNodeDirChangesDirectoryInsideTheDevShell(t *testing.T) {
	f, tree := dirFixture(t)
	f.mgr.Devshell = devshell.Finder{
		LookPath: func(string) (string, error) { return "/bin/direnv", nil },
		ReadFile: func(string) ([]byte, error) { return []byte("export X=1\n"), nil },
	}
	if err := os.WriteFile(filepath.Join(tree, ".envrc"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := f.mgr.Plan(context.Background(), f.loc("web/main"), PlanOptions{Nodes: []string{"web.where"}})
	if err != nil {
		t.Fatal(err)
	}
	in, _ := plan.Instance("web.where")
	if in.Devshell.Kind != devshell.Direnv || in.Devshell.Dir != tree {
		t.Fatalf("devshell = %+v: the shell is the worktree's, not the node's directory", in.Devshell)
	}
	argv := in.command(context.Background(), in.Cmd).Args
	if len(argv) < 6 || argv[0] != "direnv" || argv[1] != "exec" || argv[2] != tree {
		t.Fatalf("argv = %q", argv)
	}
	script := argv[len(argv)-1]
	if want := "cd " + shellQuote(filepath.Join(tree, "packages", "app")) + " || exit 1\n"; !strings.HasPrefix(script, want) {
		t.Errorf("script %q does not start by changing into the node's directory", script)
	}
}

// lines counts the lines of path (0 when it does not exist).
func lines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

func TestPrepareRunsOnlyWhileThePathIsMissing(t *testing.T) {
	f, tree := dirFixture(t)
	holder := tree
	ctx := context.Background()
	appDir := filepath.Join(tree, "packages", "app")
	var audited []string
	up := func() Result {
		t.Helper()
		plan, err := f.mgr.Plan(ctx, f.loc("web/main"), PlanOptions{Nodes: []string{"web.build"}})
		if err != nil {
			t.Fatal(err)
		}
		res, err := f.mgr.Up(ctx, plan, UpOptions{Holder: holder, Prepared: func(in *Instance, command string, err error) {
			audited = append(audited, in.Node.ID+": "+command)
			if err != nil {
				t.Errorf("prepare reported %v", err)
			}
		}})
		if err != nil {
			t.Fatal(err)
		}
		r := byNode(res, "web.build")
		if r.Outcome != OutcomeReady {
			t.Fatalf("%s %s", r.Outcome, r.Detail)
		}
		if _, err := f.mgr.Release(ctx, holder); err != nil { // a oneshot only re-runs once nobody holds it
			t.Fatal(err)
		}
		return r
	}

	// Missing: prepare runs in the node's dir, before the command, and its output leads the log.
	first := up()
	if !first.Prepared {
		t.Fatal("prepare did not run although node_modules was missing")
	}
	count := filepath.Join(appDir, "node_modules", "count")
	if n := lines(t, count); n != 1 {
		t.Fatalf("prepare ran %d times, want 1", n)
	}
	if n := lines(t, filepath.Join(appDir, "built.txt")); n != 1 {
		t.Fatalf("command ran %d times, want 1", n)
	}
	if log := readFile(t, first.Log); !strings.HasPrefix(log, "ade: prepare: mkdir -p node_modules") {
		t.Errorf("log: %q", log)
	}

	// Present: skipped, but the command still runs.
	second := up()
	if second.Prepared {
		t.Error("prepare ran although node_modules exists")
	}
	if n := lines(t, count); n != 1 {
		t.Errorf("prepare ran again: %d lines", n)
	}
	if n := lines(t, filepath.Join(appDir, "built.txt")); n != 2 {
		t.Errorf("command ran %d times, want 2", n)
	}

	// Gone again: runs again.
	if err := os.RemoveAll(filepath.Join(appDir, "node_modules")); err != nil {
		t.Fatal(err)
	}
	if third := up(); !third.Prepared {
		t.Error("prepare did not run again after node_modules was removed")
	}
	if len(audited) != 2 {
		t.Errorf("prepare reported %d times, want 2: %v", len(audited), audited)
	}
}

func TestPrepareWithoutAPathRunsOnEveryStart(t *testing.T) {
	f, tree := dirFixture(t)
	ctx := context.Background()
	for range 2 {
		plan, err := f.mgr.Plan(ctx, f.loc("web/main"), PlanOptions{Nodes: []string{"web.always"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.mgr.Up(ctx, plan, UpOptions{Holder: tree}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.mgr.Release(ctx, tree); err != nil {
			t.Fatal(err)
		}
	}
	if n := lines(t, filepath.Join(tree, "prepare-runs")); n != 2 {
		t.Fatalf("prepare ran %d times, want 2", n)
	}
}

func TestFailedPrepareStopsTheNodeBeforeItStarts(t *testing.T) {
	f, tree := dirFixture(t)
	_, res := f.up("web/main", PlanOptions{Nodes: []string{"web.broken"}})
	r := byNode(res, "web.broken")
	if r.Outcome != OutcomeFailed || !r.Prepared {
		t.Fatalf("result: %+v", r)
	}
	for _, want := range []string{`prepare "echo cannot install; exit 7" failed`, "exit status 7", "last log line: cannot install"} {
		if !strings.Contains(r.Detail, want) {
			t.Errorf("detail %q lacks %q", r.Detail, want)
		}
	}
	if _, err := os.Stat(filepath.Join(tree, "started.txt")); err == nil {
		t.Error("the node started although its prepare failed")
	}
}
