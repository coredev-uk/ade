package tui

import (
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// u on an MR that needs a rebase says so, asks for confirmation, then has the
// forge rebase it: audited, with a pop-up, and the page reads the new head.
func TestUpdateBranchRebases(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 1)
	if err := e.srv.SetBehind(ref, true); err != nil {
		t.Fatal(err)
	}
	e.open(ref.String())
	e.want("it needs a rebase: u updates the branch")
	before := e.m.mr.data.mr.HeadSHA

	e.press("u")
	e.want("Update branch · "+ref.String(), "behind main: the forge can update it", "enter")
	if n, _, _ := e.srv.Rebases(ref); n != 0 {
		t.Fatal("rebased before the confirmation")
	}
	e.press("enter")
	if n, _, _ := e.srv.Rebases(ref); n != 1 {
		t.Fatalf("rebase requests = %d, want 1", n)
	}
	if after := e.m.mr.data.mr.HeadSHA; after == before {
		t.Error("the page still shows the old head")
	}
	e.wantNot("it needs a rebase")
	if es := e.audited("forge.mr.rebase"); len(es) != 1 || es[0].Outcome != "ok" {
		t.Errorf("audit = %+v", es)
	}
	var popped bool
	for _, ev := range e.drainEvents() {
		popped = popped || (ev.Kind == "mr.rebase" && ev.Severity == events.Success && strings.Contains(ev.Title, "Rebased "+ref.String()))
	}
	if !popped {
		t.Error("no success pop-up for the rebase")
	}
}

// A conflict is shown with the git commands that fix it by hand; when the
// forge refuses anyway, the failure carries the same commands.
func TestUpdateBranchConflictShowsLocalCommands(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 1)
	if err := e.srv.SetConflicts(ref, true); err != nil {
		t.Fatal(err)
	}
	e.open(ref.String())
	e.press("u")
	e.want("it conflicts with main", "git fetch origin", "git rebase origin/main", "git push --force-with-lease origin")
	e.press("enter")
	if es := e.audited("forge.mr.rebase"); len(es) != 1 || es[0].Outcome != "error" {
		t.Errorf("audit = %+v, want one error", es)
	}
	var failed events.Event
	for _, ev := range e.drainEvents() {
		if ev.Kind == "mr.rebase.failed" {
			failed = ev
		}
	}
	if failed.Kind == "" || !strings.Contains(failed.Detail, "git rebase origin/main") || !strings.Contains(failed.Detail, "--force-with-lease") {
		t.Errorf("failure = %+v, want the local commands in its detail", failed)
	}
}

func TestUpdateBranchRefusesAMergedMR(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 1)
	if err := e.srv.MergeMR(ref); err != nil {
		t.Fatal(err)
	}
	e.open(ref.String())
	e.press("u")
	if e.m.mr.modal != nil {
		t.Error("the update dialog opened on a merged MR")
	}
	if n, _, _ := e.srv.Rebases(ref); n != 0 {
		t.Errorf("rebase requests = %d", n)
	}
}

// esc leaves the dialog without sending anything.
func TestUpdateBranchEscCancels(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 1)
	e.open(ref.String())
	e.press("u", "esc")
	if e.m.mr.modal != nil {
		t.Error("esc did not close the dialog")
	}
	if n, _, _ := e.srv.Rebases(ref); n != 0 {
		t.Errorf("rebase requests = %d", n)
	}
}

// The diff read before the update quotes the old head; a rebase reads it again
// so a comment lands on the new version.
func TestUpdateBranchReloadsTheDiff(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 2)
	if err := e.srv.SetBehind(ref, true); err != nil {
		t.Fatal(err)
	}
	e.open(ref.String())
	e.press("3")
	old := e.m.mr.data.diff.HeadSHA
	if old == "" {
		t.Fatal("the diff was not read")
	}
	e.press("u", "enter")
	if got := e.m.mr.data.diff.HeadSHA; got == old || got != e.m.mr.data.mr.HeadSHA {
		t.Errorf("diff head %s after the update, MR head %s, before %s", got, e.m.mr.data.mr.HeadSHA, old)
	}
}
