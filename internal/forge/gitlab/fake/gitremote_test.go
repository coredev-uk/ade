package fake_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

func gitOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGitRootBranchCreateMirrorsToBareRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	srv := fake.New(fake.Free, fake.WithGitRoot(root))
	defer srv.Close()
	project := fake.GroupPath + "/api"
	if err := srv.InitRemote(project, map[string]string{"README.md": "hi\n", "cmd/x/main.go": "package main\n"}); err != nil {
		t.Fatal(err)
	}
	bare := srv.RemotePath(project)
	if got := gitOut(t, "-C", bare, "show", "main:cmd/x/main.go"); got != "package main" {
		t.Fatalf("main:cmd/x/main.go = %q", got)
	}
	const api = "/api/v4/projects/ade-demo-env%2Fapi/repository/branches?branch=1-feature&ref=main"
	code, _, body := get(t, srv, "POST", api, fake.Token, "")
	if code != 201 {
		t.Fatalf("create branch = %d: %s", code, body)
	}
	if got, want := gitOut(t, "-C", bare, "rev-parse", "1-feature"), gitOut(t, "-C", bare, "rev-parse", "main"); got != want {
		t.Errorf("1-feature at %s, want main's %s", got, want)
	}
	if code, _, _ = get(t, srv, "POST", api, fake.Token, ""); code != 400 {
		t.Errorf("duplicate branch = %d, want 400", code)
	}
	if code, _, _ = get(t, srv, "POST", strings.Split(api, "?")[0]+"?branch=x&ref=nope", fake.Token, ""); code != 400 {
		t.Errorf("unknown ref = %d, want 400", code)
	}
	if out := gitOut(t, "-C", bare, "branch", "--list", "x"); out != "" {
		t.Errorf("rejected branch reached the bare repo: %q", out)
	}
}

func TestGitRootLeavesUninitialisedProjectsAlone(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithGitRoot(t.TempDir()))
	defer srv.Close()
	code, _, body := get(t, srv, "POST", "/api/v4/projects/ade-demo-env%2Fweb/repository/branches?branch=b&ref=main", fake.Token, "")
	if code != 201 {
		t.Fatalf("create branch = %d: %s", code, body)
	}
}

func TestInitRemoteCarriesSeededBranches(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithGitRoot(t.TempDir()))
	defer srv.Close()
	project := fake.GroupPath + "/web"
	if err := srv.InitRemote(project, map[string]string{"README.md": "x\n"}); err != nil {
		t.Fatal(err)
	}
	for _, b := range srv.BranchNames(project) {
		gitOut(t, "-C", srv.RemotePath(project), "rev-parse", "--verify", b)
	}
	if len(srv.BranchNames(project)) < 2 {
		t.Fatalf("expected the seed to carry an MR branch on web, got %v", srv.BranchNames(project))
	}
}
