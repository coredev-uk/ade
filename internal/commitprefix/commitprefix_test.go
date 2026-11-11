package commitprefix

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareWithTrees builds <tmp>/.bare plus worktrees a and b, like ade project add.
func bareWithTrees(t *testing.T) (a, b string) {
	t.Helper()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	sh(t, tmp, "init", "-q", "-b", "main", src)
	sh(t, src, "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "init")
	bare := filepath.Join(tmp, "repo", ".bare")
	sh(t, tmp, "clone", "-q", "--bare", src, bare)
	a, b = filepath.Join(tmp, "repo", "a"), filepath.Join(tmp, "repo", "b")
	sh(t, bare, "worktree", "add", "-q", "-b", "a", a)
	sh(t, bare, "worktree", "add", "-q", "-b", "b", b)
	return a, b
}

func TestWriteWorktreeConfig(t *testing.T) {
	ctx := context.Background()
	a, b := bareWithTrees(t)
	for range 2 { // idempotent
		if err := WriteWorktreeConfig(ctx, a, 7, "GL-7"); err != nil {
			t.Fatal(err)
		}
	}
	if got := Read(ctx, a); got != "GL-7" {
		t.Errorf("prefix in a = %q", got)
	}
	if got := sh(t, a, "config", "--get", KeyParent); got != "7" {
		t.Errorf("parent = %q", got)
	}
	if got := Read(ctx, b); got != "" {
		t.Errorf("prefix leaked into sibling worktree b: %q", got)
	}
	if err := WriteWorktreeConfig(ctx, b, 9, "GL-9"); err != nil {
		t.Fatal(err)
	}
	if Read(ctx, a) != "GL-7" || Read(ctx, b) != "GL-9" {
		t.Errorf("per-worktree values mixed up: %q %q", Read(ctx, a), Read(ctx, b))
	}
	sh(t, a, "status") // bare repo worktrees still work
	sh(t, filepath.Dir(a), "-C", ".bare", "worktree", "add", "-q", "-b", "c", "../c")
	sh(t, filepath.Join(filepath.Dir(a), "c"), "status")

	if err := WriteWorktreeConfig(ctx, a, 7, "GL-70"); err != nil {
		t.Fatal(err)
	}
	if got := Read(ctx, a); got != "GL-70" {
		t.Errorf("updated prefix = %q", got)
	}
	if err := WriteWorktreeConfig(ctx, a, 7, ""); err != nil {
		t.Fatal(err)
	}
	if got := Read(ctx, a); got != "" {
		t.Errorf("stale prefix not removed: %q", got)
	}
	if got := sh(t, a, "config", "--get", KeyParent); got != "7" {
		t.Errorf("parent lost: %q", got)
	}
}

func TestExpandPrefix(t *testing.T) {
	m := &config.Manifest{Name: "acme", Repos: map[string]config.Repo{"api": {Path: "g/api"}}}
	m.Conventions.CommitPrefix = "{project}-{parent}/{child}"
	wi := domain.WorkItem{
		Ref:      forge.Ref{Project: "g/tracker", IID: 412, Kind: forge.KindIssue},
		Children: []forge.Issue{{Ref: forge.Ref{Project: "g/api", IID: 17, Kind: forge.KindIssue}}},
		MRs:      []forge.MR{{Ref: forge.Ref{Project: "g/api", IID: 3, Kind: forge.KindMR}}},
	}
	if got := ExpandPrefix(m, wi, "api"); got != "acme-412/17" {
		t.Errorf("got %q", got)
	}
	if got := ExpandPrefix(m, wi, "web"); got != "acme-412/412" {
		t.Errorf("repo without child: %q", got)
	}
	if got := ForMR(m, wi, wi.MRs[0].Ref); got != "acme-412/17" {
		t.Errorf("ForMR = %q", got)
	}
	if !Owns(wi, wi.MRs[0].Ref) || Owns(wi, forge.Ref{Project: "g/api", IID: 4, Kind: forge.KindMR}) {
		t.Error("Owns wrong")
	}
	m.Conventions.CommitPrefix = ""
	if got := ExpandPrefix(m, wi, "api"); got != "" {
		t.Errorf("unset template gave %q", got)
	}
}
