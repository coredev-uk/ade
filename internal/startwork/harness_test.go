package startwork_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/startwork/swtest"
)

const (
	group   = fake.GroupPath
	tracker = group + "/tracker"
	apiProj = group + "/api"
	webProj = group + "/web"
)

// manifestTOML mirrors scripts/sandbox/manifests/gitlab.ade.toml.
const manifestTOML = `
name  = "demo-gl"
forge = "sandbox-gitlab"

[tracker]
project    = "` + tracker + `"
iterations = { source = "milestones" }
weight     = { source = "label", pattern = "weight::{value}" }

[labels.type]
pattern = "type::{value}"
[labels.app]
pattern = "app::{value}"
[labels.workflow]
pattern = "workflow::{value}"
stage   = { started = "in progress", draft = "in progress", review = "in review", merged = "done" }
sync    = "auto"
done    = "done"

[repos.api]
path = "` + apiProj + `"
app  = ["api"]
[repos.api.hooks]
post_worktree = ["touch .hook-ran"]

[repos.web]
path     = "` + webProj + `"
app      = ["web"]
requires = ["api"]

[conventions]
branch = "{child_iid}-{slug}"
link   = ["relates_to", "description"]
mr     = { draft = true, closes = "child", mention = "parent" }
commit_prefix = "GL-{parent}"
`

const defaultTemplate = "## What\n\n## Testing\n\n/label ~\"type::story\"\n/assign_reviewer @reviewer-rae\n"

type harness struct {
	t      *testing.T
	srv    *fake.Server
	forge  *swtest.Mirror
	env    *startwork.Env
	app    *app.App
	m      *config.Manifest
	events <-chan events.Event
	dir    string
	remote map[string]*swtest.Remote // by repo name
}

func newHarness(t *testing.T, tier fake.Tier, opts ...fake.Option) *harness {
	t.Helper()
	swtest.GitEnv(t)
	dir := t.TempDir()
	srv := fake.New(tier, opts...)
	t.Cleanup(srv.Close)
	client := gitlab.New(srv.URL, fake.Token)

	m, err := config.ParseManifest([]byte(manifestTOML), "test")
	if err != nil {
		t.Fatal(err)
	}
	al, err := audit.Open(filepath.Join(dir, "records"), "user")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	a := &app.App{Config: &cfg, Root: filepath.Join(dir, "root"), Audit: al, Bus: events.NewBus(), Paths: paths.Paths{CacheDir: filepath.Join(dir, "cache")}}
	t.Cleanup(func() { a.Close() })
	ch, cancel := a.Bus.Subscribe(256)
	t.Cleanup(cancel)

	h := &harness{t: t, srv: srv, app: a, m: m, events: ch, dir: dir, remote: map[string]*swtest.Remote{}}
	h.forge = swtest.NewMirror(client)
	for name, proj := range map[string]string{"api": apiProj, "web": webProj} {
		r := swtest.NewRemote(t, dir, name)
		h.remote[name] = r
		h.forge.Remotes[proj] = r
		swtest.Clone(t, h.app.Root, "demo-gl", name, r)
	}
	srv.AddFile(apiProj, startwork.TemplateDir+"/Default.md", defaultTemplate)
	h.env = &startwork.Env{App: a, Forge: h.forge}
	return h
}

func (h *harness) resolve(iid int) *startwork.Resolved {
	h.t.Helper()
	r, err := startwork.ResolveWith(context.Background(), h.forge, h.m, issueRef(tracker, iid))
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) start(iid int, ch startwork.Choices) (startwork.Plan, startwork.Result, error) {
	h.t.Helper()
	return h.env.Start(context.Background(), h.resolve(iid), ch, false)
}

// writes counts non-GET requests the fake has served.
func (h *harness) writes() int {
	n := 0
	for _, r := range h.srv.Requests() {
		if r.Method != "GET" && r.Operation != "query" { // GraphQL reads are POSTs
			n++
		}
	}
	return n
}

func (h *harness) auditActions() []string {
	h.t.Helper()
	es, err := h.app.Audit.Query(audit.Filter{})
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Action)
	}
	return out
}

func (h *harness) drain() []events.Event {
	var out []events.Event
	for {
		select {
		case ev := <-h.events:
			out = append(out, ev)
		case <-time.After(10 * time.Millisecond):
			return out
		}
	}
}

func issueRef(project string, iid int) forge.Ref {
	return forge.Ref{Project: project, IID: iid, Kind: forge.KindIssue}
}

func target(t *testing.T, res startwork.Result, repo string) startwork.TargetResult {
	t.Helper()
	for _, tr := range res.Targets {
		if tr.Repo == repo {
			return tr
		}
	}
	t.Fatalf("no target %s in %+v", repo, res.Targets)
	return startwork.TargetResult{}
}

func countOutcome(res startwork.Result, outcome string) int {
	n := 0
	for _, s := range res.Steps {
		if s.Outcome == outcome {
			n++
		}
	}
	return n
}

var errBoom = errors.New("boom")

// mutations counts the GraphQL mutations the fake has served.
func (h *harness) mutations() int {
	n := 0
	for _, r := range h.srv.Requests() {
		if r.Operation == "mutation" {
			n++
		}
	}
	return n
}
