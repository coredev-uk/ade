package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

// runCd runs `ade cd` against e and returns stdout and stderr apart.
func (e *codeEnv) runCd(args ...string) (stdout, stderr string, err error) {
	e.t.Helper()
	cmd := newCdCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err = cmd.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, e.app))
	return out.String(), errOut.String(), err
}

// addItem caches another work item for project demo.
func (e *codeEnv) addItem(wi domain.WorkItem) {
	e.t.Helper()
	st, err := e.app.Cache()
	if err != nil {
		e.t.Fatal(err)
	}
	raw, _ := json.Marshal(wi)
	if _, err := st.DB.Exec(`INSERT OR REPLACE INTO work_items (project, ref, json) VALUES ('demo', ?, ?)`, wi.Ref.String(), string(raw)); err != nil {
		e.t.Fatal(err)
	}
}

// addWorktree adds a worktree on a new branch of repo.
func (e *codeEnv) addWorktree(repo, branch string) string {
	e.t.Helper()
	dir := filepath.Join(e.root, "demo", repo)
	path := filepath.Join(dir, branch)
	c := exec.Command("git", "worktree", "add", "-q", "-b", branch, path, "main")
	c.Dir = filepath.Join(dir, ".bare")
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := c.CombinedOutput(); err != nil {
		e.t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	return path
}

func TestCdPrintsOnlyThePathOfTheItemsWorktree(t *testing.T) {
	e := newCodeEnv(t)
	want := filepath.Join(e.root, "demo", "api", "3-add-limit")
	// The tracker ref and the child issue's ref name one item.
	for _, ref := range []string{"g/tracker#15", "g/api#3"} {
		out, errOut, err := e.runCd(ref)
		if err != nil || out != want+"\n" || errOut != "" {
			t.Errorf("ade cd %s = %q, stderr %q, %v; want %q", ref, out, errOut, err, want+"\n")
		}
	}
}

func TestCdRepoFlagPicksAnotherWorktreeOrListsTheRepos(t *testing.T) {
	e := newCodeEnv(t)
	web := e.addWorktree("web", "7-web-limit")
	e.addItem(domain.WorkItem{
		Ref:   forge.Ref{Project: "g/tracker", IID: 15, Kind: forge.KindIssue},
		Repos: []string{"api", "web"},
		Children: []forge.Issue{
			{Ref: forge.Ref{Project: "g/api", IID: 3, Kind: forge.KindIssue}},
			{Ref: forge.Ref{Project: "g/web", IID: 7, Kind: forge.KindIssue}},
		},
	})
	out, _, err := e.runCd("g/tracker#15", "--repo", "web")
	if err != nil || out != web+"\n" {
		t.Errorf("--repo web = %q, %v; want %q", out, err, web)
	}
	if out, _, err = e.runCd("g/tracker#15"); err != nil || out != filepath.Join(e.root, "demo", "api", "3-add-limit")+"\n" {
		t.Errorf("without --repo the primary repo (first routed) = %q, %v", out, err)
	}

	jsonOut = true
	t.Cleanup(func() { jsonOut = false })
	out, _, err = e.runCd("g/tracker#15", "--repo", "web")
	if err != nil {
		t.Fatal(err)
	}
	var got cdJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got.Path != web || got.Project != "demo" || got.Repo != "web" || got.Branch != "7-web-limit" || len(got.Worktrees) != 2 {
		t.Fatalf("%+v", got)
	}
	if w := got.Worktrees[0]; w.Repo != "api" || !w.Primary || w.Branch != "3-add-limit" {
		t.Errorf("worktrees[0] = %+v", w)
	}
	if w := got.Worktrees[1]; w.Repo != "web" || w.Primary {
		t.Errorf("worktrees[1] = %+v", w)
	}
}

func TestCdRepoWithoutWorktreeIsAnErrorListingTheRepos(t *testing.T) {
	e := newCodeEnv(t)
	out, _, err := e.runCd("g/tracker#15", "--repo", "web")
	if err == nil || out != "" || !strings.Contains(err.Error(), "it has: api") {
		t.Errorf("out %q, err %v", out, err)
	}
}

func TestCdItemWithoutWorktreeNamesHowToMakeOne(t *testing.T) {
	e := newCodeEnv(t)
	e.addItem(domain.WorkItem{Ref: forge.Ref{Project: "g/tracker", IID: 16, Kind: forge.KindIssue}, Repos: []string{"web"}})
	out, _, err := e.runCd("g/tracker#16")
	if err == nil || out != "" || !strings.Contains(err.Error(), "ade start") {
		t.Errorf("out %q, err %v", out, err)
	}
	out, _, err = e.runCd("g/tracker#99")
	if err == nil || out != "" || !strings.Contains(err.Error(), "ade sync") {
		t.Errorf("unknown item: out %q, err %v", out, err)
	}
}

func TestCdRepoNamePrintsTheBaselineTree(t *testing.T) {
	e := newCodeEnv(t)
	for _, arg := range []string{"api", "demo/api"} {
		out, _, err := e.runCd(arg)
		if want := filepath.Join(e.root, "demo", "api", "main") + "\n"; err != nil || out != want {
			t.Errorf("ade cd %s = %q, %v; want %q", arg, out, err, want)
		}
	}
	jsonOut = true
	t.Cleanup(func() { jsonOut = false })
	out, _, err := e.runCd("web")
	if err != nil {
		t.Fatal(err)
	}
	var got cdJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got.Repo != "web" || got.Branch != "main" || got.Path != filepath.Join(e.root, "demo", "web", "main") || len(got.Worktrees) != 1 || !got.Worktrees[0].Primary {
		t.Errorf("%+v", got)
	}
}

func TestCdRepoNameErrors(t *testing.T) {
	e := newCodeEnv(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"nope"}, "neither a work item ref nor a repo"},
		{[]string{"demo/nope"}, "no repo"},
		{[]string{"other/api"}, "no project"},
		{[]string{"api", "--repo", "web"}, "--repo applies to a work item"},
	} {
		out, _, err := e.runCd(tc.args...)
		if err == nil || out != "" || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ade cd %v: out %q, err %v; want error with %q", tc.args, out, err, tc.want)
		}
	}
}

// useSnapshot registers one project whose manifest snapshot names tracker g/tracker.
func (e *codeEnv) useSnapshot(extraManifest string) {
	e.t.Helper()
	source := filepath.Join(e.root, "demo.ade.toml")
	e.app.Config.Projects = []config.ProjectRef{{Source: source}}
	manifest := "name = \"demo\"\n[tracker]\nproject = \"g/tracker\"\n[repos.api]\npath = \"g/api\"\ndefault_branch = \"main\"\n[repos.web]\npath = \"g/web\"\ndefault_branch = \"main\"\n" + extraManifest
	snap := "source = " + strconv.Quote(source) + "\nmanifest = '''\n" + manifest + "'''\n"
	if err := os.WriteFile(filepath.Join(e.root, "demo", ".project.toml"), []byte(snap), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func TestCdBareNumberReadsTheProjectSnapshot(t *testing.T) {
	e := newCodeEnv(t)
	e.useSnapshot("")
	want := filepath.Join(e.root, "demo", "api", "3-add-limit") + "\n"
	for _, arg := range []string{"15", "#15"} {
		if out, errOut, err := e.runCd(arg); err != nil || out != want || errOut != "" {
			t.Errorf("ade cd %s = %q, stderr %q, %v; want %q", arg, out, errOut, err, want)
		}
	}
}

// ade cd never fetches: a registered project without a snapshot is an error
// even though its source could be fetched.
func TestCdBareNumberWithoutASnapshotNeverFetches(t *testing.T) {
	e := newCodeEnv(t)
	e.app.Config.Projects = []config.ProjectRef{{Source: "https://127.0.0.1:1/never-fetched.ade.toml"}}
	out, _, err := e.runCd("15")
	if err == nil || out != "" || !strings.Contains(err.Error(), "ade update") {
		t.Errorf("out %q, err %v", out, err)
	}
}

func TestCdMRRefAndABaselineNamedAfterTheDefaultBranch(t *testing.T) {
	e := newCodeEnv(t)
	e.addItem(domain.WorkItem{
		Ref:      forge.Ref{Project: "g/tracker", IID: 15, Kind: forge.KindIssue},
		Repos:    []string{"api"},
		Children: []forge.Issue{{Ref: forge.Ref{Project: "g/api", IID: 3, Kind: forge.KindIssue}}},
		MRs:      []forge.MR{{Ref: forge.Ref{Project: "g/api", IID: 5, Kind: forge.KindMR}, SourceBranch: "3-add-limit"}},
	})
	want := filepath.Join(e.root, "demo", "api", "3-add-limit") + "\n"
	if out, _, err := e.runCd("g/api!5"); err != nil || out != want {
		t.Errorf("ade cd g/api!5 = %q, %v; want %q", out, err, want)
	}

	// A repo whose default branch is trunk has its baseline in trunk/.
	dir := filepath.Join(e.root, "demo", "svc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := t.TempDir()
	for _, step := range []struct {
		dir  string
		args []string
	}{
		{seed, []string{"init", "-q", "-b", "trunk"}},
		{seed, []string{"commit", "-q", "--allow-empty", "-m", "init"}},
		{dir, []string{"clone", "-q", "--bare", seed, ".bare"}},
		{filepath.Join(dir, ".bare"), []string{"worktree", "add", "-q", filepath.Join(dir, "trunk"), "trunk"}},
	} {
		c := exec.Command("git", step.args...)
		c.Dir = step.dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", step.args, err, out)
		}
	}
	e.useSnapshot("[repos.svc]\npath = \"g/svc\"\ndefault_branch = \"trunk\"\n")
	for _, arg := range []string{"svc", "demo/svc"} {
		if out, _, err := e.runCd(arg); err != nil || out != filepath.Join(dir, "trunk")+"\n" {
			t.Errorf("ade cd %s = %q, %v; want the trunk tree", arg, out, err)
		}
	}
}
