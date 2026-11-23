package review

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forge"
)

type reviewerWriter struct {
	forge.MRWriter
	asked [][]string
}

func (w *reviewerWriter) Reviewers(context.Context, forge.Ref) ([]forge.Reviewer, error) {
	return []forge.Reviewer{{User: forge.User{Username: "rae"}, State: forge.ReviewerCommented}}, nil
}

func (w *reviewerWriter) RequestReview(_ context.Context, _ forge.Ref, users []string) error {
	w.asked = append(w.asked, users)
	return nil
}

func TestParseUsernames(t *testing.T) {
	got := ParseUsernames("@rae, sam\t@Rae\n@tia,,")
	if want := []string{"rae", "sam", "tia"}; !slices.Equal(got, want) {
		t.Errorf("ParseUsernames = %v, want %v", got, want)
	}
	if got := ParseUsernames(" @ , "); len(got) != 0 {
		t.Errorf("a bare @ is no user: %v", got)
	}
}

func TestPlanReviewRequest(t *testing.T) {
	cur := []forge.Reviewer{
		{User: forge.User{Username: "rae", Name: "Rae R"}, State: forge.ReviewerChangesRequested},
		{User: forge.User{Username: "sam"}, State: forge.ReviewerApproved},
	}
	got := PlanReviewRequest(cur, []string{"tia", "RAE"}, false)
	want := []ReviewTarget{
		{Username: "tia", Action: RequestAdd},
		{Username: "rae", Action: RequestAgain, Was: forge.ReviewerChangesRequested, Name: "Rae R"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("named only = %+v, want %+v", got, want)
	}
	got = PlanReviewRequest(cur, nil, true)
	if len(got) != 2 || got[0].Username != "rae" || got[1].Username != "sam" || got[1].Action != RequestAgain {
		t.Errorf("again = %+v", got)
	}
	if got = PlanReviewRequest(cur, []string{"tia"}, true); !slices.Equal(Usernames(got), []string{"tia", "rae", "sam"}) {
		t.Errorf("named and again = %+v", got)
	}
	if got = PlanReviewRequest(nil, nil, true); len(got) != 0 {
		t.Errorf("nobody to ask = %+v", got)
	}
}

func TestRequestReviewIsAudited(t *testing.T) {
	log, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	mr := forge.Ref{Project: "grp/api", IID: 2, Kind: forge.KindMR}
	w := &reviewerWriter{}
	if err := Audited(w, log, false).RequestReview(context.Background(), mr, []string{"rae"}, true); err != nil {
		t.Fatal(err)
	}
	es, _ := log.Query(audit.Filter{Action: "forge.mr.request_review"})
	if len(w.asked) != 1 || len(es) != 1 || es[0].Item != mr.String() || es[0].Params["again"] != true {
		t.Fatalf("asked=%v audit=%+v", w.asked, es)
	}
	if users, _ := es[0].Params["users"].([]any); len(users) != 1 || users[0] != "rae" {
		t.Errorf("users param = %v", es[0].Params["users"])
	}

	// A dry run asks nobody and records that it would have.
	if err := Audited(w, log, true).RequestReview(context.Background(), mr, []string{"rae"}, false); err != nil || len(w.asked) != 1 {
		t.Fatalf("dry run wrote: %v %v", err, w.asked)
	}
	es, _ = log.Query(audit.Filter{Action: "forge.mr.request_review"})
	if len(es) != 2 || !es[1].DryRun || es[1].Outcome != "skipped" {
		t.Fatalf("dry-run audit: %+v", es)
	}
	if rs, err := Audited(w, nil, false).Reviewers(context.Background(), mr); err != nil || len(rs) != 1 {
		t.Errorf("Reviewers = %v, %v", rs, err)
	}
}

func TestRequestReviewOnADriverWithoutReviewersIsAnError(t *testing.T) {
	a := Audited(&struct{ forge.MRWriter }{}, nil, false)
	mr := forge.Ref{Project: "g/p", IID: 1, Kind: forge.KindMR}
	if err := a.RequestReview(context.Background(), mr, []string{"rae"}, false); err == nil || !strings.Contains(err.Error(), "cannot request reviews") {
		t.Fatalf("err = %v", err)
	}
	if _, err := a.Reviewers(context.Background(), mr); err == nil {
		t.Fatal("Reviewers on a driver without them was not an error")
	}
}

type partialWriter struct{ forge.MRWriter }

func (partialWriter) Reviewers(context.Context, forge.Ref) ([]forge.Reviewer, error) { return nil, nil }

func (partialWriter) RequestReview(context.Context, forge.Ref, []string) error {
	return &forge.PartialReviewError{Asked: []string{"sam"}, Failed: []string{"rae"}, Err: errors.New("refused")}
}

func TestPartialRequestReviewAuditsBothParts(t *testing.T) {
	log, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	mr := forge.Ref{Project: "grp/api", IID: 2, Kind: forge.KindMR}
	err = Audited(partialWriter{}, log, false).RequestReview(context.Background(), mr, []string{"sam", "rae"}, false)
	var pe *forge.PartialReviewError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want the partial error", err)
	}
	es, _ := log.Query(audit.Filter{Action: "forge.mr.request_review"})
	if len(es) != 2 || es[0].Outcome != "ok" || es[1].Outcome != "error" || es[1].Error == "" {
		t.Fatalf("audit = %+v", es)
	}
}
