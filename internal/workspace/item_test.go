package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

// gitRepo makes <repoDir>/.bare with a main worktree and one worktree per
// extra branch, the layout `ade project add` produces.
func gitRepo(t *testing.T, repoDir string, branches ...string) {
	t.Helper()
	gitRepoOn(t, repoDir, "main", branches...)
}

// gitRepoOn is gitRepo for a repo whose default branch (the bare clone's HEAD,
// and its baseline tree) is def.
func gitRepoOn(t *testing.T, repoDir, def string, branches ...string) {
	t.Helper()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	seed := t.TempDir()
	run(seed, "init", "-q", "-b", def)
	run(seed, "commit", "-q", "--allow-empty", "-m", "init")
	bare := filepath.Join(repoDir, ".bare")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(repoDir, "clone", "-q", "--bare", seed, bare)
	run(bare, "worktree", "add", "-q", filepath.Join(repoDir, def), def)
	for _, b := range branches {
		run(bare, "worktree", "add", "-q", "-b", b, filepath.Join(repoDir, b), def)
	}
}

const gitManifest = `
name = "demo"
[repos.api]
path = "grp/api"
default_branch = "main"
app = ["api"]
[repos.web]
path = "grp/web"
default_branch = "main"
app = ["web"]
requires = ["api"]
`

func gitFixture(t *testing.T, apiBranches, webBranches []string) (root string, loc Location) {
	t.Helper()
	f := newFixture(t, gitManifest)
	gitRepo(t, filepath.Join(f.root, "demo", "api"), apiBranches...)
	gitRepo(t, filepath.Join(f.root, "demo", "web"), webBranches...)
	l, err := Locate(f.root, filepath.Join(f.root, "demo", "web", "main"))
	if err != nil {
		t.Fatal(err)
	}
	return f.root, l
}

func workItem(repos []string, children []forge.Ref, mrs ...forge.MR) domain.WorkItem {
	wi := domain.WorkItem{Ref: forge.Ref{Project: "grp/tracker", IID: 7, Kind: forge.KindIssue}, Repos: repos, MRs: mrs}
	for _, c := range children {
		wi.Children = append(wi.Children, forge.Issue{Ref: c})
	}
	return wi
}

func issueRef(project string, iid int) forge.Ref {
	return forge.Ref{Project: project, IID: iid, Kind: forge.KindIssue}
}

func TestTargetForItemPrefersItsOwnWorktree(t *testing.T) {
	root, loc := gitFixture(t, []string{"3-add-limit"}, []string{"12-paginate", "99-other"})
	wi := workItem([]string{"web", "api"}, []forge.Ref{issueRef("grp/web", 12), issueRef("grp/api", 3)})
	got, err := TargetForItem(context.Background(), root, loc.Manifest, wi)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "web" || got.Baseline || filepath.Base(got.Worktree) != "12-paginate" || got.Title != "12-paginate" {
		t.Fatalf("%+v", got)
	}
	if filepath.Base(got.ItemTrees["api"]) != "3-add-limit" || filepath.Base(got.ItemTrees["web"]) != "12-paginate" {
		t.Fatalf("item trees: %v", got.ItemTrees)
	}
}

func TestTargetForItemMatchesMRSourceBranch(t *testing.T) {
	root, loc := gitFixture(t, nil, []string{"fancy-branch"})
	mr := forge.MR{Ref: forge.Ref{Project: "grp/web", IID: 4, Kind: forge.KindMR}, SourceBranch: "fancy-branch"}
	got, err := TargetForItem(context.Background(), root, loc.Manifest, workItem([]string{"web"}, nil, mr))
	if err != nil || filepath.Base(got.Worktree) != "fancy-branch" {
		t.Fatalf("%+v %v", got, err)
	}
	// Another repo's MR with the same branch name does not claim it.
	other := forge.MR{Ref: forge.Ref{Project: "grp/api", IID: 4, Kind: forge.KindMR}, SourceBranch: "fancy-branch"}
	got, err = TargetForItem(context.Background(), root, loc.Manifest, workItem([]string{"web"}, nil, other))
	if err != nil || !got.Baseline {
		t.Fatalf("an MR of another repo must not match: %+v %v", got, err)
	}
}

func TestTargetForItemFallsBackToTheBaseline(t *testing.T) {
	root, loc := gitFixture(t, nil, []string{"99-other"})
	got, err := TargetForItem(context.Background(), root, loc.Manifest, workItem([]string{"web"}, []forge.Ref{issueRef("grp/web", 12)}))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Baseline || got.Repo != "web" || filepath.Base(got.Worktree) != "main" || len(got.ItemTrees) != 0 {
		t.Fatalf("%+v", got)
	}
	// An iid that is merely a prefix of another number does not match: 1- vs 12-.
	root, loc = gitFixture(t, nil, []string{"12-paginate"})
	got, _ = TargetForItem(context.Background(), root, loc.Manifest, workItem([]string{"web"}, []forge.Ref{issueRef("grp/web", 1)}))
	if !got.Baseline {
		t.Fatalf("child #1 must not claim branch 12-paginate: %+v", got)
	}
}

func TestTargetForItemNeedsARoutedRepo(t *testing.T) {
	root, loc := gitFixture(t, nil, nil)
	_, err := TargetForItem(context.Background(), root, loc.Manifest, workItem(nil, nil))
	if err == nil || !strings.Contains(err.Error(), "add an app label") {
		t.Fatalf("got %v", err)
	}
}

func TestTargetForMR(t *testing.T) {
	root, loc := gitFixture(t, nil, []string{"12-paginate"})
	mr := forge.MR{Ref: forge.Ref{Project: "grp/web", IID: 5, Kind: forge.KindMR}, SourceBranch: "12-paginate"}
	got, err := TargetForMR(context.Background(), root, loc.Manifest, mr)
	if err != nil || filepath.Base(got.Worktree) != "12-paginate" || got.Baseline {
		t.Fatalf("%+v %v", got, err)
	}
	mr.SourceBranch = "someone-elses"
	got, err = TargetForMR(context.Background(), root, loc.Manifest, mr)
	if err != nil || !got.Baseline || filepath.Base(got.Worktree) != "main" {
		t.Fatalf("a review of a branch we have no tree for opens the baseline: %+v %v", got, err)
	}
	mr.Ref.Project = "grp/unknown"
	if _, err := TargetForMR(context.Background(), root, loc.Manifest, mr); err == nil {
		t.Fatal("an MR outside the manifest's repos must be an error")
	}
}

func TestItemOfAndBranchOf(t *testing.T) {
	_, loc := gitFixture(t, nil, []string{"12-paginate"})
	feat, err := Locate(loc.Root, filepath.Join(loc.RepoDir, "12-paginate"))
	if err != nil {
		t.Fatal(err)
	}
	if b := BranchOf(context.Background(), feat); b != "12-paginate" {
		t.Fatalf("branch %q", b)
	}
	wi := workItem([]string{"web"}, []forge.Ref{issueRef("grp/web", 12)})
	items := []ItemRef{{Project: "other", Forge: "f0", Item: wi}, {Project: "demo", Forge: "f1", Item: wi}}
	forgeName, tracker, ok := ItemOf(items, "demo", "grp/web", "12-paginate")
	if !ok || forgeName != "f1" || tracker.IID != 7 {
		t.Fatalf("%q %v %v", forgeName, tracker, ok)
	}
	if _, _, ok := ItemOf(items, "demo", "grp/web", "main"); ok {
		t.Fatal("the baseline belongs to no item")
	}
	if _, _, ok := ItemOf(items, "demo", "grp/web", ""); ok {
		t.Fatal("a detached tree belongs to no item")
	}
}

// A repo whose bare HEAD is develop and whose manifest sets no default_branch
// has its baseline in develop/, not main/.
func TestBaselineDirFollowsTheBareHead(t *testing.T) {
	f := newFixture(t, strings.ReplaceAll(gitManifest, `default_branch = "main"`+"\n", ""))
	projectDir := filepath.Join(f.root, "demo")
	gitRepoOn(t, filepath.Join(projectDir, "api"), "develop")
	gitRepoOn(t, filepath.Join(projectDir, "web"), "develop")
	loc, err := Locate(f.root, filepath.Join(projectDir, "web", "develop"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := BaselineDir(context.Background(), projectDir, loc.Manifest, "api")
	if err != nil || got != filepath.Join(projectDir, "api", "develop") {
		t.Fatalf("BaselineDir = %q, %v; want api/develop", got, err)
	}
}

func TestBaselineDirIsNamedAfterTheDefaultBranch(t *testing.T) {
	root, loc := gitFixture(t, nil, nil)
	ctx := context.Background()
	projectDir := filepath.Join(root, "demo")
	got, err := BaselineDir(ctx, projectDir, loc.Manifest, "api")
	if err != nil || got != filepath.Join(projectDir, "api", "main") {
		t.Fatalf("BaselineDir = %q, %v", got, err)
	}

	// default_branch = "release/1" puts the baseline in release-1/.
	m := *loc.Manifest
	m.Repos = map[string]config.Repo{"api": {Path: "grp/api", DefaultBranch: "release/1"}}
	if _, err := BaselineDir(ctx, projectDir, &m, "api"); err == nil || !strings.Contains(err.Error(), "release-1") {
		t.Fatalf("a missing baseline tree must name release-1: %v", err)
	}
	want := filepath.Join(projectDir, "api", "release-1")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := BaselineDir(ctx, projectDir, &m, "api"); err != nil || got != want {
		t.Fatalf("BaselineDir = %q, %v; want %q", got, err, want)
	}
}
