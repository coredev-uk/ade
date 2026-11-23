package cli

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// stageEnv is notesEnv under labels.workflow.sync = "ask" (the default), with
// tracker#1 cached as an item whose MR went to review while its label still
// says "in progress".
func stageEnv(t *testing.T) *cliEnv {
	t.Helper()
	e := notesEnv(t)
	src := e.app.Config.Projects[0].Source
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `sync    = "auto"`) {
		t.Fatal("the sandbox manifest no longer says sync = \"auto\"")
	}
	if err := os.WriteFile(src, []byte(strings.Replace(string(b), `sync    = "auto"`, `sync    = "ask"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	tr := forge.Ref{Project: trackerProj, IID: 1, Kind: forge.KindIssue}
	api := gitlab.New(e.srv.URL, fake.Token)
	cur, err := api.Issue(context.Background(), tr)
	if err != nil {
		t.Fatal(err)
	}
	var drop []string
	for _, l := range cur.Labels {
		if strings.HasPrefix(l, "workflow::") {
			drop = append(drop, l)
		}
	}
	if err := api.SetLabels(context.Background(), tr, []string{"workflow::in progress"}, drop); err != nil {
		t.Fatal(err)
	}
	is, err := api.Issue(context.Background(), tr)
	if err != nil {
		t.Fatal(err)
	}
	e.cacheDemoGL(domain.WorkItem{
		Project: "demo-gl", Ref: tr, Issue: is, Stage: domain.StageReview,
		Workflow: "in progress", WantWorkflow: "in review", Drift: true,
	})
	return e
}

func TestIssueStageAppliesThePendingMoveAfterAPreview(t *testing.T) {
	e := stageEnv(t)
	tracker := trackerProj + "#1"

	// --dry-run shows the label change and writes nothing.
	out := e.mustIssue("stage", tracker, "--dry-run")
	want := "would write: " + tracker + ": workflow in progress → in review (add workflow::in review, remove workflow::in progress)"
	if !strings.Contains(out, want) {
		t.Fatalf("dry run:\n%s\nwant %q", out, want)
	}
	if got := labelsOf(t, e, trackerProj, 1); !slices.Contains(got, "workflow::in progress") || len(e.audited("forge.issue.labels")) != 0 {
		t.Fatalf("a dry run wrote: %v, audit %v", got, e.audited("forge.issue.labels"))
	}

	// A script without --yes is refused, and nothing is written.
	if _, err := e.issue("stage", tracker); err == nil || exitCode(err) != exitNeedsConfirm {
		t.Fatalf("no --yes: err %v, code %d", err, exitCode(err))
	}
	if got := labelsOf(t, e, trackerProj, 1); !slices.Contains(got, "workflow::in progress") {
		t.Fatalf("a refused move wrote: %v", got)
	}

	// --yes moves it, audited.
	out = e.mustIssue("stage", tracker, "--yes")
	if !strings.Contains(out, "done: "+tracker+": workflow in progress → in review") {
		t.Fatalf("output:\n%s", out)
	}
	got := labelsOf(t, e, trackerProj, 1)
	if !slices.Contains(got, "workflow::in review") || slices.Contains(got, "workflow::in progress") {
		t.Fatalf("labels after the move: %v", got)
	}
	es := e.audited("forge.issue.labels")
	if len(es) != 1 || es[0].Actor != "user" || es[0].Target != tracker || es[0].Params["reason"] != "workflow stage" || es[0].Outcome != "ok" {
		t.Fatalf("audit = %+v", es)
	}

	// The cache is still a sync behind; the live labels already say review, so
	// a second run moves nothing and says why.
	out = e.mustIssue("stage", tracker, "--yes")
	if !strings.Contains(out, "nothing to move: the workflow label already says in review") || len(e.audited("forge.issue.labels")) != 1 {
		t.Fatalf("rerun:\n%s", out)
	}
}

func TestIssueStageLeavesADeliberateBlockedLabel(t *testing.T) {
	e := stageEnv(t)
	tracker := trackerProj + "#1"
	tr := forge.Ref{Project: trackerProj, IID: 1, Kind: forge.KindIssue}
	if err := gitlab.New(e.srv.URL, fake.Token).SetLabels(context.Background(), tr, []string{"workflow::blocked"}, []string{"workflow::in progress"}); err != nil {
		t.Fatal(err)
	}
	out := e.mustIssue("stage", tracker, "--yes")
	if !strings.Contains(out, "nothing to move") || !strings.Contains(out, "blocked") || len(e.audited("forge.issue.labels")) != 0 {
		t.Fatalf("output:\n%s", out)
	}
}

func TestIssueStageJSONAndUncachedItems(t *testing.T) {
	e := stageEnv(t)
	asJSON(t)
	var res struct {
		Ref     string `json:"ref"`
		DryRun  bool   `json:"dry_run"`
		Applied bool   `json:"applied"`
		Move    struct {
			To     string   `json:"to"`
			Add    string   `json:"add"`
			Remove []string `json:"remove"`
		} `json:"move"`
	}
	if err := json.Unmarshal([]byte(e.mustIssue("stage", trackerProj+"#1", "--dry-run")), &res); err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Applied || res.Move.To != "in review" || res.Move.Add != "workflow::in review" || len(res.Move.Remove) != 1 {
		t.Fatalf("json = %+v", res)
	}
	if _, err := e.issue("stage", trackerProj+"#99", "--yes"); err == nil || !strings.Contains(err.Error(), "not in the cache") {
		t.Fatalf("an item the cache lacks: %v", err)
	}
}
