package overview

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func ref(project string, iid int) forge.Ref {
	return forge.Ref{Project: project, IID: iid, Kind: forge.KindIssue}
}

func mrRef(project string, iid int) forge.Ref {
	return forge.Ref{Project: project, IID: iid, Kind: forge.KindMR}
}

func two(n int) *int { return &n }

// item is a tracker issue with one child issue in api; each test adds the MR.
func item() domain.WorkItem {
	due, _ := forge.ParseDate("2026-11-13")
	sp := &domain.Sprint{Key: "42", Title: "Sprint 42", Due: due}
	return domain.WorkItem{
		Project: "demo", Ref: ref("acme/tracker", 15),
		Issue: forge.Issue{
			Ref: ref("acme/tracker", 15), Title: "Rate-limit the export endpoint", State: "opened",
			Assignees:   []forge.User{{Username: "paul"}},
			Description: "Exports hammer the API.\n\n- [x] measure\n- [ ] add limiter\n\n## Acceptance criteria\n\n- [x] 429 over the limit\n- [ ] documented\n- [ ] dashboard shows it",
		},
		Children: []forge.Issue{{Ref: ref("acme/api", 3), Title: "Limiter middleware", State: "opened"}},
		Stage:    domain.StageReview, Court: domain.CourtReviewers, Reason: "2 unresolved threads",
		Type: "feature", Apps: []string{"api"}, Repos: []string{"api"}, Workflow: "in review",
		Weight: two(5), Sprint: sp,
	}
}

func snap(it domain.WorkItem) Snapshot {
	return Snapshot{
		Ref: it.Ref, Found: true, Item: it, Forge: "gl",
		Window:   domain.Window{Current: it.Sprint, DaysLeft: 3},
		SyncedAt: now.Add(-4 * time.Minute),
		Notes:    "ask support about 50 req/min",
	}
}

func plain(s string) string { return ansi.Strip(s) }

func lines(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }

func TestDraftMRWithoutPipeline(t *testing.T) {
	it := item()
	it.Stage, it.Court, it.Reason = domain.StageDraft, domain.CourtYou, "finish the draft"
	it.MRs = []forge.MR{{
		Ref: mrRef("acme/api", 5), State: "opened", Draft: true, SourceBranch: "3-rate-limit",
		WebURL: "https://git.example/acme/api/-/merge_requests/5", Approvals: forge.Approvals{Required: -1},
	}}
	got := plain(Render(snap(it), "", 60, now))
	want := ` acme/tracker#15  Rate-limit the export endpoint

 ●━━●━━●━━◉──○──○──○  draft
 as st br dr ci rv mg
 you · finish the draft

 weight    5w
 sprint    Sprint 42 · ends Fri 13 Nov · 3d left
 type      feature · api
 workflow  in review
 state     opened
 assignees paul

 Code ────────────────────────────────────── 1 issue · 1 MR
 ┃ api#3  Limiter middleware
 ┃ !5 · draft · 3-rate-limit
 ┃ no pipeline · no open threads
 ┃ https://git.example/acme/api/-/merge_requests/5

 Description ──────────────────────────────────── 1/2 tasks
 Exports hammer the API.

 [x] measure
 [ ] add limiter

 Acceptance ────────────────────────────────────── 1/3 done
 [x] 429 over the limit
 [ ] documented
 [ ] dashboard shows it

 notes ────────────────────────────────────────────────────
 ask support about 50 req/min
 synced 4m ago
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestReviewWithThreadsFailedPipelineAndConflicts(t *testing.T) {
	it := item()
	it.Court, it.Reason = domain.CourtYou, "pipeline failed"
	it.Stage = domain.StageCI
	it.MRs = []forge.MR{{
		Ref: mrRef("acme/api", 5), State: "opened", SourceBranch: "3-rate-limit-the-export-endpoint-with-a-token-bucket",
		Pipeline: &forge.Pipeline{Status: "failed"}, Unresolved: 2, HasConflicts: true,
		Approvals: forge.Approvals{Required: 2, ApprovedBy: []forge.User{{Username: "sam"}}},
		WebURL:    "https://git.example/acme/api/-/merge_requests/5",
	}}
	got := plain(Render(snap(it), "", 50, now))
	for _, want := range []string{
		"you · ✕ pipeline failed",
		"┃ !5 · open · 3-rate-limit-the-export-endpoint-…",
		"┃ ✕ pipeline failed · ✕ conflicts",
		"┃ ✉ 2 unresolved threads · 1/2 approved",
		"┃ https://git.example/acme/api/-/merge_requests/\n",
		"┃ 5\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("view lacks %q:\n%s", want, got)
		}
	}
	for i, l := range lines(got) {
		if w := ansi.StringWidth(l); w > 50 {
			t.Errorf("line %d is %d cells at 50 columns: %q", i, w, l)
		}
	}
}

func TestMergedMRShowsNoPipelineOrThreads(t *testing.T) {
	it := item()
	it.Stage, it.Court, it.Reason = domain.StageMerged, domain.CourtYou, "set workflow::done"
	merged := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	it.MRs = []forge.MR{{
		Ref: mrRef("acme/api", 5), State: "merged", SourceBranch: "3-limiter", MergedAt: &merged,
		Pipeline: &forge.Pipeline{Status: "success"},
	}}
	got := plain(Render(snap(it), "", 70, now))
	if !strings.Contains(got, "┃ !5 · ✓ merged 10 Nov · 3-limiter") {
		t.Errorf("merged line missing:\n%s", got)
	}
	for _, not := range []string{"pipeline", "threads"} {
		if strings.Contains(got, not) {
			t.Errorf("a merged MR shows %q:\n%s", not, got)
		}
	}
	if !strings.Contains(got, "●━━●━━●━━●━━●━━●━━◉  merged") {
		t.Errorf("track should end on the merged station:\n%s", got)
	}
}

func TestNoChildYet(t *testing.T) {
	it := item()
	it.Children, it.MRs = nil, nil
	it.Stage, it.Court, it.Reason = domain.StageAssigned, domain.CourtYou, ""
	it.Weight, it.Sprint, it.Workflow, it.Type, it.Apps = nil, nil, "", "", nil
	it.Issue.Assignees = nil
	got := plain(Render(snap(it), "", 60, now))
	for _, want := range []string{
		"Code ─", "not started", "no child issue yet", "unweighted", "no sprint", "▲ untriaged", "unassigned",
		"◉──○──○──○──○──○──○  assigned",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("view lacks %q:\n%s", want, got)
		}
	}
}

func TestSeveralChildrenInOneRepoSplitTheirMRs(t *testing.T) {
	it := item()
	it.Children = []forge.Issue{
		{Ref: ref("acme/api", 3), Title: "Limiter"},
		{Ref: ref("acme/api", 4), Title: "Docs"},
		{Ref: ref("acme/web", 9), Title: "Banner"},
	}
	it.MRs = []forge.MR{
		{Ref: mrRef("acme/api", 5), State: "opened", SourceBranch: "4-docs"},
		{Ref: mrRef("acme/api", 6), State: "opened", SourceBranch: "3-limiter"},
		{Ref: mrRef("acme/web", 2), State: "opened", SourceBranch: "wip"},
		{Ref: mrRef("acme/lib", 1), State: "opened", SourceBranch: "stray"},
	}
	got := plain(Render(snap(it), "", 60, now))
	order := []string{"api#3  Limiter", "!6 · open · 3-limiter", "api#4  Docs", "!5 · open · 4-docs", "web#9  Banner", "!2 · open · wip", "lib · no child issue", "!1 · open · stray"}
	at := 0
	for _, s := range order {
		i := strings.Index(got[at:], s)
		if i < 0 {
			t.Fatalf("%q missing or out of order:\n%s", s, got)
		}
		at += i
	}
}

func TestDescriptionWithoutAcceptanceOrBody(t *testing.T) {
	it := item()
	it.Issue.Description = ""
	got := plain(Render(snap(it), "", 60, now))
	if !strings.Contains(got, "no description") || strings.Contains(got, "Acceptance") {
		t.Errorf("empty description:\n%s", got)
	}
	it.Issue.Description = "## Acceptance\n- [ ] one"
	got = plain(Render(snap(it), "", 60, now))
	if strings.Contains(got, "Description") || !strings.Contains(got, "Acceptance ─") {
		t.Errorf("acceptance only:\n%s", got)
	}
}

func TestNotesSectionIsFaintAndMarkdownIsReadable(t *testing.T) {
	it := item()
	s := snap(it)
	s.Notes = "# Plan\n- first thing that is long enough to wrap onto a second line of the pane\n- [x] done already\n\n\n\nlast"
	got := plain(Render(s, "", 40, now))
	for _, want := range []string{" Plan\n", " - first thing that is long enough to\n", "   wrap onto a second line of the pane\n", " [x] done already\n\n last\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("notes lack %q:\n%s", want, got)
		}
	}
	s.Notes = ""
	if got := plain(Render(s, "", 40, now)); !strings.Contains(got, "no notes yet") {
		t.Errorf("empty notes:\n%s", got)
	}
}

func TestNarrowAndWideWidths(t *testing.T) {
	it := item()
	it.MRs = []forge.MR{{
		Ref: mrRef("acme/api", 5), State: "opened", SourceBranch: "3-rate-limit", Pipeline: &forge.Pipeline{Status: "running"}, Unresolved: 1,
		WebURL: "https://git.example/acme/api/-/merge_requests/5",
	}}
	it.Issue.Description = strings.Repeat("a very long description sentence ", 12)
	for _, w := range []int{30, 40, 50, 100} {
		got := plain(Render(snap(it), "", w, now))
		for i, l := range lines(got) {
			if lw := ansi.StringWidth(l); lw > w {
				t.Errorf("width %d: line %d is %d cells: %q", w, i, lw, l)
			}
		}
		if !strings.Contains(got, "⟳ pipeline running") {
			t.Errorf("width %d lacks the pipeline:\n%s", w, got)
		}
	}
	// 100 columns: rules run the full width.
	for _, l := range lines(plain(Render(snap(it), "", 100, now))) {
		if strings.HasPrefix(l, " Code ") && ansi.StringWidth(l) != 99 {
			t.Errorf("rule is %d cells at 100 columns: %q", ansi.StringWidth(l), l)
		}
	}
}

func TestAsciiGlyphsAndNoColour(t *testing.T) {
	it := item()
	it.Court, it.Reason = domain.CourtBlocked, "pipeline failed"
	it.Stage = domain.StageCI
	it.MRs = []forge.MR{{Ref: mrRef("acme/api", 5), State: "opened", Pipeline: &forge.Pipeline{Status: "failed"}, Unresolved: 2, HasConflicts: true}}
	out := Render(snap(it), "ascii", 60, now)
	if !strings.Contains(out, "\x1b[") {
		t.Fatal("the view should carry styles before the writer strips them")
	}
	p := plain(out)
	for _, r := range "●━◉○✓✕▲✉⟳┃─◎" {
		if strings.ContainsRune(p, r) {
			t.Errorf("ascii view has %q:\n%s", r, p)
		}
	}
	for _, want := range []string{"*==*==*==*==@--o--o  ci", "! blocked - x pipeline failed", "| x pipeline failed - x conflicts - m 2 unresolved threads", "1 issue - 1 MR"} {
		if !strings.Contains(p, want) {
			t.Errorf("ascii view lacks %q:\n%s", want, p)
		}
	}
}
