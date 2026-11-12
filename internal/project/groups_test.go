package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withGroups appends [repo_groups] tables to the manifest at src (they are
// separate tables, so they can follow the repos).
func withGroups(t *testing.T, src, tables string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, append(b, []byte("\n"+tables)...), 0o644); err != nil {
		t.Fatal(err)
	}
}

type groupFile struct {
	Folders []struct{ Name, Path string }
	Ade     struct {
		RepoGroup string `json:"repo_group"`
	}
	Settings   map[string]json.RawMessage
	Extensions map[string]any
}

func readGroupFile(t *testing.T, path string) groupFile {
	t.Helper()
	var g groupFile
	if err := json.Unmarshal([]byte(readFile(t, path)), &g); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return g
}

func TestGroupWorkspaceWrittenMergedAndRemoved(t *testing.T) {
	e := newEnv(t)
	web, admin, api := newRemote(t, "web"), newRemote(t, "admin"), newRemote(t, "api")
	src := e.manifest("demo", map[string]*remote{"web": web, "admin": admin, "api": api}, nil)
	withGroups(t, src, "[repo_groups.frontend]\nrepos = [\"web\", \"admin\"]\nworkspace = \"demo-frontend\"\n[repo_groups.shared]\nrepos = [\"web\", \"api\"]\n")
	file := filepath.Join(e.app.Root, "demo", "demo-frontend.code-workspace")

	rep, err := e.svc.Add(context.Background(), src)
	if err != nil || rep.Failed() {
		t.Fatalf("add: %v %+v", err, rep)
	}
	if len(rep.Setup) != 1 || len(rep.Setup[0].Written) != 1 || rep.Setup[0].Written[0] != "demo-frontend.code-workspace" {
		t.Errorf("setup = %+v", rep.Setup)
	}
	g := readGroupFile(t, file)
	wantWeb := filepath.Join(e.app.Root, "demo", "web", "main")
	if len(g.Folders) != 2 || g.Folders[0].Path != wantWeb || g.Folders[0].Name != "web (main)" ||
		g.Folders[1].Path != filepath.Join(e.app.Root, "demo", "admin", "main") {
		t.Errorf("folders = %+v", g.Folders)
	}
	if g.Ade.RepoGroup != "frontend" {
		t.Errorf("marker = %+v", g.Ade)
	}
	if _, ok := g.Settings["git.autoRepositoryDetection"]; !ok {
		t.Errorf("settings = %v", g.Settings)
	}
	if _, err := os.Stat(filepath.Join(e.app.Root, "demo", "shared.code-workspace")); err == nil {
		t.Error("a group without workspace must not get a file")
	}

	// A user's keys survive a regeneration; an unchanged file is not rewritten.
	user := strings.Replace(readFile(t, file), `"settings": {`, `"settings": {
    "editor.tabSize": 2,`, 1)
	user = strings.Replace(user, "{\n", "{\n  \"extensions\": {\"recommendations\": [\"a.b\"]},\n", 1)
	if err := os.WriteFile(file, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if rep, err = e.svc.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	g = readGroupFile(t, file)
	if string(g.Settings["editor.tabSize"]) != "2" || g.Extensions == nil {
		t.Errorf("user keys lost:\n%s", readFile(t, file))
	}
	if len(rep.Setup) != 1 || len(rep.Setup[0].Written) != 1 {
		// the file carried the user's keys, so merging rewrote it once
		t.Errorf("update setup = %+v", rep.Setup)
	}
	if rep, err = e.svc.Update(context.Background()); err != nil || len(rep.Setup) != 0 {
		t.Errorf("second update rewrote the file: %v %+v", err, rep.Setup)
	}

	// A copy of the generated file under another name is the user's: kept.
	copied := filepath.Join(e.app.Root, "demo", "mine.code-workspace")
	if err := os.WriteFile(copied, []byte(readFile(t, file)), 0o644); err != nil {
		t.Fatal(err)
	}

	// Dropping the workspace key removes the generated file.
	b, _ := os.ReadFile(src)
	if err := os.WriteFile(src, []byte(strings.Replace(string(b), "workspace = \"demo-frontend\"\n", "", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if rep, err = e.svc.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("workspace file kept after its key was removed")
	}
	if len(rep.Setup) != 1 || len(rep.Setup[0].Removed) != 1 {
		t.Errorf("setup = %+v", rep.Setup)
	}
	if _, err := os.Stat(copied); err != nil {
		t.Errorf("the user's copy was removed: %v", err)
	}
}

func TestGroupWorkspaceRemovedWithItsGroupTable(t *testing.T) {
	e := newEnv(t)
	web := newRemote(t, "web")
	src := e.manifest("demo", map[string]*remote{"web": web}, nil)
	base, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	withGroups(t, src, "[repo_groups.fe]\nrepos = [\"web\"]\nworkspace = \"fe\"\n")
	if rep, err := e.svc.Add(context.Background(), src); err != nil || rep.Failed() {
		t.Fatalf("add: %v %+v", err, rep)
	}
	file := filepath.Join(e.app.Root, "demo", "fe.code-workspace")
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, base, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("workspace file kept after its [repo_groups] table was removed")
	}
}

func TestGroupWorkspaceFollowsANonMainDefaultBranch(t *testing.T) {
	e := newEnv(t)
	web := newRemote(t, "web")
	gitCmd(t, web.work, "checkout", "--quiet", "-b", "develop")
	web.push("develop")
	gitCmd(t, web.bare, "symbolic-ref", "HEAD", "refs/heads/develop")
	src := e.manifest("demo", map[string]*remote{"web": web}, nil)
	withGroups(t, src, "[repo_groups.fe]\nrepos = [\"web\"]\nworkspace = \"fe\"\n")
	if rep, err := e.svc.Add(context.Background(), src); err != nil || rep.Failed() {
		t.Fatalf("add: %v %+v", err, rep)
	}
	g := readGroupFile(t, filepath.Join(e.app.Root, "demo", "fe.code-workspace"))
	if want := filepath.Join(e.app.Root, "demo", "web", "develop"); len(g.Folders) != 1 || g.Folders[0].Path != want {
		t.Errorf("folders = %+v, want %s", g.Folders, want)
	}
}

func TestGroupWorkspaceWithCommentsIsReported(t *testing.T) {
	e := newEnv(t)
	web := newRemote(t, "web")
	src := e.manifest("demo", map[string]*remote{"web": web}, nil)
	withGroups(t, src, "[repo_groups.fe]\nrepos = [\"web\"]\nworkspace = \"fe\"\n")
	if rep, err := e.svc.Add(context.Background(), src); err != nil || rep.Failed() {
		t.Fatalf("add: %v %+v", err, rep)
	}
	file := filepath.Join(e.app.Root, "demo", "fe.code-workspace")
	commented := "// mine\n" + readFile(t, file)
	if err := os.WriteFile(file, []byte(commented), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := e.svc.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, file) != commented {
		t.Error("a commented file was rewritten")
	}
	if len(rep.Setup) != 1 || len(rep.Setup[0].Warnings) != 1 || !strings.Contains(rep.Setup[0].Warnings[0], "not plain JSON") {
		t.Errorf("setup = %+v", rep.Setup)
	}
}

func TestGroupWorkspaceUsesTheDefaultBranchDirectory(t *testing.T) {
	e := newEnv(t)
	web := newRemote(t, "web")
	src := e.manifest("demo", map[string]*remote{"web": web}, nil)
	withGroups(t, src, "[repo_groups.fe]\nrepos = [\"web\"]\nworkspace = \"fe\"\n")
	if rep, err := e.svc.Add(context.Background(), src); err != nil || rep.Failed() {
		t.Fatalf("add: %v %+v", err, rep)
	}
	m, err := LoadManifest(filepath.Join(e.app.Root, "demo"))
	if err != nil {
		t.Fatal(err)
	}
	folders, err := GroupFolders(context.Background(), filepath.Join(e.app.Root, "demo"), m, "fe")
	if err != nil || len(folders) != 1 || filepath.Base(folders[0].Path) != "main" {
		t.Fatalf("folders = %+v, %v", folders, err)
	}
	if _, err := GroupFolders(context.Background(), filepath.Join(e.app.Root, "demo"), m, "nope"); err == nil || !strings.Contains(err.Error(), "have: fe") {
		t.Errorf("unknown group: %v", err)
	}
}

func TestGroupWorkspaceLeavesAHandWrittenFileAlone(t *testing.T) {
	e := newEnv(t)
	web := newRemote(t, "web")
	src := e.manifest("demo", map[string]*remote{"web": web}, nil)
	withGroups(t, src, "[repo_groups.fe]\nrepos = [\"web\"]\nworkspace = \"fe\"\n")
	own := filepath.Join(e.app.Root, "demo", "fe.code-workspace")
	if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(own, []byte(`{"folders":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := e.svc.Add(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, own); got != `{"folders":[]}` {
		t.Errorf("hand-written file rewritten:\n%s", got)
	}
	if len(rep.Setup) != 1 || len(rep.Setup[0].Warnings) != 1 || !strings.Contains(rep.Setup[0].Warnings[0], "not generated by ADE") {
		t.Errorf("setup = %+v", rep.Setup)
	}
}

func TestStatusGroupsRepos(t *testing.T) {
	e := newEnv(t)
	web, admin, api := newRemote(t, "web"), newRemote(t, "admin"), newRemote(t, "api")
	src := e.manifest("demo", map[string]*remote{"web": web, "admin": admin, "api": api}, nil)
	withGroups(t, src, "[repo_groups.zeta]\nrepos = [\"web\"]\n[repo_groups.alpha]\nrepos = [\"web\", \"admin\"]\nworkspace = \"alpha\"\n")
	if rep, err := e.svc.Add(context.Background(), src); err != nil || rep.Failed() {
		t.Fatalf("add: %v %+v", err, rep)
	}
	sts, err := e.svc.Status(context.Background(), "demo")
	if err != nil || len(sts) != 1 {
		t.Fatalf("status: %v %+v", err, sts)
	}
	st := sts[0]
	if len(st.RepoGroups) != 2 || st.RepoGroups[0].Name != "alpha" || st.RepoGroups[1].Name != "zeta" {
		t.Fatalf("groups = %+v", st.RepoGroups)
	}
	if a := st.RepoGroups[0]; a.Workspace != "alpha" || !a.Written || filepath.Base(a.File) != "alpha.code-workspace" {
		t.Errorf("alpha = %+v", a)
	}
	got := map[string][]string{}
	for _, r := range st.Repos {
		got[r.Name] = r.Groups
	}
	if strings.Join(got["web"], ",") != "alpha,zeta" || strings.Join(got["admin"], ",") != "alpha" || len(got["api"]) != 0 {
		t.Errorf("repo groups = %v", got)
	}
	for _, r := range st.Repos {
		if r.Name == "alpha.code-workspace" {
			t.Error("workspace file listed as a repo")
		}
	}
}
