package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// v asks for a review again: the dialog lists the reviewers and what will
// happen to them, nothing is written until enter, and the write is audited.
func TestRequestReviewAgainFromTheDialog(t *testing.T) {
	e := newMREnv(t, fake.Free) // no AI: this must work without it
	ref := mrRef("api", 1)
	if err := e.srv.SetReviewState(ref, "reviewer-rae", "requested_changes"); err != nil {
		t.Fatal(err)
	}
	e.open(ref.String())
	e.press("v")
	if e.m.mr.modal == nil || e.m.mr.modal.kind != modalRequestReview {
		t.Fatal("v did not open the request review dialog")
	}
	e.want("Request review · "+ref.String(), "reviewers now", "Rae Reviewer (reviewer-rae) · changes requested",
		"[x] tab  ask the 1 current reviewer again", "@reviewer-rae · changes requested → asked again", "enter ask")
	if st, _ := e.srv.ReviewerState(ref, "reviewer-rae"); st != "requested_changes" {
		t.Fatalf("the dialog alone changed rae to %q", st)
	}
	e.press("enter")
	if e.m.mr.modal != nil {
		t.Error("enter left the dialog open")
	}
	if st, _ := e.srv.ReviewerState(ref, "reviewer-rae"); st != "unreviewed" {
		t.Errorf("rae = %q, want unreviewed after the re-request", st)
	}
	es := e.audited("forge.mr.request_review")
	if len(es) != 1 || es[0].Outcome != "ok" || es[0].Item != ref.String() || es[0].Params["again"] != true {
		t.Errorf("audit = %+v", es)
	}
	var ok bool
	for _, ev := range e.drainEvents() {
		ok = ok || (ev.Kind == "mr.request_review" && ev.Severity == events.Success && strings.Contains(ev.Title, "@reviewer-rae"))
	}
	if !ok {
		t.Error("no success pop-up for the request")
	}
}

func TestRequestReviewNamedUsersAndTheToggle(t *testing.T) {
	e := newMREnv(t, fake.Premium)
	ref := mrRef("api", 1)
	e.srv.AddUser("reviewer-sam", "Sam Second")
	if err := e.srv.SetReviewState(ref, "reviewer-rae", "approved"); err != nil {
		t.Fatal(err)
	}
	e.open(ref.String())
	e.press("v")
	e.typ("@reviewer-sam")
	e.want("@reviewer-sam · added as a reviewer", "@reviewer-rae · approved → asked again")
	e.press("tab") // leave the current reviewers alone
	e.want("[ ] tab  ask the 1 current reviewer again")
	e.wantNot("@reviewer-rae · approved → asked again")
	e.press("enter")
	names, _ := e.srv.MRReviewers(ref)
	if !slices.Equal(names, []string{"reviewer-rae", "reviewer-sam"}) {
		t.Errorf("reviewers = %v: sam must be added and rae kept", names)
	}
	if st, _ := e.srv.ReviewerState(ref, "reviewer-rae"); st != "approved" {
		t.Errorf("rae = %q: she was not asked again", st)
	}
	if es := e.audited("forge.mr.request_review"); len(es) != 1 {
		t.Fatalf("audit = %+v", es)
	} else if u, _ := es[0].Params["users"].([]any); len(u) != 1 || u[0] != "reviewer-sam" || es[0].Params["again"] != false {
		t.Errorf("audited params = %v", es[0].Params)
	}
}

func TestRequestReviewRefusalsAndCancel(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 1)
	e.open(ref.String())
	e.press("v")
	e.press("tab") // nobody left to ask
	e.press("enter")
	if e.m.mr.modal == nil {
		t.Fatal("enter with nobody to ask closed the dialog")
	}
	e.want("name whom to ask")
	e.press("esc")
	if e.m.mr.modal != nil {
		t.Error("esc did not close the dialog")
	}
	if n := len(e.audited("forge.mr.request_review")); n != 0 {
		t.Errorf("a cancelled dialog wrote %d audit entries", n)
	}

	// An unknown user is a failure pop-up; the reviewers stay as they were.
	e.press("v")
	e.typ("@nobody-here")
	e.press("enter")
	var failed bool
	for _, ev := range e.drainEvents() {
		failed = failed || (ev.Kind == "mr.request_review.failed" && ev.Severity == events.Error)
	}
	if !failed {
		t.Error("no failure pop-up for an unknown user")
	}
	if es := e.audited("forge.mr.request_review"); len(es) != 1 || es[0].Outcome != "error" {
		t.Errorf("audit = %+v", es)
	}
	if names, _ := e.srv.MRReviewers(ref); !slices.Equal(names, []string{"reviewer-rae"}) {
		t.Errorf("reviewers = %v", names)
	}
}

func TestRequestReviewNeedsAnOpenMR(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 1)
	e.open(ref.String())
	e.m.mr.data.mr.State = "merged"
	e.press("v")
	if e.m.mr.modal != nil {
		t.Error("v opened the dialog on a merged MR")
	}
}

func TestRequestReviewIsInTheHelpAndFooter(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.open(mrRef("api", 1).String())
	e.want("v review again")
	e.press(":")
	e.typ("Ask for a review again")
	e.press("enter")
	if e.m.mr.modal == nil || e.m.mr.modal.kind != modalRequestReview {
		t.Fatal("the palette entry did not open the dialog")
	}
}
