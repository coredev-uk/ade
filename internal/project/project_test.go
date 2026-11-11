package project

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/git"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// remote is a local bare repository plus a working clone used to push to it.
type remote struct {
	t    *testing.T
	bare string
	work string
}

func newRemote(t *testing.T, name string) *remote {
	t.Helper()
	base := t.TempDir()
	r := &remote{t: t, bare: filepath.Join(base, name+".git"), work: filepath.Join(base, name+"-work")}
	gitCmd(t, base, "init", "--bare", "-b", "main", r.bare)
	gitCmd(t, base, "clone", "--quiet", r.bare, r.work)
	r.commit("README", "one")
	gitCmd(t, r.work, "push", "--quiet", "origin", "main")
	return r
}

// commit writes content to file and commits it on the work clone's current branch.
func (r *remote) commit(file, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.work, file), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	gitCmd(r.t, r.work, "add", file)
	gitCmd(r.t, r.work, "commit", "--quiet", "-m", "change "+file)
}

func (r *remote) push(branch string) {
	r.t.Helper()
	gitCmd(r.t, r.work, "push", "--quiet", "origin", branch)
}

func (r *remote) branch(name string) {
	r.t.Helper()
	gitCmd(r.t, r.work, "checkout", "--quiet", "-b", name)
	r.commit(strings.ReplaceAll(name, "/", "_"), "x")
	r.push(name)
	gitCmd(r.t, r.work, "checkout", "--quiet", "main")
}

func (r *remote) url() string { return "file://" + r.bare }

type env struct {
	t      *testing.T
	app    *app.App
	svc    *Service
	events <-chan events.Event
	dir    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	dir := t.TempDir()
	al, err := audit.Open(filepath.Join(dir, "records"), "user")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.File = filepath.Join(dir, "cfg", "config.toml")
	cfg.Update.Concurrency = 2
	cfg.Update.FeatureBranches = "ff-if-clean"
	a := &app.App{Config: &cfg, Root: filepath.Join(dir, "root"), Audit: al, Bus: events.NewBus()}
	a.Paths.CacheDir = filepath.Join(dir, "cache")
	ch, cancel := a.Bus.Subscribe(256)
	t.Cleanup(cancel)
	return &env{t: t, app: a, svc: New(a, "ade:update"), events: ch, dir: dir}
}

// manifest writes a manifest with the given repos (name -> remote) and returns its path.
func (e *env) manifest(name string, repos map[string]*remote, extra map[string]string) string {
	e.t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "name = %q\n", name)
	for n, r := range repos {
		fmt.Fprintf(&b, "[repos.%s]\nurl = %q\n%s\n", n, r.url(), extra[n])
	}
	p := filepath.Join(e.dir, name+".ade.toml")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) drain() []events.Event {
	var out []events.Event
	for {
		select {
		case ev := <-e.events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func kinds(evs []events.Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	sort.Strings(out)
	return out
}

func find(evs []events.Event, kind string) *events.Event {
	for i := range evs {
		if evs[i].Kind == kind {
			return &evs[i]
		}
	}
	return nil
}

func (e *env) tree(project, repo, tree string) string {
	return filepath.Join(e.app.Root, project, repo, tree)
}

func TestAddCreatesLayout(t *testing.T) {
	e := newEnv(t)
	r := newRemote(t, "api")
	r.branch("feature/x")
	src := e.manifest("demo", map[string]*remote{"api": r}, nil)

	rep, err := e.svc.Add(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed() || !rep.Results[0].Cloned {
		t.Fatalf("report = %+v", rep)
	}
	repo := filepath.Join(e.app.Root, "demo", "api")
	for _, p := range []string{".bare", ".git", "main/README", "../.project.toml"} {
		if !exists(filepath.Join(repo, p)) {
			t.Errorf("missing %s", p)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(repo, ".git")); string(got) != "gitdir: ./.bare\n" {
		t.Errorf(".git file = %q", got)
	}
	main := e.tree("demo", "api", "main")
	if up := gitCmd(t, main, "rev-parse", "--abbrev-ref", "@{u}"); up != "origin/main" {
		t.Errorf("upstream = %q", up)
	}
	if heads := gitCmd(t, repo, "for-each-ref", "--format=%(refname:short)", "refs/heads"); heads != "main" {
		t.Errorf("local heads = %q, want only main", heads)
	}
	if rem := gitCmd(t, repo, "for-each-ref", "--format=%(refname:short)", "refs/remotes/origin"); !strings.Contains(rem, "origin/feature/x") {
		t.Errorf("remote-tracking refs = %q", rem)
	}
	if !exists(filepath.Join(repo, ".bare")) || strings.Contains(gitCmd(t, repo, "config", "--get", "remote.origin.fetch"), "heads/*:refs/heads") {
		t.Error("fetch refspec not fixed")
	}
	if got := gitCmd(t, repo, "worktree", "list"); !strings.Contains(got, "main") {
		t.Errorf("repo-level git does not see worktrees: %q", got)
	}
	if k := kinds(e.drain()); len(k) != 1 || k[0] != "git.clone.ok" {
		t.Errorf("events = %v", k)
	}
}

func TestAddRegistersConfigOnceAndIsIdempotent(t *testing.T) {
	e := newEnv(t)
	r := newRemote(t, "api")
	src := e.manifest("demo", map[string]*remote{"api": r}, nil)
	if err := os.MkdirAll(filepath.Dir(e.app.Config.File), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := "# my config\n[user]\nhandle = \"me\"\n"
	if err := os.WriteFile(e.app.Config.File, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.Add(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	e.drain()
	headBefore := gitCmd(t, e.tree("demo", "api", "main"), "rev-parse", "HEAD")
	rep, err := e.svc.Add(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Results[0].Cloned || rep.Failed() {
		t.Errorf("second add did work: %+v", rep)
	}
	if evs := e.drain(); len(evs) != 0 {
		t.Errorf("second add published %v", kinds(evs))
	}
	if got := gitCmd(t, e.tree("demo", "api", "main"), "rev-parse", "HEAD"); got != headBefore {
		t.Error("baseline moved on re-add")
	}
	data, _ := os.ReadFile(e.app.Config.File)
	if !strings.HasPrefix(string(data), existing) {
		t.Errorf("existing config rewritten: %q", data)
	}
	if n := strings.Count(string(data), "[[projects]]"); n != 1 {
		t.Errorf("[[projects]] count = %d, want 1:\n%s", n, data)
	}
	reloaded, err := config.Load(e.app.Config.File)
	if err != nil || len(reloaded.Projects) != 1 || reloaded.Projects[0].Source != src {
		t.Errorf("reloaded projects = %+v, err %v", reloaded.Projects, err)
	}
}

func TestAddFillsGaps(t *testing.T) {
	e := newEnv(t)
	r := newRemote(t, "api")
	src := e.manifest("demo", map[string]*remote{"api": r}, nil)
	if _, err := e.svc.Add(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	// A deleted baseline tree is recreated: its stale worktree record is pruned by ADE.
	main := e.tree("demo", "api", "main")
	if err := os.RemoveAll(main); err != nil {
		t.Fatal(err)
	}
	rep, err := e.svc.Add(context.Background(), src)
	if err != nil || rep.Failed() {
		t.Fatalf("add: %v %+v", err, rep)
	}
	if !exists(filepath.Join(main, "README")) {
		t.Error("baseline not recreated")
	}
}

func TestUpdateFastForwardsCleanBaseline(t *testing.T) {
	e := newEnv(t)
	r := newRemote(t, "api")
	src := e.manifest("demo", map[string]*remote{"api": r}, nil)
	if _, err := e.svc.Add(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	e.drain()
	r.commit("new", "data")
	r.push("main")

	var progress []Progress
	e.svc.OnProgress = func(p Progress) { progress = append(progress, p) }
	rep, err := e.svc.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(e.tree("demo", "api", "main"), "new")) {
		t.Error("baseline not fast-forwarded")
	}
	evs := e.drain()
	if k := kinds(evs); strings.Join(k, ",") != "git.pull.ok,git.update.done" {
		t.Errorf("events = %v", k)
	}
	done := find(evs, "git.update.done")
	if done.Severity != events.Success || done.Title != "pulled 1 repo" {
		t.Errorf("summary = %v %q", done.Severity, done.Title)
	}
	if rep.Failed() {
		t.Error("report failed")
	}
	if len(progress) < 2 || progress[0] != (Progress{0, 1}) || progress[len(progress)-1] != (Progress{1, 1}) {
		t.Errorf("progress = %v", progress)
	}
	entries, err := e.app.Audit.Query(audit.Filter{Action: "git.pull"})
	if err != nil || len(entries) != 1 || entries[0].Actor != "ade:update" {
		t.Errorf("audit = %+v err %v", entries, err)
	}
}

func TestUpdateRecreatesDeletedBaselineAndCatchesUp(t *testing.T) {
	e := newEnv(t)
	r := newRemote(t, "api")
	src := e.manifest("demo", map[string]*remote{"api": r}, nil)
	if _, err := e.svc.Add(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	main := e.tree("demo", "api", "main")
	if err := os.RemoveAll(main); err != nil {
		t.Fatal(err)
	}
	r.commit("new", "data")
	r.push("main")
	rep, err := e.svc.Update(context.Background())
	if err != nil || rep.Failed() {
		t.Fatalf("update: %v %+v", err, rep)
	}
	if !exists(filepath.Join(main, "new")) {
		t.Error("recreated baseline is behind origin")
	}
}

func TestUpdateNothingToDo(t *testing.T) {
	e := newEnv(t)
	r := newRemote(t, "api")
	src := e.manifest("demo", map[string]*remote{"api": r}, nil)
	if _, err := e.svc.Add(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if _, err := e.svc.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	evs := e.drain()
	if len(evs) != 1 || evs[0].Title != "all repos up to date" || evs[0].Severity != events.Success || !evs[0].Transient {
		t.Errorf("events = %+v, want one transient success summary", evs)
	}
}

func TestUpdateSkipsDirtyBaseline(t *testing.T) {
	e := newEnv(t)
	r := newRemote(t, "api")
	src := e.manifest("demo", map[string]*remote{"api": r}, nil)
	if _, err := e.svc.Add(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	e.drain()
	main := e.tree("demo", "api", "main")
	if err := os.WriteFile(filepath.Join(main, "README"), []byte("local edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := gitCmd(t, main, "rev-parse", "HEAD")
	r.commit("new", "data")
	r.push("main")

	rep, err := e.svc.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := gitCmd(t, main, "rev-parse", "HEAD"); got != before {
		t.Error("dirty baseline moved")
	}
	evs := e.drain()
	skipped := find(evs, "git.pull.skipped")
	if skipped == nil || !strings.Contains(skipped.Title, "dirty") || skipped.Subject != "api/main" {
		t.Fatalf("skipped event = %+v", skipped)
	}
	done := find(evs, "git.update.done")
	if done.Severity != events.Warn || done.Title != "1 skipped (api/main dirty)" {
		t.Errorf("summary = %v %q", done.Severity, done.Title)
	}
	if rep.Failed() {
		t.Error("a skip is not a failure")
	}
}

// featureTree adds a worktree for branch the way "start work" will.
func (e *env) featureTree(project, repo, branch string) string {
	e.t.Helper()
	bare := filepath.Join(e.app.Root, project, repo, ".bare")
	dir := filepath.Join(e.app.Root, project, repo, git.BranchDir(branch))
	if err := (git.Client{}).AddTracking(context.Background(), bare, dir, branch); err != nil {
		e.t.Fatal(err)
	}
	return dir
}

func TestUpdateFeatureTrees(t *testing.T) {
	e := newEnv(t)
	r := newRemote(t, "api")
	r.branch("feature/behind")
	r.branch("feature/diverged")
	src := e.manifest("demo", map[string]*remote{"api": r}, nil)
	if _, err := e.svc.Add(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	behind := e.featureTree("demo", "api", "feature/behind")
	diverged := e.featureTree("demo", "api", "feature/diverged")
	e.drain()

	gitCmd(t, diverged, "commit", "--quiet", "--allow-empty", "-m", "local only")
	divergedHead := gitCmd(t, diverged, "rev-parse", "HEAD")
	for _, b := range []string{"feature/behind", "feature/diverged"} {
		gitCmd(t, r.work, "checkout", "--quiet", b)
		r.commit("more_"+git.BranchDir(b), "y")
		r.push(b)
	}
	gitCmd(t, r.work, "checkout", "--quiet", "main")

	rep, err := e.svc.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(behind, "more_feature-behind")) {
		t.Error("strictly-behind clean feature tree was not fast-forwarded")
	}
	if got := gitCmd(t, diverged, "rev-parse", "HEAD"); got != divergedHead {
		t.Error("diverged feature tree moved")
	}
	evs := e.drain()
	if ok := find(evs, "git.pull.ok"); ok == nil || ok.Subject != "api/feature-behind" {
		t.Errorf("pull.ok = %+v", ok)
	}
	if sk := find(evs, "git.pull.skipped"); sk == nil || sk.Subject != "api/feature-diverged" || !strings.Contains(sk.Title, "diverged") {
		t.Errorf("pull.skipped = %+v", sk)
	}
	if rep.Failed() {
		t.Error("unexpected failure")
	}
}

func TestUpdateNotifyOnlyLeavesFeatureTrees(t *testing.T) {
	e := newEnv(t)
	e.app.Config.Update.FeatureBranches = "notify-only"
	r := newRemote(t, "api")
	r.branch("feature/x")
	src := e.manifest("demo", map[string]*remote{"api": r}, nil)
	if _, err := e.svc.Add(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	tree := e.featureTree("demo", "api", "feature/x")
	before := gitCmd(t, tree, "rev-parse", "HEAD")
	gitCmd(t, r.work, "checkout", "--quiet", "feature/x")
	r.commit("more", "y")
	r.push("feature/x")
	gitCmd(t, r.work, "checkout", "--quiet", "main")
	e.drain()

	if _, err := e.svc.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gitCmd(t, tree, "rev-parse", "HEAD") != before {
		t.Error("notify-only moved a feature tree")
	}
	if sk := find(e.drain(), "git.pull.skipped"); sk == nil || !strings.Contains(sk.Title, "notify-only") {
		t.Errorf("no notify-only skip event")
	}
}

func TestHookFailureIsReported(t *testing.T) {
	e := newEnv(t)
	good, bad := newRemote(t, "good"), newRemote(t, "bad")
	src := e.manifest("demo", map[string]*remote{"good": good, "bad": bad}, map[string]string{
		"bad": "hooks = { post_clone = [\"echo ran > hook-marker\", \"echo oops >&2; exit 3\"] }",
	})
	rep, err := e.svc.Add(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Failed() {
		t.Fatal("hook failure not reported")
	}
	if !exists(filepath.Join(e.tree("demo", "bad", "main"), "hook-marker")) {
		t.Error("hook did not run in the baseline tree")
	}
	if !exists(e.tree("demo", "good", "main")) {
		t.Error("a failing hook stopped the other repo")
	}
	evs := e.drain()
	hf := find(evs, "git.hook.failed")
	if hf == nil || hf.Severity != events.Error || !strings.Contains(hf.Detail, "oops") {
		t.Errorf("hook.failed = %+v", hf)
	}
	if entries, _ := e.app.Audit.Query(audit.Filter{Action: "git.hook"}); len(entries) != 2 {
		t.Errorf("hook audit entries = %d, want 2 (one ok, one error)", len(entries))
	}
}

func TestCloneFailureDoesNotStopOthersAndRetries(t *testing.T) {
	e := newEnv(t)
	good := newRemote(t, "good")
	missing := &remote{t: t, bare: filepath.Join(t.TempDir(), "nope.git")}
	src := e.manifest("demo", map[string]*remote{"good": good, "gone": missing}, nil)
	rep, err := e.svc.Add(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Failed() || !exists(e.tree("demo", "good", "main")) {
		t.Fatalf("report = %+v", rep)
	}
	if find(e.drain(), "git.clone.failed") == nil {
		t.Error("no git.clone.failed event")
	}
	if exists(filepath.Join(e.app.Root, "demo", "gone", ".bare")) {
		t.Error("failed clone left a .bare behind")
	}
}

func TestRemovedRepoIsFlaggedNotDeleted(t *testing.T) {
	e := newEnv(t)
	api, web := newRemote(t, "api"), newRemote(t, "web")
	src := e.manifest("demo", map[string]*remote{"api": api, "web": web}, nil)
	if _, err := e.svc.Add(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	e.drain()
	e.manifest("demo", map[string]*remote{"api": api}, nil)

	if _, err := e.svc.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	rm := find(e.drain(), "project.repo.removed")
	if rm == nil || rm.Subject != "web" {
		t.Fatalf("removed event = %+v", rm)
	}
	if !exists(e.tree("demo", "web", "main")) {
		t.Error("removed repo was deleted")
	}
	st, err := e.svc.Status(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	flags := map[string]bool{}
	for _, r := range st[0].Repos {
		flags[r.Name] = r.InManifest
	}
	if len(flags) != 2 || !flags["api"] || flags["web"] {
		t.Errorf("in_manifest flags = %v", flags)
	}
}

func TestInvalidManifestKeepsSnapshot(t *testing.T) {
	e := newEnv(t)
	r := newRemote(t, "api")
	src := e.manifest("demo", map[string]*remote{"api": r}, nil)
	if _, err := e.svc.Add(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if err := os.WriteFile(src, []byte("name = [broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.commit("new", "data")
	r.push("main")

	rep, err := e.svc.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	evs := e.drain()
	if find(evs, "project.manifest.invalid") == nil {
		t.Errorf("events = %v", kinds(evs))
	}
	if !exists(filepath.Join(e.tree("demo", "api", "main"), "new")) {
		t.Error("project stopped updating after a bad manifest")
	}
	if len(rep.Invalid) != 1 {
		t.Errorf("invalid = %v", rep.Invalid)
	}
	if done := find(evs, "git.update.done"); done.Severity != events.Warn {
		t.Errorf("summary severity = %v", done.Severity)
	}
}

func TestAddRejectsInvalidManifest(t *testing.T) {
	e := newEnv(t)
	bad := filepath.Join(e.dir, "bad.toml")
	if err := os.WriteFile(bad, []byte("name = \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Add(context.Background(), bad); err == nil {
		t.Fatal("expected error")
	}
	if len(e.app.Config.Projects) != 0 || exists(e.app.Config.File) {
		t.Error("an invalid manifest was registered")
	}
	if err := os.WriteFile(bad, []byte("name = \"../escape\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Add(context.Background(), bad); err == nil {
		t.Fatal("path-like project name accepted")
	}
}

func TestStatusReportsTrees(t *testing.T) {
	e := newEnv(t)
	r := newRemote(t, "api")
	r.branch("feature/x")
	src := e.manifest("demo", map[string]*remote{"api": r}, nil)
	if _, err := e.svc.Add(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	feat := e.featureTree("demo", "api", "feature/x")
	gitCmd(t, feat, "commit", "--quiet", "--allow-empty", "-m", "ahead")
	if err := os.WriteFile(filepath.Join(feat, "scratch"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := e.svc.Status(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	repo := st[0].Repos[0]
	if !repo.Present || repo.Baseline == nil || repo.Baseline.Dirty || !repo.Baseline.HasUpstream {
		t.Errorf("baseline = %+v", repo.Baseline)
	}
	if len(repo.Worktrees) != 1 {
		t.Fatalf("worktrees = %+v", repo.Worktrees)
	}
	if w := repo.Worktrees[0]; w.Name != "feature-x" || !w.Dirty || w.Ahead != 1 || w.Behind != 0 {
		t.Errorf("feature tree = %+v", w)
	}
	if _, err := e.svc.Status(context.Background(), "nope"); err == nil {
		t.Error("unknown project accepted")
	}
}

func TestListShowsRegisteredProjects(t *testing.T) {
	e := newEnv(t)
	r := newRemote(t, "api")
	src := e.manifest("demo", map[string]*remote{"api": r}, nil)
	e.app.Config.Projects = []config.ProjectRef{{Source: filepath.Join(e.dir, "pending.ade.toml")}}
	if _, err := e.svc.Add(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	infos, err := e.svc.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 || infos[0].Name != "" || infos[1].Name != "demo" || infos[1].Repos != 1 || infos[1].LastUpdate.IsZero() {
		t.Errorf("infos = %+v", infos)
	}
}

func TestManifestFromGitSource(t *testing.T) {
	e := newEnv(t)
	api := newRemote(t, "api")
	src := e.manifest("demo", map[string]*remote{"api": api}, nil)
	manifestData, _ := os.ReadFile(src)

	shared := newRemote(t, "shared")
	if err := os.MkdirAll(filepath.Join(shared.work, "ade"), 0o755); err != nil {
		t.Fatal(err)
	}
	shared.commit("ade/project.ade.toml", string(manifestData))
	shared.push("main")

	gitSrc := "git+" + shared.url() + "#ade/project.ade.toml"
	if _, err := e.svc.Add(context.Background(), gitSrc); err != nil {
		t.Fatal(err)
	}
	if !exists(e.tree("demo", "api", "main")) {
		t.Error("project not cloned from git manifest")
	}

	shared.commit("ade/project.ade.toml", strings.Replace(string(manifestData), "name = \"demo\"", "name = \"demo2\"", 1))
	shared.push("main")
	if _, err := e.svc.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(e.app.Root, "demo2", ".project.toml")) {
		t.Error("cached manifest clone was not refreshed")
	}
	if _, err := e.svc.fetchRaw(context.Background(), "git+"+shared.url()+"#../etc/passwd"); err == nil {
		t.Error("path traversal accepted")
	}
}

func TestManifestFromHTTP(t *testing.T) {
	e := newEnv(t)
	api := newRemote(t, "api")
	src := e.manifest("web", map[string]*remote{"api": api}, nil)
	data, _ := os.ReadFile(src)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("PRIVATE-TOKEN") != "" {
			http.Error(w, "credentials over plain http", http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/m.toml" {
			http.NotFound(w, r)
			return
		}
		w.Write(data) //nolint:errcheck
	}))
	defer srv.Close()
	e.app.Config.Forges = map[string]config.Forge{"f": {Kind: "gitlab", URL: srv.URL, TokenEnv: "ADE_TEST_TOKEN"}}
	t.Setenv("ADE_TEST_TOKEN", "secret")

	if _, err := e.svc.Add(context.Background(), srv.URL+"/m.toml"); err != nil {
		t.Fatal(err)
	}
	if !exists(e.tree("web", "api", "main")) {
		t.Error("not cloned")
	}
	if _, err := e.svc.Add(context.Background(), srv.URL+"/missing.toml"); err == nil {
		t.Error("404 accepted")
	}
}

func TestCloneURL(t *testing.T) {
	gl := config.Forge{Kind: "gitlab", URL: "https://gitlab.example.com/", GitProtocol: "https"}
	tests := []struct {
		name  string
		forge config.Forge
		repo  config.Repo
		want  string
		err   bool
	}{
		{"https", gl, config.Repo{Path: "grp/api"}, "https://gitlab.example.com/grp/api.git", false},
		{"ssh default", config.Forge{Kind: "github"}, config.Repo{Path: "org/api"}, "git@github.com:org/api.git", false},
		{"ssh custom host", config.Forge{Kind: "gitlab", URL: "https://git.example.com:8443"}, config.Repo{Path: "/g/api.git"}, "git@git.example.com:g/api.git", false},
		{"explicit url wins", gl, config.Repo{URL: "file:///x.git", Path: "grp/api"}, "file:///x.git", false},
		{"nothing", gl, config.Repo{}, "", true},
		{"no host", config.Forge{}, config.Repo{Path: "a/b"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cloneURL(tt.forge, tt.repo)
			if (err != nil) != tt.err || got != tt.want {
				t.Errorf("cloneURL = %q, %v; want %q (err %v)", got, err, tt.want, tt.err)
			}
		})
	}
}

func TestMatchForgePrefersManifestForge(t *testing.T) {
	cfg := &config.Config{Forges: map[string]config.Forge{
		"a": {Kind: "gitlab", URL: "https://git.example.com"},
		"b": {Kind: "gitlab", URL: "https://GIT.example.com/"},
		"c": {Kind: "github"},
	}}
	if n, _, ok := matchForge(cfg, "b", "https://git.example.com/x/y.git"); !ok || n != "b" {
		t.Errorf("preferred forge lost: %q %v", n, ok)
	}
	if n, _, ok := matchForge(cfg, "", "https://git.example.com/x/y.git"); !ok || n != "a" {
		t.Errorf("deterministic fallback = %q %v", n, ok)
	}
	if _, _, ok := matchForge(cfg, "a", "https://evil.example.org/x.git"); ok {
		t.Error("token would be sent to an unrelated host")
	}
}

func TestReportTitle(t *testing.T) {
	rep := Report{Results: []RepoResult{
		{Repo: "a", Pulled: []string{"a/main"}},
		{Repo: "b", Pulled: []string{"b/main"}},
		{Repo: "web", Skipped: []Skip{{"web/main", "dirty"}}},
		{Repo: "api", Failures: []Failure{{Op: "fetch", Reason: "auth"}}},
	}}
	want := "pulled 2 repos · 1 skipped (web/main dirty) · 1 failed (api: auth)"
	if got := rep.Title(); got != want {
		t.Errorf("title = %q", got)
	}
	if rep.Severity() != events.Error {
		t.Error("severity")
	}
}

// A local override (PLAN 2) is merged before validation and stored in the
// snapshot, so every later reader of the project sees it.
func TestLocalOverrideReachesTheSnapshot(t *testing.T) {
	e := newEnv(t)
	api := newRemote(t, "api")
	src := e.manifest("demo", map[string]*remote{"api": api}, map[string]string{"api": `path = "acme/api"`})
	overlay := config.LocalPath(src)
	if err := os.WriteFile(overlay, []byte("[repos.api]\napp = [\"api\"]\n[repos.api.tasks.dev]\ncmd = \"make dev\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Add(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(filepath.Join(e.app.Root, "demo"))
	if err != nil {
		t.Fatal(err)
	}
	r := m.Repos["api"]
	if r.Path != "acme/api" || r.URL != api.url() || !slices.Equal(r.App, []string{"api"}) || r.Tasks["dev"].Cmd != "make dev" {
		t.Fatalf("snapshot repo = %+v", r)
	}

	// Changing only the override is picked up by the next update.
	if err := os.WriteFile(overlay, []byte("[repos.api.tasks.dev]\ncmd = \"make serve\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m, err = LoadManifest(filepath.Join(e.app.Root, "demo")); err != nil || m.Repos["api"].Tasks["dev"].Cmd != "make serve" {
		t.Fatalf("after update: %+v %v", m, err)
	}
}

func TestLocalOverrideInvalidMergeRejectsTheManifest(t *testing.T) {
	e := newEnv(t)
	api := newRemote(t, "api")
	src := e.manifest("demo", map[string]*remote{"api": api}, nil)
	if err := os.WriteFile(config.LocalPath(src), []byte("[repos.api.tasks.dev]\ncmd = \"x\"\nrequires = [\"api.nope\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Add(context.Background(), src); err == nil {
		t.Fatal("an override that breaks the manifest must be rejected before anything is cloned")
	}
	if exists(filepath.Join(e.app.Root, "demo")) {
		t.Error("project directory created despite the invalid merged manifest")
	}
}

func TestLocalOverrideOfRemoteManifestLivesInTheConfigDir(t *testing.T) {
	e := newEnv(t)
	for source, want := range map[string]string{
		"git+file:///srv/shared.git#ade/project.ade.toml": "project.ade.local.toml",
		"https://example.com/m/shop.ade.toml?ref=main":    "shop.ade.local.toml",
	} {
		cfgDir := filepath.Dir(e.app.Config.File)
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(cfgDir, want)
		if err := os.WriteFile(path, []byte("name = \"x\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := e.svc.localOverlay(source)
		if err != nil || got.path != path {
			t.Errorf("localOverlay(%q) = %q, %v; want %q", source, got.path, err, path)
		}
	}
}
