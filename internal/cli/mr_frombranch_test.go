package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/startwork"
)

const fromBranchAPI = "ade-demo-env/api"

// startedOutsideADE puts a branch with one commit on the fake forge and its
// git remote, and checks it out as a worktree the way `git worktree add` would.
func startedOutsideADE(t *testing.T, e *cliEnv, root, branch string, pushed bool) string {
	t.Helper()
	bare := filepath.Join(root, "demo-gl", "api", ".bare")
	tree := filepath.Join(root, "demo-gl", "api", branch)
	if pushed {
		if _, err := gitlab.New(e.srv.URL, fake.Token).CreateBranch(context.Background(), fromBranchAPI, branch, "main"); err != nil {
			t.Fatal(err)
		}
		origin := gitOut(t, bare, "remote", "get-url", "origin")
		work := filepath.Join(t.TempDir(), "w")
		gitAt(t, filepath.Dir(work), "clone", "--quiet", origin, work)
		gitAt(t, work, "checkout", "--quiet", "-b", branch)
		gitAt(t, work, "commit", "--quiet", "--allow-empty", "-m", "Retry the cache fill")
		gitAt(t, work, "push", "--quiet", "origin", branch)
		gitAt(t, bare, "fetch", "--quiet", "origin")
		if err := (git.Client{}).AddTracking(context.Background(), bare, tree, branch); err != nil {
			t.Fatal(err)
		}
		return tree
	}
	gitAt(t, bare, "worktree", "add", "--quiet", "-b", branch, tree, "origin/main")
	return tree
}

func TestMRFromBranchOpensADraftFromTheTemplate(t *testing.T) {
	e, root := reviewOpenEnv(t)
	e.srv.AddFile(fromBranchAPI, startwork.TemplateDir+"/Default.md", "## What\n\nDescribe it.\n\n/assign_reviewer @bob\n")
	tree := startedOutsideADE(t, e, root, "fix-retry-cache", true)

	out, err := e.mr("from-branch", tree, "--dry-run", "--closes", "ade-demo-env/tracker#5", "--strip-actions=assign_reviewer")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"dry-run: would open draft MR fix-retry-cache → main: Retry the cache fill", "│ Closes ade-demo-env/tracker#5", "│ ## What", "quick action stripped"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "│ /assign_reviewer") {
		t.Errorf("the stripped action is in the body:\n%s", out)
	}
	if mrs, _ := gitlab.New(e.srv.URL, fake.Token).ListMRs(context.Background(), forge.MRQuery{Project: fromBranchAPI, SourceBranch: "fix-retry-cache"}); len(mrs) != 0 {
		t.Fatal("--dry-run opened the MR")
	}

	out, err = e.mr("from-branch", tree, "--closes", "ade-demo-env/tracker#5", "--strip-actions")
	if err != nil || !strings.Contains(out, "opened draft MR") {
		t.Fatalf("%v\n%s", err, out)
	}
	mrs, err := gitlab.New(e.srv.URL, fake.Token).ListMRs(context.Background(), forge.MRQuery{Project: fromBranchAPI, SourceBranch: "fix-retry-cache"})
	if err != nil || len(mrs) != 1 {
		t.Fatalf("mrs = %+v, %v", mrs, err)
	}
	if title, _ := e.srv.Title(mrs[0].Ref); !strings.HasPrefix(title, "Draft:") {
		t.Errorf("title %q: not a draft", title)
	}
	if es := e.audited("forge.mr.create"); len(es) != 2 || !es[0].DryRun || es[1].Outcome != "ok" || es[1].Params["draft"] != true {
		t.Fatalf("audit = %+v", es)
	}

	// A second run reports the open MR instead of making another.
	asJSON(t)
	var got mrFromBranch
	if err := json.Unmarshal([]byte(e.mustMR("from-branch", tree)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Created || got.MR == nil || got.MR.IID != mrs[0].Ref.IID || got.Repo != "api" || got.Branch != "fix-retry-cache" {
		t.Fatalf("second run = %+v", got)
	}
	if len(e.audited("forge.mr.create")) != 2 {
		t.Error("the second run wrote again")
	}
}

func TestMRFromBranchReadyAndTitleFlags(t *testing.T) {
	e, root := reviewOpenEnv(t)
	tree := startedOutsideADE(t, e, root, "fix-retry-cache", true)
	asJSON(t)
	var got mrFromBranch
	if err := json.Unmarshal([]byte(e.mustMR("from-branch", tree, "--dry-run", "--ready", "--title", "Own title", "--target", "develop")), &got); err != nil {
		t.Fatal(err)
	}
	if got.Draft || got.Title != "Own title" || got.Target != "develop" || !got.DryRun {
		t.Fatalf("%+v", got)
	}
}

func TestMRFromBranchRefusesWhatCannotHaveAnMR(t *testing.T) {
	e, root := reviewOpenEnv(t)
	unpushed := startedOutsideADE(t, e, root, "local-only", false)
	if _, err := e.mr("from-branch", unpushed); err == nil || !strings.Contains(err.Error(), "git push -u origin local-only") {
		t.Errorf("unpushed branch: %v", err)
	}
	if _, err := e.mr("from-branch", filepath.Join(root, "demo-gl", "api", "main")); err == nil || !strings.Contains(err.Error(), "target branch itself") {
		t.Errorf("default branch: %v", err)
	}
	if _, err := e.mr("from-branch", t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a worktree") {
		t.Errorf("a directory outside the projects root: %v", err)
	}
}
