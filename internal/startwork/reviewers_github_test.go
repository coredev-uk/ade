package startwork_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
	githubfake "github.com/coredev-uk/ade/internal/forge/github/fake"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/startwork/swtest"
)

const (
	ghTracker = "ade-demo/tracker"
	ghAPI     = "ade-demo/api"
)

// ghManifest asks reviewerGroup to review the api repo's PRs.
func ghManifest(reviewerGroup string) string {
	return `
name  = "demo-gh"
forge = "sandbox-github"

[tracker]
project = "` + ghTracker + `"

[labels.type]
pattern = "type:{value}"
[labels.app]
pattern = "app:{value}"

[repos.api]
path = "` + ghAPI + `"
app  = ["api"]

[conventions]
branch = "{child_iid}-{slug}"
mr     = { draft = true, closes = "child", mention = "parent", reviewer_group = "` + reviewerGroup + `" }
`
}

// githubForge adds the git remote side to the GitHub client's CreateBranch. It
// embeds the interface, not swtest.Mirror, so it offers no ReviewerGroupReader
// (GitHub requests a team by name after the PR exists).
type githubForge struct {
	startwork.Forge
	remote *swtest.Remote
}

func (g githubForge) CreateBranch(ctx context.Context, project, name, from string) (forge.Branch, error) {
	b, err := g.Forge.CreateBranch(ctx, project, name, from)
	if err == nil {
		g.remote.Branch(name)
	}
	return b, err
}

type githubHarness struct {
	t     *testing.T
	srv   *githubfake.Server
	env   *startwork.Env
	app   *app.App
	m     *config.Manifest
	forge githubForge
}

func newGitHubHarness(t *testing.T, reviewerGroup string) *githubHarness {
	t.Helper()
	swtest.GitEnv(t)
	dir := t.TempDir()
	srv := githubfake.New()
	t.Cleanup(srv.Close)
	m, err := config.ParseManifest([]byte(ghManifest(reviewerGroup)), "test")
	if err != nil {
		t.Fatal(err)
	}
	al, err := audit.Open(filepath.Join(dir, "records"), "user")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	a := &app.App{Config: &cfg, Root: filepath.Join(dir, "root"), Audit: al, Bus: events.NewBus()}
	remote := swtest.NewRemote(t, dir, "api")
	swtest.Clone(t, a.Root, "demo-gh", "api", remote)
	f := githubForge{Forge: github.New(srv.URL, githubfake.Token), remote: remote}
	return &githubHarness{t: t, srv: srv, env: &startwork.Env{App: a, Forge: f}, app: a, m: m, forge: f}
}

func (h *githubHarness) start(iid int) (startwork.Plan, startwork.Result, error) {
	h.t.Helper()
	r, err := startwork.ResolveWith(context.Background(), h.forge, h.m, forge.Ref{Project: ghTracker, IID: iid, Kind: forge.KindIssue})
	if err != nil {
		h.t.Fatal(err)
	}
	return h.env.Start(context.Background(), r, draftApi(), false)
}

func (h *githubHarness) createAudit() audit.Entry {
	h.t.Helper()
	es, err := h.app.Audit.Query(audit.Filter{Action: "forge.mr.create"})
	if err != nil || len(es) != 1 {
		h.t.Fatalf("forge.mr.create entries: %v %+v", err, es)
	}
	return es[0]
}

func TestGitHubStartRequestsTheTeamOfTheReposOrganisation(t *testing.T) {
	h := newGitHubHarness(t, "ade-demo/reviewers")
	h.srv.AddTeam("reviewers")
	plan, res, err := h.start(3)
	if err != nil {
		t.Fatalf("start: %v\n%+v", err, res.Steps)
	}
	if len(plan.Warnings) != 0 || len(res.Notes) != 0 {
		t.Errorf("warnings %v, notes %v", plan.Warnings, res.Notes)
	}
	if text := mrStepText(t, plan); !strings.Contains(text, "request review from team ade-demo/reviewers") {
		t.Errorf("MR step = %q", text)
	}
	if teams, _ := h.srv.RequestedTeams(*target(t, res, "api").MR); !slices.Equal(teams, []string{"reviewers"}) {
		t.Errorf("requested teams = %v", teams)
	}
	if e := h.createAudit(); e.Params["reviewer_group"] != "ade-demo/reviewers" || e.Params["reviewers_error"] != nil {
		t.Errorf("forge.mr.create params = %v", e.Params)
	}
}

func TestGitHubTeamOfAnotherOrganisationWarnsAndTheMRIsStillCreated(t *testing.T) {
	h := newGitHubHarness(t, "other-org/reviewers")
	h.srv.AddTeam("reviewers")
	plan, res, err := h.start(3)
	if err != nil {
		t.Fatalf("start: %v\n%+v", err, res.Steps)
	}
	if !slices.ContainsFunc(plan.Warnings, func(w string) bool {
		return strings.Contains(w, "other-org/reviewers") && strings.Contains(w, "without reviewers")
	}) {
		t.Errorf("warnings = %v, want one about other-org/reviewers", plan.Warnings)
	}
	mr := target(t, res, "api").MR
	if mr == nil {
		t.Fatalf("no PR was created: %+v", res.Steps)
	}
	if teams, _ := h.srv.RequestedTeams(*mr); len(teams) != 0 {
		t.Errorf("requested teams = %v, want none", teams)
	}
	if e := h.createAudit(); e.Params["reviewer_group"] != nil {
		t.Errorf("no request was made, yet params = %v", e.Params)
	}
}

// GitHub refuses a team that does not exist after the PR is open: the PR
// stays, the result carries a note and the audit entry says why.
func TestGitHubUnknownTeamLeavesTheMRAndAnAuditedNote(t *testing.T) {
	h := newGitHubHarness(t, "ade-demo/no-such-team")
	plan, res, err := h.start(3)
	if err != nil {
		t.Fatalf("start: %v\n%+v", err, res.Steps)
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("warnings = %v", plan.Warnings)
	}
	api := target(t, res, "api")
	if api.MR == nil {
		t.Fatalf("no PR was created: %+v", res.Steps)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "review not requested from ade-demo/no-such-team on "+api.MR.String()) {
		t.Errorf("notes = %v", res.Notes)
	}
	e := h.createAudit()
	if got, _ := e.Params["reviewers_error"].(string); !strings.Contains(got, "reviewers not requested") || e.Params["reviewer_group"] != "ade-demo/no-such-team" {
		t.Errorf("forge.mr.create params = %v", e.Params)
	}
}
