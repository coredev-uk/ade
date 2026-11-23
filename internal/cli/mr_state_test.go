package cli

import (
	"strings"
	"testing"
)

func TestMRCloseAndReopen(t *testing.T) {
	e := newCLIEnv(t)
	state := func(r string) string { return e.mustMR("show", r) }

	// Close needs --yes, but a dry run does not and changes nothing.
	if out, err := e.mr("close", rateMR); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("close without --yes: %v %q", err, out)
	}
	if out := e.mustMR("close", rateMR, "--dry-run"); !strings.Contains(out, "dry-run: would close "+rateMR) {
		t.Errorf("dry run output = %q", out)
	}
	if !strings.Contains(state(rateMR), "opened") {
		t.Fatal("dry run closed the MR")
	}
	if es := e.audited("forge.mr.close"); len(es) != 1 || !es[0].DryRun || es[0].Outcome != "skipped" {
		t.Fatalf("dry-run audit = %+v", es)
	}

	if out := e.mustMR("close", rateMR, "--yes"); !strings.Contains(out, "closed "+rateMR) {
		t.Errorf("output = %q", out)
	}
	if es := e.audited("forge.mr.close"); len(es) != 2 || es[1].Outcome != "ok" {
		t.Errorf("audit = %+v", es)
	}
	if _, err := e.mr("close", rateMR, "--yes"); err == nil || !strings.Contains(err.Error(), "closed, not open") {
		t.Errorf("second close: %v", err)
	}

	e.mustMR("reopen", rateMR, "--dry-run")
	if !strings.Contains(state(rateMR), "closed") {
		t.Fatal("dry run reopened the MR")
	}
	if out := e.mustMR("reopen", rateMR); !strings.Contains(out, "reopened "+rateMR) { // no --yes needed
		t.Errorf("output = %q", out)
	}
	if es := e.audited("forge.mr.reopen"); len(es) != 2 || es[1].Outcome != "ok" {
		t.Errorf("audit = %+v", es)
	}
	if _, err := e.mr("reopen", rateMR); err == nil || !strings.Contains(err.Error(), "open, not closed") {
		t.Errorf("reopen of an open MR: %v", err)
	}
	// Merged: neither.
	e.mustMR("merge", rateMR, "--yes")
	for _, a := range [][]string{{"close", rateMR, "--yes"}, {"reopen", rateMR}} {
		if _, err := e.mr(a...); err == nil || !strings.Contains(err.Error(), "merged") {
			t.Errorf("%v on a merged MR: %v", a, err)
		}
	}
}
