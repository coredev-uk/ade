package forgesync

import (
	"context"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
	"github.com/coredev-uk/ade/internal/forge/github/fake"
)

const githubManifest = `
name = "demo-gh"
forge = "f"

[tracker]
project = "ade-demo/tracker"
iterations = { source = "milestones" }
weight = { source = "label", pattern = "weight:{value}" }

[labels.type]
pattern = "type:{value}"

[labels.app]
pattern = "app:{value}"

[labels.workflow]
pattern = "workflow:{value}"
stage = { started = "in progress", draft = "in progress", review = "in review", merged = "done" }
sync = "auto"
done = "done"

[repos.api]
path = "ade-demo/api"
app = ["api"]

[repos.web]
path = "ade-demo/web"
app = ["web"]
requires = ["api"]
`

// A sync of the GitHub fake (the same shapes as the GitHub sandbox) assembles
// the work items the GitLab sandbox yields: sprints from milestones read off
// the tracker repository, children through sub-issues, PRs through "Closes".
func TestSyncAgainstGitHub(t *testing.T) {
	srv := fake.New()
	t.Cleanup(srv.Close)
	h := newHarness(t, manifestFile{"demo-gh", githubManifest})
	h.app.Config.Forges = map[string]config.Forge{"f": {Kind: "github", URL: srv.URL}}
	h.svc.Open = func(context.Context, string, config.Forge) (forge.Forge, error) {
		return github.New(srv.URL, fake.Token), nil
	}

	res := h.sync()
	if len(res) != 1 || res[0].Err != "" {
		t.Fatalf("results = %+v", res)
	}
	r := res[0]
	if r.Me.Username != "demo-user" || r.Caps.Iterations || r.Caps.Weight {
		t.Errorf("me = %+v caps = %+v", r.Me, r.Caps)
	}
	cur := r.Window.Current
	if cur == nil || cur.Title != "Sprint 42" || cur.Start.String() != "2026-11-02" || cur.Due.String() != "2026-11-13" {
		t.Errorf("window = %+v: Sprint 42 must come from the tracker repo's milestones", r.Window)
	}

	by := map[int]domain.WorkItem{}
	for _, it := range r.Items {
		by[it.Ref.IID] = it
	}
	rate := by[1]
	if len(rate.Children) != 1 || rate.Children[0].Ref != (forge.Ref{Project: "ade-demo/api", IID: 1, Kind: forge.KindIssue}) {
		t.Errorf("tracker#1 children = %+v: the sub-issue is the child", rate.Children)
	}
	if len(rate.MRs) != 1 || rate.MRs[0].Ref.IID != 2 || rate.Stage != domain.StageReview || rate.Court != domain.CourtYou || rate.Reason != "2 unresolved threads" {
		t.Errorf("tracker#1 = stage %v court %v %q mrs %+v", rate.Stage, rate.Court, rate.Reason, rate.MRs)
	}
	if rate.Type != "story" || len(rate.Apps) != 1 || rate.Apps[0] != "api" || rate.Weight == nil || *rate.Weight != 5 {
		t.Errorf("tracker#1 facets = %q %v weight %v", rate.Type, rate.Apps, rate.Weight)
	}
	if audit := by[2]; audit.Stage != domain.StageDraft || !audit.Drift || audit.Workflow != "in review" || audit.WantWorkflow != "in progress" {
		t.Errorf("tracker#2 = stage %v drift %v %q→%q", audit.Stage, audit.Drift, audit.Workflow, audit.WantWorkflow)
	}
	if by[5].Court != domain.CourtBlocked {
		t.Errorf("tracker#5 court = %v, want blocked", by[5].Court)
	}
	if by[6].Weight != nil {
		t.Errorf("tracker#6 weight = %v, want unweighted", by[6].Weight)
	}
	if tr := by[11].Triage; len(tr) != 3 {
		t.Errorf("tracker#11 triage = %v", tr)
	}
	if by[10].Stage != domain.StageDone {
		t.Errorf("tracker#10 stage = %v", by[10].Stage)
	}

	// api!5 by a teammate asks me for a review.
	if len(r.Reviews) != 1 || r.Reviews[0].Ref.IID != 5 || r.Reviews[0].Ref.Project != "ade-demo/api" {
		t.Errorf("reviews = %+v", r.Reviews)
	}
}
