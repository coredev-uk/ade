package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// Gather reads the context a prompt is built from through the real GitLab
// driver against the fake server: the issue and its child, human comments only
// (system notes dropped), the MR's unresolved threads, and AGENTS.md.
func TestGatherAgainstFakeGitLab(t *testing.T) {
	srv := fake.New(fake.Free)
	defer srv.Close()
	f := gitlab.New(srv.URL, fake.Token)
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "AGENTS.md"), []byte("Run go test ./... before finishing."), 0o644); err != nil {
		t.Fatal(err)
	}
	child := forge.Ref{Project: "ade-demo-env/api", IID: 1, Kind: forge.KindIssue}
	mr := forge.Ref{Project: "ade-demo-env/api", IID: 1, Kind: forge.KindMR}
	tgt := Target{
		Tracker: forge.Ref{Project: "ade-demo-env/tracker", IID: 1, Kind: forge.KindIssue},
		Child:   &child, MR: &mr, Worktree: wt, Branch: "b",
	}
	c, err := Gather(context.Background(), f, tgt)
	if err != nil {
		t.Fatal(err)
	}
	if c.Issue.Title == "" || c.Child == nil || c.Child.Ref != child {
		t.Errorf("issue %q child %+v", c.Issue.Title, c.Child)
	}
	if len(c.Threads) == 0 {
		t.Fatal("api!1 has unresolved threads in the fake")
	}
	for _, th := range c.Threads {
		if th.ID == "" || len(th.Notes) == 0 {
			t.Errorf("thread = %+v", th)
		}
	}
	for _, n := range c.Comments {
		if n.System {
			t.Errorf("a system note reached the prompt: %+v", n)
		}
	}
	if c.Agents != "Run go test ./... before finishing." {
		t.Errorf("AGENTS.md = %q", c.Agents)
	}

	plan := PlanPrompt(tgt, c)
	for _, want := range []string{"Do not modify any file", c.Issue.Title, "AGENTS.md", wt} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan prompt lacks %q", want)
		}
	}
	addr := AddressPrompt(tgt, c)
	if !strings.Contains(addr, "thread "+c.Threads[0].ID) || !strings.Contains(addr, `"replies"`) || !strings.Contains(addr, "Do not resolve") {
		t.Errorf("address prompt:\n%s", addr)
	}
	if strings.Contains(plan, "Unresolved threads") {
		t.Error("a plan prompt does not carry threads")
	}
	if !strings.Contains(RunPrompt(tgt, c, "THE PLAN"), "THE PLAN") {
		t.Error("run prompt lacks the approved plan")
	}
}

func TestGatherFailsOnMissingIssue(t *testing.T) {
	srv := fake.New(fake.Free)
	defer srv.Close()
	_, err := Gather(context.Background(), gitlab.New(srv.URL, fake.Token), Target{Tracker: forge.Ref{Project: "ade-demo-env/tracker", IID: 9999, Kind: forge.KindIssue}})
	if err == nil {
		t.Error("a missing issue must fail the gather, not plan from nothing")
	}
}

func TestLazygitCmd(t *testing.T) {
	zj := LazygitCmd("/w/tree", true)
	got := strings.Join(zj.Args, " ")
	if zj.Args[0] != "zellij" || !strings.Contains(got, "--floating") || !strings.Contains(got, "--cwd /w/tree") || !strings.HasSuffix(got, "-- lazygit") {
		t.Errorf("zellij: %s", got)
	}
	plain := LazygitCmd("/w/tree", false)
	if plain.Args[0] != "lazygit" || plain.Dir != "/w/tree" || strings.Join(plain.Args, " ") != "lazygit -p /w/tree" {
		t.Errorf("plain: %v dir %s", plain.Args, plain.Dir)
	}
}

func TestAuthorFromGit(t *testing.T) {
	e := newEnv(t)
	if a := AuthorFromGit(context.Background(), e.wt); a.Name != "Test User (ade agent)" || a.Email != "test@example.com" {
		t.Errorf("author = %+v", a)
	}
	bare := t.TempDir()
	sh(t, bare, "init", "-q")
	if a := AuthorFromGit(context.Background(), bare); a.Name != "ade agent" || a.Email == "" {
		t.Errorf("no git identity: %+v", a)
	}
}

// A machine with no git identity must still be able to commit an agent's work.
func TestCommitWithoutGitIdentity(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir()
	sh(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sha, err := commitAll(ctx, dir, AuthorFromGit(ctx, dir), "first")
	if err != nil || sha == "" {
		t.Fatalf("commit: %q %v", sha, err)
	}
	if got := sh(t, dir, "log", "-1", "--format=%an|%ae"); got != "ade agent|ade-agent@localhost" {
		t.Errorf("author = %q", got)
	}
	if sha, err := commitAll(ctx, dir, Author{Name: "x", Email: "x@y"}, "again"); err != nil || sha != "" {
		t.Errorf("a clean tree commits nothing: %q %v", sha, err)
	}
}

// The user's git config (a pager, an external diff tool such as difftastic)
// must not change what the agent's diff looks like.
func TestDiffIgnoresUserDiffTools(t *testing.T) {
	e := newEnv(t)
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[diff]\n\texternal = /bin/false\n[color]\n\tui = always\n[core]\n\tpager = cat -n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	base := sh(t, e.wt, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(e.wt, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := worktreeDiff(context.Background(), e.wt, base, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 1 || !strings.Contains(d.Patch, "+++ b/a.txt") || !strings.Contains(d.Patch, "+one") || strings.Contains(d.Patch, "\x1b[") {
		t.Errorf("patch = %q", d.Patch)
	}
}

// A user who signs every commit must not have the agent's push fail on a
// missing key: the commit is made unsigned.
func TestCommitIgnoresSigningConfig(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[commit]\n\tgpgsign = true\n[gpg]\n\tformat = ssh\n[user]\n\tsigningkey = /nonexistent/key.pub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir()
	sh(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if sha, err := commitAll(ctx, dir, Author{Name: "A (ade agent)", Email: "a@example.com"}, "msg"); err != nil || sha == "" {
		t.Fatalf("commit with signing configured: %q %v", sha, err)
	}
}

// The project's AGENTS.md (<root>/<project>/AGENTS.md) reaches the prompt next to the worktree's.
func TestGatherCarriesProjectAgents(t *testing.T) {
	srv := fake.New(fake.Free)
	defer srv.Close()
	projectDir := t.TempDir()
	wt := filepath.Join(projectDir, "api", "b")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "AGENTS.md"), []byte("<!-- Generated by ADE from x -->\n\nEvery service logs JSON.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tgt := Target{Tracker: forge.Ref{Project: "ade-demo-env/tracker", IID: 1, Kind: forge.KindIssue}, Worktree: wt, Branch: "b"}
	c, err := Gather(context.Background(), gitlab.New(srv.URL, fake.Token), tgt)
	if err != nil {
		t.Fatal(err)
	}
	if c.ProjectAgents != "Every service logs JSON." {
		t.Errorf("ProjectAgents = %q", c.ProjectAgents)
	}
	if p := PlanPrompt(tgt, c); !strings.Contains(p, "## Project guidance") || !strings.Contains(p, "Every service logs JSON.") {
		t.Errorf("plan prompt lacks the project guidance:\n%s", p)
	}
}
