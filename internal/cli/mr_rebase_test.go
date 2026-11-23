package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMRRebaseNeedsYes(t *testing.T) {
	e := newCLIEnv(t)
	out, err := e.mr("rebase", rateMR)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("rebase without --yes = %q, %v: want a refusal naming --yes", out, err)
	}
	if n, _, _ := e.srv.Rebases(ref(rateMR)); n != 0 {
		t.Errorf("refused rebase sent %d requests", n)
	}
}

func TestMRRebaseDryRun(t *testing.T) {
	e := newCLIEnv(t)
	if err := e.srv.SetBehind(ref(rateMR), true); err != nil {
		t.Fatal(err)
	}
	out := e.mustMR("rebase", rateMR, "--dry-run") // dry-run needs no --yes
	if !strings.Contains(out, "dry-run: would update "+rateMR) || !strings.Contains(out, "main") {
		t.Errorf("dry-run output = %q", out)
	}
	if n, _, _ := e.srv.Rebases(ref(rateMR)); n != 0 {
		t.Errorf("dry run sent %d rebase requests", n)
	}
	es := e.audited("forge.mr.rebase")
	if len(es) != 1 || !es[0].DryRun || es[0].Outcome != "skipped" {
		t.Fatalf("audit = %+v, want one skipped dry run", es)
	}

	// With conflicts reported, the preview already names the local commands.
	if err := e.srv.SetConflicts(ref(rateMR), true); err != nil {
		t.Fatal(err)
	}
	out = e.mustMR("rebase", rateMR, "--dry-run")
	for _, want := range []string{"conflicts with main", "git fetch origin", "git rebase origin/main", "git push --force-with-lease origin 1-rate-limit-the-export-endpoint"} {
		if !strings.Contains(out, want) {
			t.Errorf("conflict preview lacks %q:\n%s", want, out)
		}
	}
	asJSON(t)
	var r rebaseResult
	if err := json.Unmarshal([]byte(e.mustMR("rebase", rateMR, "--dry-run")), &r); err != nil || !r.DryRun || r.Action != "rebase" || len(r.Local) == 0 {
		t.Errorf("json = %+v, %v", r, err)
	}
}

func TestMRRebase(t *testing.T) {
	e := newCLIEnv(t)
	if err := e.srv.SetBehind(ref(rateMR), true); err != nil {
		t.Fatal(err)
	}
	out := e.mustMR("rebase", rateMR, "--yes", "--skip-ci")
	if !strings.Contains(out, "rebased "+rateMR+" onto main (head ") {
		t.Errorf("output = %q", out)
	}
	if n, skip, _ := e.srv.Rebases(ref(rateMR)); n != 1 || !skip {
		t.Errorf("rebase requests = %d skip_ci = %v", n, skip)
	}
	es := e.audited("forge.mr.rebase")
	if len(es) != 1 || es[0].DryRun || es[0].Outcome != "ok" || es[0].Item != rateMR {
		t.Errorf("audit = %+v", es)
	}

	// A merged MR is not rebased.
	if err := e.srv.MergeMR(ref(webPath + "!1")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mr("rebase", webPath+"!1", "--yes"); err == nil || !strings.Contains(err.Error(), "not open") {
		t.Errorf("rebase of a merged MR: %v", err)
	}
}

func TestMRRebaseConflictPrintsLocalCommands(t *testing.T) {
	e := newCLIEnv(t)
	if err := e.srv.SetConflicts(ref(rateMR), true); err != nil {
		t.Fatal(err)
	}
	_, err := e.mr("rebase", rateMR, "--yes")
	if err == nil {
		t.Fatal("a conflicting rebase succeeded")
	}
	for _, want := range []string{"Rebase failed", "git fetch origin", "git rebase origin/main", "git push --force-with-lease origin 1-rate-limit-the-export-endpoint"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	es := e.audited("forge.mr.rebase")
	if len(es) != 1 || es[0].Outcome != "error" {
		t.Errorf("audit = %+v, want one error entry", es)
	}
}

func TestMRRebaseConflictJSONCarriesTheCommands(t *testing.T) {
	e := newCLIEnv(t)
	if err := e.srv.SetConflicts(ref(rateMR), true); err != nil {
		t.Fatal(err)
	}
	asJSON(t)
	out, err := e.mr("rebase", rateMR, "--yes")
	if err == nil {
		t.Fatal("a conflicting rebase succeeded")
	}
	var r rebaseResult
	if jerr := json.Unmarshal([]byte(out), &r); jerr != nil || len(r.Local) == 0 || !strings.Contains(strings.Join(r.Local, "\n"), "git rebase origin/main") {
		t.Errorf("json = %q (%v), want the local commands", out, jerr)
	}
}
