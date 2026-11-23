package review

import (
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
)

func TestMergeVerdict(t *testing.T) {
	running := &forge.Pipeline{Status: "running"}
	failed := &forge.Pipeline{Status: "failed"}
	cases := []struct {
		name string
		mr   forge.MR
		ok   bool
		auto bool
		why  string
	}{
		{"mergeable", forge.MR{State: "opened", DetailedStatus: "mergeable"}, true, false, ""},
		{"status unknown", forge.MR{State: "opened"}, true, false, ""},
		{"merged", forge.MR{State: "merged"}, false, false, "already merged"},
		{"closed", forge.MR{State: "closed"}, false, false, "closed"},
		{"draft", forge.MR{State: "opened", Draft: true, DetailedStatus: "draft_status"}, false, false, "draft"},
		{"conflicts", forge.MR{State: "opened", HasConflicts: true, TargetBranch: "main"}, false, false, "conflicts with main"},
		{"running pipeline can auto-merge", forge.MR{State: "opened", DetailedStatus: "ci_still_running", Pipeline: running}, false, true, "still running"},
		{"failed pipeline cannot", forge.MR{State: "opened", DetailedStatus: "ci_must_pass", Pipeline: failed}, false, false, "must pass"},
		{"needs approval", forge.MR{State: "opened", DetailedStatus: "not_approved"}, false, false, "approval"},
		{"threads", forge.MR{State: "opened", DetailedStatus: "discussions_not_resolved", Unresolved: 2}, false, false, "2 unresolved threads"},
		{"github clean", forge.MR{State: "opened", DetailedStatus: "clean"}, true, false, ""},
		{"github unstable", forge.MR{State: "opened", DetailedStatus: "unstable"}, true, false, ""},
		{"github has_hooks", forge.MR{State: "opened", DetailedStatus: "has_hooks"}, true, false, ""},
		{"github behind", forge.MR{State: "opened", DetailedStatus: "behind", TargetBranch: "main"}, false, false, "it is behind main"},
		{"github blocked", forge.MR{State: "opened", DetailedStatus: "blocked"}, false, false, "required review or check"},
		{"github unknown", forge.MR{State: "opened", DetailedStatus: "unknown"}, false, false, "still checking"},
		{"unknown status is quoted", forge.MR{State: "opened", DetailedStatus: "jira_association_missing"}, false, false, "jira association missing"},
	}
	for _, c := range cases {
		v := Verdict(c.mr, Hints{})
		if v.OK != c.ok || v.AutoOK != c.auto || !strings.Contains(v.Why, c.why) {
			t.Errorf("%s: %+v, want ok=%v auto=%v why~%q", c.name, v, c.ok, c.auto, c.why)
		}
	}
}

func TestVerdictDraftHint(t *testing.T) {
	mr := forge.MR{State: "opened", Draft: true}
	if v := Verdict(mr, Hints{}); v.Why != "it is still a draft" {
		t.Errorf("no hint: %q", v.Why)
	}
	if v := Verdict(mr, Hints{Ready: "R marks it ready"}); v.Why != "it is still a draft (R marks it ready)" {
		t.Errorf("hint: %q", v.Why)
	}
}

func TestVerdictUpdateHint(t *testing.T) {
	h := Hints{Update: "u updates the branch"}
	for _, c := range []struct {
		mr   forge.MR
		want string
	}{
		{forge.MR{State: "opened", HasConflicts: true, TargetBranch: "main"}, "it conflicts with main: u updates the branch, or rebase it locally"},
		{forge.MR{State: "opened", DetailedStatus: "need_rebase"}, "it needs a rebase: u updates the branch"},
		{forge.MR{State: "opened", DetailedStatus: "behind", TargetBranch: "main"}, "it is behind main: u updates the branch"},
	} {
		if v := Verdict(c.mr, h); v.Why != c.want {
			t.Errorf("%s: %q, want %q", c.mr.DetailedStatus, v.Why, c.want)
		}
	}
	if v := Verdict(forge.MR{State: "opened", HasConflicts: true, TargetBranch: "main"}, Hints{}); v.Why != "it conflicts with main" {
		t.Errorf("no hint: %q", v.Why)
	}
}
