package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/git"
)

const gcManifest = `
name  = "demo-gl"
forge = "sandbox"

[tracker]
project    = "ade-demo-env/tracker"
iterations = { source = "milestones" }

[repos.api]
path = "ade-demo-env/api"
app  = ["api"]

[repos.web]
path = "ade-demo-env/web"
app  = ["web"]
`

func gitAt(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// gcLayout builds <root>/demo-gl with a bare clone per repo and one feature
// worktree each: api on the branch of api!1 (open in the fake) tracking its
// remote branch, web on the branch of web!2 (merged in the fake) off origin/main,
// where the merge left it. It returns the two worktree paths.
func (e *cliEnv) gcLayout() (api, web string) {
	e.t.Helper()
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t",
		"GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull,
	} {
		e.t.Setenv(k, v)
	}
	base := e.t.TempDir()
	root := filepath.Join(base, "root")
	e.app.Root = root
	e.app.Config.File = filepath.Join(base, "config.toml")
	projectDir := filepath.Join(root, "demo-gl")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	source := filepath.Join(base, "demo-gl.ade.toml")
	if err := os.WriteFile(source, []byte(gcManifest), 0o600); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(e.app.Config.File, []byte("[[projects]]\nsource = \""+source+"\"\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
	snapshot := "source = \"" + source + "\"\nfetched_at = 2026-11-11T12:00:00Z\nlast_update = 2026-11-11T12:00:00Z\nmanifest = '''" + gcManifest + "'''\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".project.toml"), []byte(snapshot), 0o600); err != nil {
		e.t.Fatal(err)
	}
	e.app.Config.Projects = []config.ProjectRef{{Source: source}}

	mk := func(repo, branch string, tracking bool) string {
		origin, work := filepath.Join(base, repo+".git"), filepath.Join(base, repo+"-work")
		gitAt(e.t, base, "init", "--bare", "-b", "main", origin)
		gitAt(e.t, base, "clone", "--quiet", origin, work)
		gitAt(e.t, work, "commit", "--quiet", "--allow-empty", "-m", "base")
		gitAt(e.t, work, "push", "--quiet", "origin", "main")
		if tracking {
			gitAt(e.t, work, "checkout", "--quiet", "-b", branch)
			gitAt(e.t, work, "commit", "--quiet", "--allow-empty", "-m", "change")
			gitAt(e.t, work, "push", "--quiet", "origin", branch)
		}
		repoDir := filepath.Join(projectDir, repo)
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			e.t.Fatal(err)
		}
		bare := filepath.Join(repoDir, ".bare")
		gitAt(e.t, base, "clone", "--quiet", "--bare", origin, bare)
		c := git.Client{}
		if _, err := c.PrepareBare(context.Background(), bare, "main"); err != nil {
			e.t.Fatal(err)
		}
		if err := git.WriteGitFile(repoDir); err != nil {
			e.t.Fatal(err)
		}
		tree := filepath.Join(repoDir, branch)
		if tracking {
			if err := c.AddTracking(context.Background(), bare, tree, branch); err != nil {
				e.t.Fatal(err)
			}
		} else {
			gitAt(e.t, bare, "worktree", "add", "--quiet", "-b", branch, tree, "origin/main")
		}
		return tree
	}
	return mk("api", "1-rate-limit-the-export-endpoint", true), mk("web", "2-fix-truncated-column-headers-in-export-preview", false)
}

type gcRowJSON struct {
	Branch  string   `json:"branch"`
	Action  string   `json:"action"`
	MR      string   `json:"mr"`
	Keep    []string `json:"keep"`
	Removed bool     `json:"removed"`
}

func (e *cliEnv) useFakeForge() {
	e.t.Helper()
	old := openGCForge
	openGCForge = func(*app.App) func(context.Context, string, config.Forge) (forge.Forge, error) {
		return func(_ context.Context, _ string, f config.Forge) (forge.Forge, error) {
			return gitlab.New(f.URL, fake.Token), nil
		}
	}
	e.t.Cleanup(func() { openGCForge = old })
}

func (e *cliEnv) gc(args ...string) (string, error) { return e.run(newGCCmd, args...) }

func TestGCDryRunExplainsEveryTree(t *testing.T) {
	e := newCLIEnv(t)
	e.useFakeForge()
	api, web := e.gcLayout()

	out, err := e.gc("--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"remove  demo-gl/web/2-fix-truncated-column-headers-in-export-preview  (web!2 merged)",
		"keep    demo-gl/api/1-rate-limit-the-export-endpoint  merge request api!1 is still open",
		"1 of 2 worktrees can be removed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, p := range []string{api, web} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("--dry-run removed %s", p)
		}
	}

	asJSON(t)
	out, err = e.gc("--dry-run")
	var rows []gcRowJSON
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 2 {
		t.Fatalf("--json = %q %v", out, err)
	}
	for _, r := range rows {
		switch r.Branch {
		case "2-fix-truncated-column-headers-in-export-preview":
			if r.Action != "remove" || r.MR != "web!2 merged" || len(r.Keep) != 0 {
				t.Errorf("web row = %+v", r)
			}
		default:
			if r.Action != "keep" || len(r.Keep) != 1 || !strings.Contains(r.Keep[0], "still open") {
				t.Errorf("api row = %+v", r)
			}
		}
	}
}

func TestGCNeedsYesWithoutATerminalThenRemoves(t *testing.T) {
	e := newCLIEnv(t)
	e.useFakeForge()
	api, web := e.gcLayout()

	if out, err := e.gc(); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("unattended gc without --yes: err %v\n%s", err, out)
	}
	if _, err := os.Stat(web); err != nil {
		t.Fatal("a refused gc removed the tree")
	}

	out, err := e.gc("--yes")
	if err != nil || !strings.Contains(out, "removed demo-gl/web/2-fix-truncated-column-headers-in-export-preview") {
		t.Fatalf("gc --yes: %v\n%s", err, out)
	}
	if _, err := os.Stat(web); err == nil {
		t.Error("the merged tree is still there")
	}
	if _, err := os.Stat(api); err != nil {
		t.Error("the open-MR tree was removed")
	}
	if got := e.audited("worktree.remove"); len(got) != 1 || got[0].Outcome != "ok" {
		t.Errorf("audit = %+v", got)
	}
}

func TestGCOneProjectOnly(t *testing.T) {
	e := newCLIEnv(t)
	e.useFakeForge()
	e.gcLayout()
	if _, err := e.gc("--dry-run", "--project", "nope"); err == nil || !strings.Contains(err.Error(), "not cloned") {
		t.Errorf("unknown project: %v", err)
	}
}

func TestProjectRemoveGuardsAndForces(t *testing.T) {
	e := newCLIEnv(t)
	_, web := e.gcLayout()
	if err := os.WriteFile(filepath.Join(web, "wip.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	remove := func(args ...string) (string, error) {
		return e.run(newProjectCmd, append([]string{"remove"}, args...)...)
	}

	out, err := remove("demo-gl", "--yes")
	if err == nil || !isExit(err, 2) || !strings.Contains(out, "keeps web/2-fix-truncated-column-headers-in-export-preview: uncommitted changes") {
		t.Fatalf("a dirty project: err %v\n%s", err, out)
	}
	if _, err := os.Stat(e.app.Root); err != nil {
		t.Fatal("a refused remove deleted the project")
	}

	if out, err = remove("demo-gl", "--force", "--dry-run"); err != nil || !strings.Contains(out, "would lose") {
		t.Fatalf("--force --dry-run: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(e.app.Root, "demo-gl")); err != nil {
		t.Fatal("--dry-run deleted the project")
	}
	if _, err = remove("demo-gl", "--force"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("unattended without --yes: %v", err)
	}

	if out, err = remove("demo-gl", "--force", "--yes"); err != nil || !strings.Contains(out, "removed project demo-gl") {
		t.Fatalf("--force --yes: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(e.app.Root, "demo-gl")); err == nil {
		t.Error("project directory survived")
	}
	cfg, _ := os.ReadFile(e.app.Config.File)
	if strings.Contains(string(cfg), "demo-gl.ade.toml") {
		t.Errorf("config still registers the project:\n%s", cfg)
	}
}

func isExit(err error, code int) bool {
	var ee *exitError
	return errors.As(err, &ee) && ee.code == code
}
