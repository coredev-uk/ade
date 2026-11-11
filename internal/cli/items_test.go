package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
)

func TestPrintItems(t *testing.T) {
	three := 3
	items := []domain.WorkItem{
		{
			Ref: forge.Ref{Project: "g/tracker", IID: 1}, Stage: domain.StageReview, Court: domain.CourtYou,
			Reason: "2 unresolved threads", Weight: &three, Issue: forge.Issue{Title: "Rate-limit exports"},
		},
		{Ref: forge.Ref{Project: "g/tracker", IID: 2}, Stage: domain.StageDraft, Court: domain.CourtNobody, Issue: forge.Issue{Title: "Paginate"}},
	}
	var b bytes.Buffer
	printItems(&b, items)
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("output:\n%s", b.String())
	}
	if f := strings.Fields(lines[0]); strings.Join(f[:5], " ") != "g/tracker#1 review you · 2" || f[len(f)-3] != "3w" {
		t.Errorf("line 1 = %q", lines[0])
	}
	if !strings.Contains(lines[1], "nobody") || !strings.Contains(lines[1], "–") || !strings.HasSuffix(lines[1], "Paginate") {
		t.Errorf("line 2 = %q", lines[1])
	}
}

func TestSelectItems(t *testing.T) {
	results := []forgesync.Result{
		{Project: "a", Items: []domain.WorkItem{{Ref: forge.Ref{Project: "g/t", IID: 1}}}},
		{Project: "b", Items: []domain.WorkItem{{Ref: forge.Ref{Project: "g/t", IID: 2}}}},
	}
	if got, err := selectItems(results, ""); err != nil || len(got) != 2 {
		t.Errorf("all = %v, %v", got, err)
	}
	if got, err := selectItems(results, "b"); err != nil || len(got) != 1 || got[0].Ref.IID != 2 {
		t.Errorf("b = %v, %v", got, err)
	}
	if _, err := selectItems(results, "zzz"); err == nil {
		t.Error("unknown project accepted")
	}
	if _, err := selectItems(nil, ""); err != errNoCache {
		t.Errorf("empty cache err = %v", err)
	}
}

func TestWindowLine(t *testing.T) {
	due, _ := forge.ParseDate("2026-11-13")
	w := domain.Window{Current: &domain.Sprint{Title: "Sprint 42", Due: due}, DaysLeft: 3}
	if got, want := windowLine(w), "Sprint 42 · ends Fri 13 Nov · 3 workdays left"; got != want {
		t.Errorf("windowLine = %q, want %q", got, want)
	}
	w.DaysLeft = 1
	if got := windowLine(w); !strings.HasSuffix(got, "1 workday left") {
		t.Errorf("windowLine = %q", got)
	}
	if got := windowLine(domain.Window{}); got != "no current sprint" {
		t.Errorf("empty window = %q", got)
	}
}
