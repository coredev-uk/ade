package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

func (e *cliEnv) issueCmd(args ...string) (string, error) { return e.run(newIssueCmd, args...) }

func TestIssueAssignAddsMeAndKeepsTheOthers(t *testing.T) {
	e := newCLIEnv(t)
	c := gitlab.New(e.srv.URL, fake.Token)
	ctx := context.Background()
	me, err := c.Me(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// An issue that Rae, not I, is working on.
	const issue = "ade-demo-env/api#1"
	rae := forge.User{ID: 2, Username: "reviewer-rae"}
	if err := c.SetAssignees(ctx, ref(issue), []forge.User{rae}); err != nil {
		t.Fatal(err)
	}
	before, err := c.Issue(ctx, ref(issue))
	if err != nil || len(before.Assignees) != 1 || before.Assignees[0].ID != rae.ID {
		t.Fatalf("setup: %+v, %v", before.Assignees, err)
	}

	out, err := e.issueCmd("assign", issue, "--dry-run")
	if err != nil || !strings.Contains(out, "dry-run: would assign "+issue+" to ") {
		t.Fatalf("%v\n%s", err, out)
	}
	if is, _ := c.Issue(ctx, ref(issue)); len(is.Assignees) != len(before.Assignees) {
		t.Fatal("--dry-run assigned")
	}

	asJSON(t)
	out, err = e.issueCmd("assign", issue)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got assignOut
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Action != "assign" || !hasString(got.Assignees, me.Username) {
		t.Fatalf("%v %+v", err, got)
	}
	after, _ := c.Issue(ctx, ref(issue))
	if len(after.Assignees) != len(before.Assignees)+1 {
		t.Errorf("assignees %v -> %v: the others were not kept", before.Assignees, after.Assignees)
	}
	if es := e.audited("forge.issue.assign"); len(es) != 2 || !es[0].DryRun || es[1].Outcome != "ok" {
		t.Errorf("audit = %+v", es)
	}

	out, err = e.issueCmd("assign", issue)
	if err != nil || !strings.Contains(out, `"action": "unchanged"`) || len(e.audited("forge.issue.assign")) != 2 {
		t.Fatalf("second run: %v\n%s", err, out)
	}

	jsonOut = false
	if out, err = e.issueCmd("assign", issue, "--replace"); err != nil || !strings.Contains(out, "assigned "+issue+" to "+me.Username) {
		t.Fatalf("--replace: %v\n%s", err, out)
	}
	if is, _ := c.Issue(ctx, ref(issue)); len(is.Assignees) != 1 || is.Assignees[0].ID != me.ID {
		t.Errorf("after --replace: %v", is.Assignees)
	}
}

func hasString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestIssueAssignRefusesAnMR(t *testing.T) {
	e := newCLIEnv(t)
	if _, err := e.issueCmd("assign", rateMR); err == nil || !strings.Contains(err.Error(), "merge request") {
		t.Errorf("err = %v", err)
	}
}
