package startwork_test

import (
	"context"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/startwork/swtest"
)

// A single-repo project: the tracker project is also a code repo
// ([repos.mono] path = tracker). child_issue = auto must not open a child
// issue next to the tracker issue: the tracker issue is the work item and the
// merge request closes it.
func TestTrackerThatIsAlsoTheCodeRepoGetsNoChild(t *testing.T) {
	for _, tier := range tiers {
		t.Run(tier.String(), func(t *testing.T) {
			h := newHarness(t, tier)
			ctx := context.Background()
			h.m.Repos["mono"] = config.Repo{Path: tracker}
			r := swtest.NewRemote(t, h.dir, "mono")
			h.forge.Remotes[tracker] = r
			swtest.Clone(t, h.app.Root, "demo-gl", "mono", r)
			h.srv.AddFile(tracker, startwork.TemplateDir+"/Default.md", "## What\n")

			ch := startwork.Choices{Repos: []string{"mono"}, Mode: startwork.ModeDraft, Child: startwork.ChildAuto}
			plan, res, err := h.start(4, ch)
			if err != nil {
				t.Fatal(err)
			}
			if tp := plan.Targets[0]; tp.UseChild {
				t.Fatalf("plan uses a child issue in the tracker project: %+v", tp)
			}
			for _, s := range plan.Steps {
				if s.Kind == startwork.StepChild || s.Kind == startwork.StepLink {
					t.Errorf("planned %s for a tracker that is the code repo", s.Kind)
				}
			}
			mono := target(t, res, "mono")
			if mono.Child != nil || mono.MR == nil || mono.Branch != "4-remove-the-legacy-csv-export-path" {
				t.Fatalf("target = %+v", mono)
			}
			mr, err := h.forge.MR(ctx, *mono.MR)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(mr.Description, "Closes "+tracker+"#4") && !strings.Contains(mr.Description, "Closes #4") {
				t.Errorf("MR does not close the tracker issue:\n%s", mr.Description)
			}
			if strings.Contains(mr.Description, "Part of") {
				t.Errorf("redundant parent mention:\n%s", mr.Description)
			}
			for _, a := range h.auditActions() {
				if a == "forge.issue.create" || a == "forge.issue.link" {
					t.Errorf("wrote %s: no child issue is wanted", a)
				}
			}
			before := h.writes()
			if _, res2, err := h.start(4, ch); err != nil || res2.Created() != 0 || h.writes() != before {
				t.Errorf("re-run created %d, %v", res2.Created(), err)
			}
		})
	}
}

func TestDefaultChoicesPicksTheOnlyRepo(t *testing.T) {
	h := newHarness(t, tiers[0])
	item := h.resolve(4).Item
	item.Repos = nil
	if got := startwork.DefaultChoices(h.m, item).Repos; len(got) != 0 {
		t.Errorf("two repos, no routing: repos = %v, want none", got)
	}
	one := *h.m
	one.Repos = map[string]config.Repo{"mono": {Path: tracker}}
	if got := startwork.DefaultChoices(&one, item).Repos; len(got) != 1 || got[0] != "mono" {
		t.Errorf("one repo: repos = %v, want [mono]", got)
	}
	item.Repos = []string{"mono"}
	if got := startwork.DefaultChoices(&one, item).Repos; len(got) != 1 {
		t.Errorf("routed repo kept: %v", got)
	}
}
