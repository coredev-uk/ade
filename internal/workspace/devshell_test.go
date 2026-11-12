package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/devshell"
)

func direnvShell(dir string) devshell.Shell { return devshell.Shell{Kind: devshell.Direnv, Dir: dir} }

// Editor, omp and shell panes run inside the dev shell; lazygit and the ade
// panes (which wrap their own commands) do not.
func TestBuildLayoutRunsPanesInTheDevShell(t *testing.T) {
	dir := "/projects/acme/web/418-paginate"

	code := baseSpec(LayoutCode)
	code.Devshell = direnvShell(dir)
	got, err := BuildLayout(code)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`pane name="editor" size="60%" focus=true command="direnv" {`,
		`args "exec" "` + dir + `" "nvim" "-u" "NONE"`,
		`pane name="shell" command="direnv" {`,
		`args "exec" "` + dir + `" "sh" "-c" "exec \"${SHELL:-sh}\""`,
		`pane name="lazygit" expanded=true command="/bin/lazygit"`, // untouched
	} {
		if !strings.Contains(got, want) {
			t.Errorf("code layout lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `command="direnv" {
                    args "exec" "`+dir+`" "/opt/ade`) {
		t.Errorf("ade panes must not be wrapped (their task run wraps the commands):\n%s", got)
	}

	agent := baseSpec(LayoutAgent)
	agent.Devshell = devshell.Shell{Kind: devshell.Nix, Dir: dir}
	agent.Agent = []string{"--config", "/tmp/omp.yml", "start"}
	got, err = BuildLayout(agent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `pane name="agent" size="60%" focus=true command="nix" {`) ||
		!strings.Contains(got, `args "develop" "`+dir+`" "-c" "env" "ADE_AGENT_RUN=1" "/bin/omp" "--config" "/tmp/omp.yml" "start"`) {
		t.Errorf("agent layout must start omp via nix develop:\n%s", got)
	}

	manual := baseSpec(LayoutManual)
	manual.Devshell = direnvShell(dir)
	got, err = BuildLayout(manual)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `pane name="shell" size="60%" focus=true command="direnv" {`) {
		t.Errorf("the manual layout's shell must enter the dev shell:\n%s", got)
	}

	plain, err := BuildLayout(baseSpec(LayoutCode))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "direnv") || strings.Contains(plain, "nix") {
		t.Errorf("a tree without a dev shell must lay out as before:\n%s", plain)
	}
}

// devshellFixture is a one-repo project whose oneshot task prints $FROM_ENVRC
// into a file, so the test sees what environment the task ran in.
func devshellFixture(t *testing.T, devshellSetting string) (f *fixture, out, tree string) {
	t.Helper()
	if _, err := exec.LookPath("direnv"); err != nil {
		t.Skip("direnv is not installed")
	}
	// direnv keeps its allow list under the user's data dir: isolate it.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DIRENV_CONFIG", t.TempDir())
	out = filepath.Join(t.TempDir(), "seen")
	setting := ""
	if devshellSetting != "" {
		setting = fmt.Sprintf("devshell = %q\n", devshellSetting)
	}
	manifest := fmt.Sprintf(`
name = "demo"
[repos.api]
path = "demo/api"
default_branch = "main"
%s[repos.api.tasks.probe]
cmd  = 'printf "%%s" "$FROM_ENVRC" > %s'
kind = "oneshot"
`, setting, out)
	f = newFixture(t, manifest, "api/main")
	tree = filepath.Join(f.root, "demo", "api", "main")
	if err := os.WriteFile(filepath.Join(tree, ".envrc"), []byte("export FROM_ENVRC=from-the-envrc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("direnv", "allow", tree).CombinedOutput(); err != nil {
		t.Skipf("direnv allow: %v\n%s", err, b)
	}
	return f, out, tree
}

// A worktree with an .envrc runs its tasks inside `direnv exec`: the task sees
// what the .envrc exports, though the environment ade runs in never had it.
func TestTaskRunsInsideDirenv(t *testing.T) {
	t.Setenv("FROM_ENVRC", "")
	os.Unsetenv("FROM_ENVRC") //nolint:errcheck // the variable must be absent, not empty
	f, out, _ := devshellFixture(t, "")
	plan, _ := f.up("api/main", PlanOptions{Nodes: []string{"api.probe"}})
	in, ok := plan.Instance("api.probe")
	if !ok {
		t.Fatal("api.probe is not planned")
	}
	if in.Devshell.Kind != devshell.Direnv {
		t.Fatalf("devshell = %v, want direnv", in.Devshell)
	}
	if got := readFile(t, out); got != "from-the-envrc" {
		t.Errorf("the task saw FROM_ENVRC=%q", got)
	}
}

func TestTaskDevshellNoneIsRespected(t *testing.T) {
	os.Unsetenv("FROM_ENVRC") //nolint:errcheck
	f, out, _ := devshellFixture(t, "none")
	plan, _ := f.up("api/main", PlanOptions{Nodes: []string{"api.probe"}})
	if in, _ := plan.Instance("api.probe"); in.Devshell.Active() {
		t.Fatalf("devshell = none must not wrap, got %v", in.Devshell)
	}
	if got := readFile(t, out); got != "" {
		t.Errorf("the task ran inside the .envrc although devshell = none: %q", got)
	}
}

// Services declared under [services.*] belong to no repo and are never wrapped.
func TestServicesOutsideReposAreNotWrapped(t *testing.T) {
	manifest := strings.Replace(appManifest, `[groups]`, "[services.cache]\ncmd = \"WS_HELPER=http {{HELPER}}\"\nport = \"auto\"\n\n[groups]", 1)
	manifest = strings.Replace(manifest, `requires = ["api.build"]`, `requires = ["api.build", "services.cache"]`, 1)
	f := newFixture(t, manifest, "api/main", "web/main")
	if err := os.WriteFile(filepath.Join(f.root, "demo", "api", "main", ".envrc"), []byte("export X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.mgr.Devshell = devshell.Finder{LookPath: func(string) (string, error) { return "/bin/direnv", nil }}
	plan, err := f.mgr.Plan(context.Background(), f.loc("api/main"), PlanOptions{Nodes: []string{"api.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plan.Instance("services.cache"); !ok {
		t.Fatal("services.cache is not in the plan")
	}
	for _, in := range plan.Instances {
		if in.Node.Repo != "" && in.Devshell.Kind != devshell.Direnv {
			t.Errorf("%s: devshell = %v, want direnv (the tree has an .envrc)", in.Node.ID, in.Devshell)
		}
		if in.Node.Repo == "" && in.Devshell.Active() {
			t.Errorf("%s belongs to no repo and must not be wrapped", in.Node.ID)
		}
	}
}
