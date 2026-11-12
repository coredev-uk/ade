package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

func milestoneTitle(t *testing.T, e *cliEnv, project string, iid int) string {
	t.Helper()
	is, err := gitlab.New(e.srv.URL, fake.Token).Issue(context.Background(), forge.Ref{Project: project, IID: iid, Kind: forge.KindIssue})
	if err != nil {
		t.Fatal(err)
	}
	if is.Milestone == nil {
		return ""
	}
	return is.Milestone.Title
}

func TestIssueMoveCarriesTheIssueAndItsChildToTheNextSprint(t *testing.T) {
	e := notesEnv(t)
	old := issueMoveNow
	issueMoveNow = func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { issueMoveNow = old })
	tracker := trackerProj + "#1" // Sprint 42, child api#1

	out := e.mustIssue("move", tracker, "--dry-run")
	if !strings.Contains(out, "would write: "+tracker+": Sprint 42 → Sprint 43 (milestone)") || !strings.Contains(out, "(child)") {
		t.Fatalf("dry run:\n%s", out)
	}
	if got := milestoneTitle(t, e, trackerProj, 1); got != "Sprint 42" || len(e.audited("forge.issue.sprint")) != 0 {
		t.Fatalf("a dry run wrote: milestone %q, audit %v", got, e.audited("forge.issue.sprint"))
	}

	out = e.mustIssue("move", tracker)
	if !strings.Contains(out, "done: "+tracker+": Sprint 42 → Sprint 43") {
		t.Fatalf("output:\n%s", out)
	}
	for _, p := range []string{trackerProj, apiProject} {
		if got := milestoneTitle(t, e, p, 1); got != "Sprint 43" {
			t.Errorf("%s#1 milestone = %q, want Sprint 43", p, got)
		}
	}
	if es := e.audited("forge.issue.sprint"); len(es) != 2 || es[0].Params["to"] != "Sprint 43" || es[1].Params["child"] != true {
		t.Fatalf("audit = %+v", es)
	}

	// Past the last sprint there is nothing to move to, and the error says what to create.
	if _, err := e.issue("move", tracker); err == nil || !strings.Contains(err.Error(), "create the next milestone in group "+fake.GroupPath) {
		t.Fatalf("move past the last sprint: %v", err)
	}

	// --json, --to by title, --no-children.
	asJSON(t)
	var res struct {
		DryRun  bool `json:"dry_run"`
		To      struct{ Title string }
		Results []struct {
			Plan    struct{ Child bool } `json:"plan"`
			Applied bool                 `json:"applied"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(e.mustIssue("move", tracker, "--to", "sprint 42", "--no-children")), &res); err != nil {
		t.Fatal(err)
	}
	if res.To.Title != "Sprint 42" || len(res.Results) != 1 || !res.Results[0].Applied {
		t.Fatalf("json = %+v", res)
	}
	if got := milestoneTitle(t, e, trackerProj, 1); got != "Sprint 42" {
		t.Errorf("tracker milestone = %q", got)
	}
	if got := milestoneTitle(t, e, apiProject, 1); got != "Sprint 43" {
		t.Errorf("--no-children moved the child: %q", got)
	}
}

func TestIssueMoveRejectsContradictoryFlagsAndUnknownSprints(t *testing.T) {
	e := notesEnv(t)
	tracker := trackerProj + "#1"
	if _, err := e.issue("move", tracker, "--with-children", "--no-children"); err == nil {
		t.Error("contradictory flags were accepted")
	}
	if _, err := e.issue("move", tracker, "--to", "Sprint 99"); err == nil || !strings.Contains(err.Error(), `no sprint "Sprint 99"`) {
		t.Errorf("unknown sprint: %v", err)
	}
	if len(e.audited("forge.issue.sprint")) != 0 {
		t.Error("a refused move was audited as a write")
	}
}
