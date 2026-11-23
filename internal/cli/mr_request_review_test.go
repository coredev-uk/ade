package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestMRRequestReviewAgainWritesAndAudits(t *testing.T) {
	e := newCLIEnv(t)
	r := ref(rateMR)
	if err := e.srv.SetReviewState(r, "reviewer-rae", "requested_changes"); err != nil {
		t.Fatal(err)
	}
	out := e.mustMR("request-review", rateMR, "--again")
	for _, want := range []string{"asked for a review on", "@reviewer-rae", "changes requested: asked again"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if st, _ := e.srv.ReviewerState(r, "reviewer-rae"); st != "unreviewed" {
		t.Errorf("rae = %q, want unreviewed", st)
	}
	es := e.audited("forge.mr.request_review")
	if len(es) != 1 || es[0].DryRun || es[0].Outcome != "ok" || es[0].Item != rateMR || es[0].Params["again"] != true {
		t.Fatalf("audit = %+v", es)
	}
	if u, _ := es[0].Params["users"].([]any); len(u) != 1 || u[0] != "reviewer-rae" {
		t.Errorf("audited users = %v", es[0].Params["users"])
	}
}

func TestMRRequestReviewNamedUserKeepsReviewers(t *testing.T) {
	e := newCLIEnv(t)
	r := ref(rateMR)
	e.srv.AddUser("reviewer-sam", "Sam Second")
	asJSON(t)
	var v requestReviewView
	if err := json.Unmarshal([]byte(e.mustMR("request-review", rateMR, "@reviewer-sam")), &v); err != nil {
		t.Fatal(err)
	}
	if v.DryRun || len(v.Targets) != 1 || v.Targets[0].Username != "reviewer-sam" || v.Targets[0].Action != "add" {
		t.Fatalf("view = %+v", v)
	}
	if names, _ := e.srv.MRReviewers(r); !slices.Equal(names, []string{"reviewer-rae", "reviewer-sam"}) {
		t.Errorf("reviewers = %v", names)
	}
}

func TestMRRequestReviewDryRunWritesNothing(t *testing.T) {
	e := newCLIEnv(t)
	r := ref(rateMR)
	e.srv.AddUser("reviewer-sam", "Sam Second")
	if err := e.srv.SetReviewState(r, "reviewer-rae", "approved"); err != nil {
		t.Fatal(err)
	}
	out := e.mustMR("request-review", rateMR, "@reviewer-sam", "--again", "--dry-run")
	for _, want := range []string{"dry-run: would ask for a review on", "@reviewer-sam  not a reviewer yet: added", "@reviewer-rae  approved: asked again"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output lacks %q:\n%s", want, out)
		}
	}
	if names, _ := e.srv.MRReviewers(r); !slices.Equal(names, []string{"reviewer-rae"}) {
		t.Errorf("dry run changed the reviewers: %v", names)
	}
	if st, _ := e.srv.ReviewerState(r, "reviewer-rae"); st != "approved" {
		t.Errorf("dry run reset rae to %q", st)
	}
	es := e.audited("forge.mr.request_review")
	if len(es) != 1 || !es[0].DryRun || es[0].Outcome != "skipped" {
		t.Fatalf("audit = %+v, want one skipped dry run", es)
	}
}

func TestMRRequestReviewRefusals(t *testing.T) {
	e := newCLIEnv(t)
	if _, err := e.mr("request-review", rateMR); err == nil || !strings.Contains(err.Error(), "--again") {
		t.Errorf("no users, no --again: %v", err)
	}
	if _, err := e.mr("request-review", rateMR, "@nobody-here"); err == nil {
		t.Error("an unknown user was accepted")
	}
	// webPath!1 has no reviewers: --again has nobody to ask.
	out := e.mustMR("request-review", webPath+"!1", "--again")
	if !strings.Contains(out, "no reviewers to ask again") {
		t.Errorf("output = %q", out)
	}
	es := e.audited("forge.mr.request_review")
	if len(es) != 2 || es[0].Outcome != "error" || es[1].Outcome != "skipped" {
		t.Errorf("audit = %+v, want the unknown user's error then the skipped request", es)
	}
	e.srv.MergeMRAs(ref(rateMR), "reviewer-rae")
	if _, err := e.mr("request-review", rateMR, "--again"); err == nil || !strings.Contains(err.Error(), "not open") {
		t.Errorf("merged MR: %v", err)
	}
}

// One user is added and the other cannot be asked again: the plan was on
// screen before the write, the error says which part landed, and both parts
// are audited.
func TestMRRequestReviewPartialSuccess(t *testing.T) {
	e := newCLIEnv(t)
	r := ref(rateMR)
	e.srv.AddUser("reviewer-sam", "Sam Second")
	e.srv.FailRereview("the reviewer cannot be asked now", "reviewer-rae")
	out, err := e.mr("request-review", rateMR, "@reviewer-sam", "@reviewer-rae")
	if err == nil {
		t.Fatalf("a partly failed request exited zero:\n%s", out)
	}
	for _, want := range []string{"only partly landed", "asked @reviewer-sam", "could not ask @reviewer-rae", "the reviewer cannot be asked now"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	for _, want := range []string{"asking for a review on", "@reviewer-sam  not a reviewer yet: added", "@reviewer-rae"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan was not printed before the write; output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "asked for a review on") {
		t.Errorf("success line printed after a failure:\n%s", out)
	}
	if names, _ := e.srv.MRReviewers(r); !slices.Equal(names, []string{"reviewer-rae", "reviewer-sam"}) {
		t.Errorf("the part that could land did not: %v", names)
	}
	es := e.audited("forge.mr.request_review")
	if len(es) != 2 || es[0].Outcome != "ok" || es[1].Outcome != "error" {
		t.Fatalf("audit = %+v, want the landed part (ok) and the failed part (error)", es)
	}
	if u, _ := es[0].Params["users"].([]any); len(u) != 1 || u[0] != "reviewer-sam" {
		t.Errorf("landed users = %v", es[0].Params["users"])
	}
	if u, _ := es[1].Params["users"].([]any); len(u) != 1 || u[0] != "reviewer-rae" || !strings.Contains(es[1].Error, "cannot be asked now") {
		t.Errorf("failed entry = %+v", es[1])
	}
}
