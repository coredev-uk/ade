package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
)

const (
	adoptTracker = "ade-demo-env/tracker"
	adoptAPI     = "ade-demo-env/api"
)

func (e *cliEnv) adopt(args ...string) (string, error) { return e.run(newAdoptCmd, args...) }

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// cacheDemoGL writes work items of the gcLayout project into the cache.
func (e *cliEnv) cacheDemoGL(items ...domain.WorkItem) {
	e.t.Helper()
	st, err := e.app.Cache()
	if err != nil {
		e.t.Fatal(err)
	}
	state, _ := json.Marshal(forgesync.Result{Project: "demo-gl", Forge: "sandbox", Tracker: adoptTracker, SyncedAt: time.Now()})
	if _, err := st.DB.Exec(`INSERT INTO sync_state (project, json) VALUES ('demo-gl', ?)`, string(state)); err != nil {
		e.t.Fatal(err)
	}
	for _, wi := range items {
		b, _ := json.Marshal(wi)
		if _, err := st.DB.Exec(`INSERT INTO work_items (project, ref, json) VALUES ('demo-gl', ?, ?)`, wi.Ref.String(), string(b)); err != nil {
			e.t.Fatal(err)
		}
	}
}

// adoptItem7 is tracker issue 7 with child issue api#3 and no MR.
func adoptItem7() domain.WorkItem {
	tr := forge.Ref{Project: adoptTracker, IID: 7, Kind: forge.KindIssue}
	return domain.WorkItem{
		Project: "demo-gl", Ref: tr, Issue: forge.Issue{Ref: tr, Title: "Limit exports"},
		Children: []forge.Issue{{Ref: forge.Ref{Project: adoptAPI, IID: 3, Kind: forge.KindIssue}, Title: "Limiter"}},
		Stage:    domain.StageDraft, Court: domain.CourtYou,
	}
}

// originOf is the bare "forge" repo gcLayout cloned repo from.
func (e *cliEnv) originOf(repo string) string {
	return filepath.Join(filepath.Dir(e.app.Root), repo+".git")
}

// localClone clones repo's origin to a new directory and commits msg on a
// new branch there without pushing.
func (e *cliEnv) localClone(repo, branch, msg string) string {
	e.t.Helper()
	dir := filepath.Join(e.t.TempDir(), "other")
	gitAt(e.t, filepath.Dir(dir), "clone", "--quiet", e.originOf(repo), dir)
	gitAt(e.t, dir, "checkout", "--quiet", "-b", branch)
	gitAt(e.t, dir, "commit", "--quiet", "--allow-empty", "-m", msg)
	return dir
}

// pushBranch creates branch on the repo's origin with one commit.
func (e *cliEnv) pushBranch(repo, branch string) {
	e.t.Helper()
	work := filepath.Join(filepath.Dir(e.app.Root), repo+"-work")
	gitAt(e.t, work, "checkout", "--quiet", "-b", branch, "main")
	gitAt(e.t, work, "commit", "--quiet", "--allow-empty", "-m", "remote work")
	gitAt(e.t, work, "push", "--quiet", "origin", branch)
	gitAt(e.t, work, "checkout", "--quiet", "main")
}

func worktreeConfig(t *testing.T, dir, key string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "config", "--worktree", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func TestAdoptLocalBranchFromAnotherClone(t *testing.T) {
	e := newCLIEnv(t)
	e.gcLayout()
	snap := filepath.Join(e.app.Root, "demo-gl", ".project.toml")
	b, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	withPrefix := strings.Replace(string(b), "[repos.api]", "[conventions]\ncommit_prefix = \"GL-{parent}\"\n\n[repos.api]", 1)
	if err := os.WriteFile(snap, []byte(withPrefix), 0o600); err != nil {
		t.Fatal(err)
	}
	e.cacheDemoGL(adoptItem7())
	other := e.localClone("api", "3-limit", "wip: limiter")

	out, err := e.adopt("demo-gl", "api", "3-limit", "--from", other, "--item", adoptTracker+"#7")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dir := filepath.Join(e.app.Root, "demo-gl", "api", "3-limit")
	if got := gitOut(t, dir, "log", "-1", "--format=%s"); got != "wip: limiter" {
		t.Errorf("last commit = %q, want the unpushed one", got)
	}
	if got := gitOut(t, dir, "branch", "--show-current"); got != "3-limit" {
		t.Errorf("branch = %q", got)
	}
	for key, want := range map[string]string{"ade.parent": "7", "ade.commitPrefix": "GL-7", "ade.target": "main"} {
		if got := worktreeConfig(t, dir, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if strings.Contains(out, "note:") {
		t.Errorf("a branch with the child iid prefix got a note:\n%s", out)
	}
	if gitOut(t, e.originOf("api"), "branch", "--list", "3-limit") != "" {
		t.Error("adopt pushed the branch")
	}
	if n := len(e.srv.Requests()); n != 0 {
		t.Errorf("adopt made %d forge requests", n)
	}
	if got := e.audited("git.worktree.add"); len(got) != 1 || got[0].Target != dir || got[0].Outcome != "ok" {
		t.Errorf("audit = %+v", got)
	}
}

func TestAdoptRemoteBranchTracksOrigin(t *testing.T) {
	e := newCLIEnv(t)
	e.gcLayout()
	e.pushBranch("api", "feature/export")

	out, err := e.adopt("demo-gl", "api", "feature/export", "--target", "release")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dir := filepath.Join(e.app.Root, "demo-gl", "api", "feature-export")
	if got := gitOut(t, dir, "rev-parse", "--abbrev-ref", "@{u}"); got != "origin/feature/export" {
		t.Errorf("upstream = %q", got)
	}
	if got := gitOut(t, dir, "log", "-1", "--format=%s"); got != "remote work" {
		t.Errorf("last commit = %q", got)
	}
	if got := worktreeConfig(t, dir, "ade.target"); got != "release" {
		t.Errorf("ade.target = %q", got)
	}
	for _, key := range []string{"ade.parent", "ade.commitPrefix"} {
		if got := worktreeConfig(t, dir, key); got != "" {
			t.Errorf("%s = %q without --item", key, got)
		}
	}

	if _, err := e.adopt("demo-gl", "api", "no-such-branch"); err == nil || !strings.Contains(err.Error(), "not on origin") {
		t.Errorf("an unknown branch: %v", err)
	}
}

func TestAdoptKeepsAnExistingWorktree(t *testing.T) {
	e := newCLIEnv(t)
	api, _ := e.gcLayout()
	e.cacheDemoGL(adoptItem7())

	out, err := e.adopt("demo-gl", "api", filepath.Base(api), "--item", adoptTracker+"#7")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, api) || !strings.Contains(out, "where it is") {
		t.Errorf("output does not say where the worktree is:\n%s", out)
	}
	if !strings.Contains(out, "note: the dashboard will not show") {
		t.Errorf("no note for a branch no MR or child owns:\n%s", out)
	}
	if got := worktreeConfig(t, api, "ade.parent"); got != "7" {
		t.Errorf("ade.parent = %q", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(api))
	for _, en := range entries {
		if n := en.Name(); n != ".bare" && n != ".git" && n != filepath.Base(api) {
			t.Errorf("adopt created %s next to the existing worktree", n)
		}
	}
	if got := e.audited("git.worktree.add"); len(got) != 0 {
		t.Errorf("a worktree add was audited: %+v", got)
	}
	if _, err := e.adopt("demo-gl", "api", filepath.Base(api), "--from", t.TempDir()); err == nil || !strings.Contains(err.Error(), "checked out") {
		t.Errorf("--from onto a checked-out branch: %v", err)
	}
}

func TestAdoptDryRunWritesNothing(t *testing.T) {
	e := newCLIEnv(t)
	e.gcLayout()
	e.cacheDemoGL(adoptItem7())
	other := e.localClone("api", "3-limit", "wip")
	bare := filepath.Join(e.app.Root, "demo-gl", "api", ".bare")

	out, err := e.adopt("demo-gl", "api", "3-limit", "--from", other, "--item", adoptTracker+"#7", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"would fetch 3-limit from", "would add worktree", "would write ade.parent=7"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(e.app.Root, "demo-gl", "api", "3-limit")); err == nil {
		t.Error("--dry-run created the worktree")
	}
	if got := gitOut(t, bare, "branch", "--list", "3-limit"); got != "" {
		t.Errorf("--dry-run fetched the branch: %q", got)
	}
	if got := e.audited("git.worktree.add"); len(got) != 0 {
		t.Errorf("--dry-run audited a worktree add: %+v", got)
	}
}

func TestAdoptRefusesAnExistingDirectory(t *testing.T) {
	e := newCLIEnv(t)
	e.gcLayout()
	e.pushBranch("api", "collide")
	dir := filepath.Join(e.app.Root, "demo-gl", "api", "collide")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := e.adopt("demo-gl", "api", "collide")
	if err == nil || !strings.Contains(err.Error(), "exists but is not a worktree") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Error("adopt wrote into the directory")
	}
}
