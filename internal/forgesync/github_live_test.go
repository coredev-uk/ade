//go:build live

package forgesync

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
)

// A sync of the real GitHub sandbox (scripts/sandbox/github-seed.sh) yields the
// court and stage shapes of the GitLab sandbox. Needs `gh auth token`:
//
//	go test -tags live -run LiveSyncGitHub -v ./internal/forgesync
func TestLiveSyncGitHub(t *testing.T) {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		t.Skip("`gh auth token` failed")
	}
	tok := strings.TrimSpace(string(out))
	manifest := strings.NewReplacer(
		"ade-demo/", "coredev-uk/ade-sandbox-",
	).Replace(githubManifest)
	h := newHarness(t, manifestFile{"demo-gh", manifest})
	h.app.Config.Forges = map[string]config.Forge{"f": {Kind: "github"}}
	h.svc.Open = func(context.Context, string, config.Forge) (forge.Forge, error) {
		return github.New("", tok), nil
	}

	res := h.sync()
	if len(res) != 1 || res[0].Err != "" {
		t.Fatalf("results = %+v", res)
	}
	r := res[0]
	if cur := r.Window.Current; cur == nil || cur.Title != "Sprint 42" || cur.Start.String() != "2026-11-02" || cur.Due.String() != "2026-11-13" {
		t.Errorf("window = %+v", r.Window)
	}
	by := map[string]domain.WorkItem{}
	for _, it := range r.Items {
		by[it.Issue.Title] = it
	}
	rate := by["Rate-limit the export endpoint"]
	if rate.Stage != domain.StageReview || rate.Court != domain.CourtYou || rate.Reason != "2 unresolved threads" || len(rate.Children) != 1 || len(rate.MRs) != 1 {
		t.Errorf("rate-limit = stage %v court %v %q children %d mrs %d", rate.Stage, rate.Court, rate.Reason, len(rate.Children), len(rate.MRs))
	}
	if a := by["Paginate the audit log view"]; a.Stage != domain.StageDraft || !a.Drift {
		t.Errorf("audit = stage %v drift %v, want a draft PR with label drift", a.Stage, a.Drift)
	}
	if b := by["Export timestamps ignore tenant timezone"]; b.Court != domain.CourtBlocked {
		t.Errorf("timezone court = %v, want blocked", b.Court)
	}
	if n := by["Exports page sometimes shows stale totals"]; len(n.Triage) != 3 {
		t.Errorf("stale totals triage = %v, want type, app, weight", n.Triage)
	}
	if d := by["Upgrade export worker dependencies"]; d.Stage != domain.StageDone {
		t.Errorf("deps stage = %v, want done", d.Stage)
	}
}
