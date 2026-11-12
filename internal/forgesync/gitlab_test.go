package forgesync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

const sandboxManifest = `
name  = "demo-gl"
forge = "f"

[tracker]
project    = "ade-demo-env/tracker"
iterations = { source = "auto" }

[labels.type]
pattern = "type::{value}"

[labels.app]
pattern = "app::{value}"

[labels.workflow]
pattern = "workflow::{value}"
stage   = { started = "in progress", draft = "in progress", review = "in review", merged = "done" }

[repos.api]
path = "ade-demo-env/api"
app  = ["api"]

[repos.web]
path     = "ade-demo-env/web"
app      = ["web"]
requires = ["api"]
`

func syncFake(t *testing.T, tier fake.Tier, opts ...fake.Option) Result {
	t.Helper()
	srv := fake.New(tier, opts...)
	t.Cleanup(srv.Close)
	h := newHarness(t, manifestFile{"demo-gl", sandboxManifest})
	h.svc.Open = func(context.Context, string, config.Forge) (forge.Forge, error) {
		return gitlab.New(srv.URL, fake.Token), nil
	}
	h.svc.Now = func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }
	res := h.sync()
	if len(res) != 1 || res[0].Err != "" {
		t.Fatalf("results = %+v", res)
	}
	return res[0]
}

func TestSyncAgainstFakeGitLab(t *testing.T) {
	for _, tier := range []fake.Tier{fake.Free, fake.Premium} {
		t.Run(tier.String(), func(t *testing.T) {
			r := syncFake(t, tier)
			items := byRef(r)

			t1, ok := items["ade-demo-env/tracker#1"]
			if !ok {
				t.Fatalf("tracker#1 missing from %v", refs(r))
			}
			if t1.Stage != domain.StageReview || t1.Court != domain.CourtYou || t1.Reason != "2 unresolved threads" {
				t.Errorf("tracker#1 = %v / %v / %q", t1.Stage, t1.Court, t1.Reason)
			}
			if len(t1.MRs) != 1 || t1.MRs[0].Ref.String() != "ade-demo-env/api!1" || t1.MRs[0].Unresolved != 2 {
				t.Errorf("tracker#1 MRs = %+v", t1.MRs)
			}
			if t2 := items["ade-demo-env/tracker#2"]; t2.Stage != domain.StageDraft {
				t.Errorf("tracker#2 stage = %v", t2.Stage)
			}
			if len(r.Reviews) != 0 {
				t.Errorf("reviews = %+v; the seed asks nobody but the author", r.Reviews)
			}

			cur := r.Window.Current
			if cur == nil {
				t.Fatalf("no current sprint: %+v", r.Window)
			}
			weights := 0
			for _, wi := range r.Items {
				if wi.Weight != nil {
					weights++
				}
			}
			switch tier {
			case fake.Premium:
				if cur.Source != "gitlab-iterations" || !r.Caps.Iterations {
					t.Errorf("premium window = %+v caps = %+v", cur, r.Caps)
				}
				if w := t1.Weight; w == nil || *w != 3 || weights == 0 {
					t.Errorf("premium weights: tracker#1 = %v, %d weighted", t1.Weight, weights)
				}
			case fake.Free:
				if cur.Source != "milestones" || cur.Title != "Sprint 42" || r.Caps.Iterations {
					t.Errorf("free window = %+v caps = %+v", cur, r.Caps)
				}
				if weights != 0 {
					t.Errorf("free tier shows %d weights", weights)
				}
			}
		})
	}
}

// A tracker in a personal namespace has no group endpoints: sprints are the
// tracker project's milestones and weight comes from its licence.
func TestSyncUserNamespaceTracker(t *testing.T) {
	for _, tier := range []fake.Tier{fake.Free, fake.Premium} {
		t.Run(tier.String(), func(t *testing.T) {
			r := syncFake(t, tier, fake.WithUserNamespace())
			if !r.Caps.UserNamespace || r.Caps.Iterations {
				t.Errorf("caps = %+v, want a user namespace without iterations", r.Caps)
			}
			cur := r.Window.Current
			if cur == nil || cur.Source != "milestones" || cur.Title != "Sprint 42" {
				t.Fatalf("window = %+v, want the project milestone Sprint 42", r.Window)
			}
			if r.Caps.Weight != (tier == fake.Premium) {
				t.Errorf("weight caps = %v on %s", r.Caps.Weight, tier)
			}
		})
	}
}

func TestIterationsSourceNeedsAGroup(t *testing.T) {
	for _, src := range []string{srcIterations, "cadence"} {
		t.Run(src, func(t *testing.T) {
			p := &pipeline{m: &config.Manifest{Tracker: config.Tracker{
				Project: "me/tracker", Iterations: config.IterationsSource{Source: src},
			}}}
			_, err := p.loadSprints(context.Background(), forge.Caps{UserNamespace: true})
			if err == nil || !strings.Contains(err.Error(), "user namespace") {
				t.Errorf("err = %v, want a user namespace error", err)
			}
		})
	}
}
