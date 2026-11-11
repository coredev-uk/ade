package bootstrap_test

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/bootstrap"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
	ghfake "github.com/coredev-uk/ade/internal/forge/github/fake"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

var fakeNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

const userForge = "work"

// userConfig is the config a team lead has: one forge called "work".
func userConfig() *config.Config {
	c := config.Defaults()
	c.Forges = map[string]config.Forge{userForge: {Kind: "gitlab"}}
	return &c
}

func opts(extra ...func(*bootstrap.Options)) bootstrap.Options {
	o := bootstrap.Options{ForgeName: userForge, Group: fake.GroupPath, Now: func() time.Time { return fakeNow }}
	for _, f := range extra {
		f(&o)
	}
	return o
}

func gitlabFake(t *testing.T, tier fake.Tier) (*gitlab.Client, *fake.Server) {
	t.Helper()
	srv := fake.New(tier)
	t.Cleanup(srv.Close)
	return gitlab.New(srv.URL, fake.Token), srv
}

// seedRoots gives the sandbox repos the root files of the sandbox apps.
func seedRoots(srv *fake.Server) {
	srv.AddFile("ade-demo-env/api", "go.mod", "module example.com/api\n")
	srv.AddFile("ade-demo-env/api", "main.go", "package main\n")
	srv.AddFile("ade-demo-env/web", "package.json", `{"scripts":{"dev":"vite","test":"vitest"}}`)
	srv.AddFile("ade-demo-env/web", "package-lock.json", "{}")
	srv.AddFile("ade-demo-env/api", ".gitlab/merge_request_templates/Default.md", "## Summary\n")
}

// roundTrip parses the rendered text and checks it is exactly the draft's
// manifest, valid for the user's config and free of unknown keys.
func roundTrip(t *testing.T, d *bootstrap.Draft) *config.Manifest {
	t.Helper()
	m, err := config.ParseManifest([]byte(d.TOML), "generated")
	if err != nil {
		t.Fatalf("the rendered manifest does not parse: %v\n%s", err, d.TOML)
	}
	if len(m.Undecoded) > 0 {
		t.Errorf("unknown keys in the rendered manifest: %v", m.Undecoded)
	}
	issues, err := d.Check(userConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, is := range issues {
		t.Errorf("validation: %s", is)
	}
	want := d.Manifest
	m.Source, m.Undecoded = "", nil
	if !reflect.DeepEqual(*m, normalised(want)) {
		t.Errorf("rendered text differs from the draft manifest\nparsed: %+v\ndraft:  %+v\n%s", *m, normalised(want), d.TOML)
	}
	return m
}

// normalised makes empty maps nil, as parsing leaves them.
func normalised(m config.Manifest) config.Manifest {
	if len(m.Labels.Workflow.Stage) == 0 {
		m.Labels.Workflow.Stage = nil
	}
	if len(m.Groups) == 0 {
		m.Groups = nil
	}
	if len(m.Workspaces) == 0 {
		m.Workspaces = nil
	}
	m.Repos = maps.Clone(m.Repos)
	for k, r := range m.Repos {
		if len(r.Tasks) == 0 {
			r.Tasks = nil
		}
		m.Repos[k] = r
	}
	return m
}

func TestDiscoverGitLabFree(t *testing.T) {
	c, srv := gitlabFake(t, fake.Free)
	seedRoots(srv)
	srv.ArchiveProject(srv.AddProject("old-service"))

	d, err := bootstrap.Discover(context.Background(), c, opts())
	if err != nil {
		t.Fatal(err)
	}
	m := roundTrip(t, d)

	if m.Tracker.Project != "ade-demo-env/tracker" {
		t.Errorf("tracker = %q", m.Tracker.Project)
	}
	if m.Tracker.Iterations.Source != "milestones" {
		t.Errorf("iterations = %+v: a Free group has milestones", m.Tracker.Iterations)
	}
	if w := m.Tracker.Weight; w.Source != "label" || w.Pattern != "weight::{value}" {
		t.Errorf("weight = %+v: Free has no weight field, the weight::N labels carry it", w)
	}
	if got := m.Labels; got.Type.Pattern != "type::{value}" || got.App.Pattern != "app::{value}" || got.Workflow.Pattern != "workflow::{value}" {
		t.Errorf("labels = %+v", got)
	}
	wantStage := map[string]string{"started": "in progress", "draft": "in progress", "review": "in review", "merged": "done"}
	if !maps.Equal(m.Labels.Workflow.Stage, wantStage) || m.Labels.Workflow.Done != "done" {
		t.Errorf("workflow = %+v", m.Labels.Workflow)
	}
	if len(m.Repos) != 2 || m.Repos["api"].Path != "ade-demo-env/api" || m.Repos["web"].Path != "ade-demo-env/web" {
		t.Fatalf("repos = %+v: want api and web (the tracker and the archived repo are not code repos)", m.Repos)
	}
	if !slices.Equal(m.Repos["api"].App, []string{"api"}) || !slices.Equal(m.Repos["web"].App, []string{"web"}) {
		t.Errorf("apps = %v %v", m.Repos["api"].App, m.Repos["web"].App)
	}
	if got := m.Repos["api"].Tasks["dev"].Cmd; got != "go run ." {
		t.Errorf("api dev = %q", got)
	}
	if got := m.Repos["web"].Tasks["dev"].Cmd; got != "npm run dev" {
		t.Errorf("web dev = %q", got)
	}
	if !slices.Equal(m.Groups["dev"], []string{"api.dev", "web.dev"}) || m.Workspaces["default"].Run != "dev" {
		t.Errorf("groups = %v workspaces = %v", m.Groups, m.Workspaces)
	}
	if m.Conventions.ChildIssue != "auto" {
		t.Errorf("child_issue = %q", m.Conventions.ChildIssue)
	}
	if m.Name != "ade-demo-env" || m.Forge != userForge {
		t.Errorf("name %q forge %q", m.Name, m.Forge)
	}

	for _, want := range []string{
		"ade-demo-env/old-service (archived)",
	} {
		if !slices.Contains(d.Skipped, want) {
			t.Errorf("skipped = %v, want %q", d.Skipped, want)
		}
	}
	for _, want := range []string{"MR templates found in: api.", "No MR template in: web.", "named like a tracker; 10 of its recent issues carry app labels"} {
		if !strings.Contains(d.TOML, want) {
			t.Errorf("manifest comments lack %q:\n%s", want, d.TOML)
		}
	}
}

func TestDiscoverGitLabPremium(t *testing.T) {
	c, srv := gitlabFake(t, fake.Premium)
	seedRoots(srv)

	d, err := bootstrap.Discover(context.Background(), c, opts(func(o *bootstrap.Options) { o.Name = "demo" }))
	if err != nil {
		t.Fatal(err)
	}
	m := roundTrip(t, d)
	if it := m.Tracker.Iterations; it.Source != "gitlab-iterations" || it.Cadence != "Delivery" {
		t.Errorf("iterations = %+v: want the Delivery cadence, which has the most iterations of the two", it)
	}
	if m.Tracker.Weight.Source != "gitlab" || m.Tracker.Weight.Pattern != "" {
		t.Errorf("weight = %+v: Premium issues carry the weight field", m.Tracker.Weight)
	}
	if m.Name != "demo" {
		t.Errorf("name = %q, want the --name", m.Name)
	}
	for _, want := range []string{`cadence "Delivery": 2-week sprints from 2026-10-19`, `"Maintenance"`} {
		if !strings.Contains(d.TOML, want) {
			t.Errorf("comments lack %q:\n%s", want, d.TOML)
		}
	}
}

type answers map[string]string

func (a answers) Choose(question string, options []string, def string) (string, error) {
	for key, pick := range a {
		if strings.Contains(question, key) {
			for _, o := range options {
				if strings.HasPrefix(o, pick) {
					return o, nil
				}
			}
			return "", errors.New("no option " + pick)
		}
	}
	return def, nil
}

func TestDiscoverAsksWhichCadence(t *testing.T) {
	c, _ := gitlabFake(t, fake.Premium)
	d, err := bootstrap.Discover(context.Background(), c, opts(func(o *bootstrap.Options) {
		o.Chooser = answers{"cadence": "Maintenance"}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Manifest.Tracker.Iterations.Cadence; got != "Maintenance" {
		t.Errorf("cadence = %q, want the answer", got)
	}
}

func TestDiscoverTrackerFlagAndErrors(t *testing.T) {
	c, _ := gitlabFake(t, fake.Free)
	d, err := bootstrap.Discover(context.Background(), c, opts(func(o *bootstrap.Options) { o.Tracker = "web" }))
	if err != nil {
		t.Fatal(err)
	}
	m := roundTrip(t, d)
	if m.Tracker.Project != "ade-demo-env/web" || len(m.Repos) != 2 || m.Repos["tracker"].Path != "ade-demo-env/tracker" {
		t.Errorf("--tracker web: tracker %q repos %v", m.Tracker.Project, m.Repos)
	}
	if !strings.Contains(d.TOML, "given with --tracker") {
		t.Errorf("the reason should say the tracker was given:\n%s", d.TOML)
	}

	if _, err := bootstrap.Discover(context.Background(), c, opts(func(o *bootstrap.Options) { o.Tracker = "nope" })); err == nil ||
		!strings.Contains(err.Error(), "not a repository of the group") {
		t.Errorf("unknown tracker: %v", err)
	}
	if _, err := bootstrap.Discover(context.Background(), c, opts(func(o *bootstrap.Options) { o.Group = "no-such-group" })); err == nil {
		t.Error("an unknown group was accepted")
	}
}

func TestDiscoverAmbiguousTracker(t *testing.T) {
	c, srv := gitlabFake(t, fake.Free)
	// Two repos with nothing to tell them apart: no app labels, no tracker-like names.
	srv.AddProject("alpha")
	srv.AddProject("beta")
	sel := func(o *bootstrap.Options) { o.Match = "[ab][le]*" }

	_, err := bootstrap.Discover(context.Background(), c, opts(sel))
	if err == nil || !strings.Contains(err.Error(), "pass --tracker") || !strings.Contains(err.Error(), "ade-demo-env/alpha") {
		t.Fatalf("unattended ambiguity: %v, want an error naming --tracker and the candidates", err)
	}

	d, err := bootstrap.Discover(context.Background(), c, opts(sel, func(o *bootstrap.Options) {
		o.Chooser = answers{"tracker": "ade-demo-env/beta"}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if d.Manifest.Tracker.Project != "ade-demo-env/beta" || len(d.Manifest.Repos) != 1 {
		t.Errorf("answered beta: tracker %q repos %v", d.Manifest.Tracker.Project, d.Manifest.Repos)
	}
}

func TestDiscoverMatchRestrictsRepos(t *testing.T) {
	c, _ := gitlabFake(t, fake.Free)
	d, err := bootstrap.Discover(context.Background(), c, opts(func(o *bootstrap.Options) { o.Match = "*a*" }))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Manifest.Repos["web"]; ok {
		t.Errorf("web does not match *a*: %v", d.Manifest.Repos)
	}
	if !slices.Contains(d.Skipped, "1 repository not matching --match *a*") {
		t.Errorf("skipped = %v", d.Skipped)
	}
	if _, err := bootstrap.Discover(context.Background(), c, opts(func(o *bootstrap.Options) { o.Match = "zzz*" })); err == nil {
		t.Error("a match that keeps nothing must be an error")
	}
	if _, err := bootstrap.Discover(context.Background(), c, opts(func(o *bootstrap.Options) { o.Match = "[" })); err == nil {
		t.Error("a bad glob must be an error")
	}
}

func TestDiscoverIsReadOnly(t *testing.T) {
	c, srv := gitlabFake(t, fake.Premium)
	if _, err := bootstrap.Discover(context.Background(), c, opts()); err != nil {
		t.Fatal(err)
	}
	for _, r := range srv.Requests() {
		if r.Method != "GET" && (r.Method != "POST" || !strings.HasSuffix(r.Path, "/graphql")) {
			t.Errorf("discovery sent %s %s", r.Method, r.Path)
		}
	}
}

func TestRenderedManifestSurvivesHostileNames(t *testing.T) {
	c, srv := gitlabFake(t, fake.Free)
	// A project name and file content with quotes and backslashes must not break the TOML.
	srv.AddFile("ade-demo-env/api", "Procfile", "web: echo \"hi\\there\" $PORT\n")
	d, err := bootstrap.Discover(context.Background(), c, opts(func(o *bootstrap.Options) { o.Name = "a.b_c-1" }))
	if err != nil {
		t.Fatal(err)
	}
	m := roundTrip(t, d)
	if got := m.Repos["api"].Tasks["web"].Cmd; got != `echo "hi\there" $PORT` {
		t.Errorf("procfile command = %q", got)
	}
}

func TestCheckRejectsUnknownForgeAndBadName(t *testing.T) {
	c, _ := gitlabFake(t, fake.Free)
	d, err := bootstrap.Discover(context.Background(), c, opts(func(o *bootstrap.Options) { o.Name = "bad name" }))
	if err != nil {
		t.Fatal(err)
	}
	issues, err := d.Check(userConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !config.HasErrors(issues) {
		t.Errorf("a name with a space passed validation: %v", issues)
	}
	d, _ = bootstrap.Discover(context.Background(), c, opts(func(o *bootstrap.Options) { o.ForgeName = "elsewhere" }))
	if issues, _ := d.Check(userConfig()); !config.HasErrors(issues) {
		t.Errorf("a forge missing from the user config passed validation: %v", issues)
	}
}

func TestDiscoverGitHub(t *testing.T) {
	srv := ghfake.New()
	t.Cleanup(srv.Close)
	srv.AddRepo("scratch")
	srv.AddRepo("retired")
	srv.ArchiveRepo("retired")
	srv.AddFile("api", "go.mod", "module x\n")
	srv.AddFile("api", "main.go", "package main\n")
	c := github.New(srv.URL, ghfake.Token)

	d, err := bootstrap.Discover(context.Background(), c, bootstrap.Options{
		ForgeName: userForge, Group: "ade-demo", Match: "[atw]*", Now: func() time.Time { return fakeNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := userConfig()
	cfg.Forges[userForge] = config.Forge{Kind: "github"}
	issues, err := d.Check(cfg)
	if err != nil || config.HasErrors(issues) {
		t.Fatalf("check: %v %v\n%s", issues, err, d.TOML)
	}
	m, _ := config.ParseManifest([]byte(d.TOML), "x")

	if m.Tracker.Project != "ade-demo/tracker" || m.Tracker.Iterations.Source != "milestones" {
		t.Errorf("tracker %q iterations %+v", m.Tracker.Project, m.Tracker.Iterations)
	}
	if w := m.Tracker.Weight; w.Source != "label" || w.Pattern != "weight:{value}" {
		t.Errorf("weight = %+v", w)
	}
	if l := m.Labels; l.Type.Pattern != "type:{value}" || l.App.Pattern != "app:{value}" || l.Workflow.Pattern != "workflow:{value}" || l.Workflow.Done != "done" {
		t.Errorf("labels = %+v", l)
	}
	if len(m.Repos) != 2 || !slices.Equal(m.Repos["api"].App, []string{"api"}) || !slices.Equal(m.Repos["web"].App, []string{"web"}) {
		t.Errorf("repos = %+v: want api and web only (scratch does not match, retired is archived)", m.Repos)
	}
	if m.Repos["api"].Tasks["dev"].Cmd != "go run ." {
		t.Errorf("api tasks = %v", m.Repos["api"].Tasks)
	}
	if !slices.Contains(d.Skipped, "ade-demo/retired (archived)") || !slices.Contains(d.Skipped, "1 repository not matching --match [atw]*") {
		t.Errorf("skipped = %v", d.Skipped)
	}
	if !strings.Contains(d.TOML, `MR templates found in: api, web.`) || !strings.Contains(d.TOML, `mr = { template = "Default" }`) {
		t.Errorf("the GitHub pull request template is not noted:\n%s", d.TOML)
	}
}

func TestPublishIsIdempotentAndAudited(t *testing.T) {
	c, srv := gitlabFake(t, fake.Free)
	const tracker = "ade-demo-env/tracker"
	content := []byte("name = \"x\"\n")
	var audited []string
	po := bootstrap.PublishOptions{Audit: func(action, target string, _ map[string]any, _ string, err error) {
		if err != nil {
			t.Errorf("audit %s: %v", action, err)
		}
		audited = append(audited, action)
	}}

	// A dry run writes nothing and says what it would do.
	dry := po
	dry.DryRun = true
	p, err := bootstrap.Publish(context.Background(), c, tracker, content, "git+x#.ade.toml", dry)
	if err != nil {
		t.Fatal(err)
	}
	if !p.DryRun || len(p.Steps) != 3 || !strings.Contains(p.Steps[0], "create branch "+bootstrap.PublishBranch) || p.MR != nil {
		t.Errorf("dry run = %+v", p)
	}
	if !strings.Contains(p.Body, "ade init --project 'git+x#.ade.toml'") {
		t.Errorf("the MR body should say how teammates register it:\n%s", p.Body)
	}
	for _, r := range srv.Requests() {
		if r.Method != "GET" {
			t.Errorf("dry run sent %s %s", r.Method, r.Path)
		}
	}
	if len(audited) != 0 {
		t.Errorf("dry run audited %v", audited)
	}

	// The real run: branch, one commit, one MR.
	p, err = bootstrap.Publish(context.Background(), c, tracker, content, "git+x#.ade.toml", po)
	if err != nil {
		t.Fatal(err)
	}
	if p.MR == nil || p.MR.State != "opened" || p.MR.SourceBranch != bootstrap.PublishBranch || p.MR.TargetBranch != "main" {
		t.Fatalf("mr = %+v", p.MR)
	}
	if !slices.Equal(audited, []string{"forge.branch.create", "forge.file.commit", "forge.mr.create"}) {
		t.Errorf("audited = %v", audited)
	}
	if got, ok := srv.FileAt(tracker, bootstrap.PublishBranch, ".ade.toml"); !ok || got != string(content) {
		t.Errorf("file on the branch = %q %v", got, ok)
	}
	if _, ok := srv.FileAt(tracker, "main", ".ade.toml"); ok {
		t.Error("the manifest must reach main only by merge, not by this command")
	}

	// Again, same content: the same MR, no second branch, commit or MR.
	audited = nil
	again, err := bootstrap.Publish(context.Background(), c, tracker, content, "git+x#.ade.toml", po)
	if err != nil {
		t.Fatal(err)
	}
	if again.MR == nil || again.MR.Ref != p.MR.Ref || len(audited) != 0 || len(srv.FileCommits()) != 1 {
		t.Errorf("re-run = %+v audited %v commits %d", again.MR, audited, len(srv.FileCommits()))
	}

	// Edited content: the same MR gets a second commit (an update).
	edited := []byte("name = \"y\"\n")
	third, err := bootstrap.Publish(context.Background(), c, tracker, edited, "git+x#.ade.toml", po)
	if err != nil {
		t.Fatal(err)
	}
	cs := srv.FileCommits()
	if third.MR == nil || third.MR.Ref != p.MR.Ref || len(cs) != 2 || !cs[1].Update || cs[1].Content != string(edited) {
		t.Errorf("edited re-run: mr %+v commits %+v", third.MR, cs)
	}
	if !slices.Equal(audited, []string{"forge.file.commit"}) {
		t.Errorf("audited = %v: only the commit is a write", audited)
	}
}

func TestPublishRefusesToReplaceWithoutForce(t *testing.T) {
	c, srv := gitlabFake(t, fake.Free)
	const tracker = "ade-demo-env/tracker"
	srv.AddFile(tracker, ".ade.toml", "name = \"old\"\n")

	_, err := bootstrap.Publish(context.Background(), c, tracker, []byte("name = \"new\"\n"), "s", bootstrap.PublishOptions{})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a refusal naming --force", err)
	}
	if len(srv.FileCommits()) != 0 {
		t.Error("a refused publish committed")
	}

	same, err := bootstrap.Publish(context.Background(), c, tracker, []byte("name = \"old\"\n"), "s", bootstrap.PublishOptions{})
	if err != nil || !same.Nothing {
		t.Errorf("identical manifest on main: %+v %v, want nothing to publish", same, err)
	}

	forced, err := bootstrap.Publish(context.Background(), c, tracker, []byte("name = \"new\"\n"), "s", bootstrap.PublishOptions{Force: true})
	if err != nil || forced.MR == nil {
		t.Fatalf("forced: %+v %v", forced, err)
	}
	if cs := srv.FileCommits(); len(cs) != 1 || !cs[0].Update {
		// the fake shares main's files with every branch, so the file exists and is updated
		t.Errorf("commits = %+v", cs)
	}
}

func TestPublishOnGitHub(t *testing.T) {
	srv := ghfake.New()
	t.Cleanup(srv.Close)
	c := github.New(srv.URL, ghfake.Token)
	const tracker = "ade-demo/tracker"

	p, err := bootstrap.Publish(context.Background(), c, tracker, []byte("name = \"x\"\n"), "s", bootstrap.PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p.MR == nil || p.MR.SourceBranch != bootstrap.PublishBranch {
		t.Fatalf("mr = %+v", p.MR)
	}
	if got, ok := srv.FileAt("tracker", bootstrap.PublishBranch, ".ade.toml"); !ok || got != "name = \"x\"\n" {
		t.Errorf("file = %q %v", got, ok)
	}
	if _, ok := srv.FileAt("tracker", "main", ".ade.toml"); ok {
		t.Error("main must not carry the manifest before the PR merges")
	}
	again, err := bootstrap.Publish(context.Background(), c, tracker, []byte("name = \"y\"\n"), "s", bootstrap.PublishOptions{})
	if err != nil || again.MR == nil || again.MR.Ref != p.MR.Ref {
		t.Errorf("second publish: %+v %v, want the same PR updated", again.MR, err)
	}
	if got, _ := srv.FileAt("tracker", bootstrap.PublishBranch, ".ade.toml"); got != "name = \"y\"\n" {
		t.Errorf("file after the update = %q", got)
	}
	var _ forge.Forge = c
}
