package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/paths"
)

func sampleResult() forgesync.Result {
	sprint := &domain.Sprint{Key: "m42", Title: "Sprint 42", Start: forge.NewDate(2026, 11, 2), Due: forge.NewDate(2026, 11, 13)}
	item := func(iid int, title string, st domain.Stage, c domain.Court, reason string, w *int, assigned bool) domain.WorkItem {
		ref := forge.Ref{Project: "g/tracker", IID: iid}
		return domain.WorkItem{
			Project: "demo", Ref: ref, Issue: forge.Issue{Ref: ref, Title: title, WebURL: "https://example.com/" + ref.String()},
			Stage: st, Court: c, Reason: reason, Weight: w, Sprint: sprint, Assigned: assigned, Repos: []string{"api"},
		}
	}
	billing := item(6, "Billing export times out", domain.StageAssigned, domain.CourtNobody, "unassigned", nil, false)
	billing.Issue.Author = forge.User{Username: "sam", Name: "Sam Smith"}
	return forgesync.Result{
		Project: "demo", Tracker: "g/tracker",
		Window: domain.Window{Current: sprint, DaysLeft: 3},
		Items: []domain.WorkItem{
			item(3, "Retry failed webhook deliveries", domain.StageAssigned, domain.CourtYou, "start work", new(5), true),
			item(1, "Rate-limit the export endpoint", domain.StageReview, domain.CourtYou, "2 unresolved threads", new(5), true),
			item(5, "Export timestamps ignore tenant timezone", domain.StageStarted, domain.CourtBlocked, "label workflow::blocked", nil, true),
			billing,
			item(10, "Upgrade dependencies", domain.StageDone, domain.CourtNobody, "closed", new(2), true),
		},
	}
}

// Home is built from the cache: rows ordered by court then stage, sprint
// load counts weights honestly, and nothing waits on the network.
func TestHomeRowsAndLoad(t *testing.T) {
	res := sampleResult()
	rows := buildHomeRows([]forgesync.Result{res}, hfAll)
	var got []string
	for _, r := range rows {
		got = append(got, r.ref())
	}
	want := "g/tracker#1 g/tracker#3 g/tracker#5 g/tracker#6"
	if strings.Join(got, " ") != want {
		t.Errorf("rows = %v, want %s (court, then furthest stage; done hidden; others' after mine)", got, want)
	}
	if l := loadOf(&res); l.committed != 12 || l.done != 2 || l.unweighted != 2 || l.items != 5 {
		t.Errorf("load = %+v", l)
	}
	if rows := buildHomeRows([]forgesync.Result{res}, hfBlocked); len(rows) != 1 || rows[0].ref() != "g/tracker#5" {
		t.Errorf("blocked filter = %+v", rows)
	}
}

// The sprint-open section is judged on the tracker issue: a ticket assigned to
// anyone leaves it (search still finds it), and the rows say who raised it.
func TestSprintOpenListsOnlyTrackerUnassignedTickets(t *testing.T) {
	res := sampleResult()
	taken := res.Items[3]
	taken.Ref.IID = 7
	taken.Issue.Ref = taken.Ref
	taken.Issue.Title = "Billing invoices overflow"
	taken.Issue.Assignees = []forge.User{{Username: "kim", Name: "Kim Lee"}}
	taken.Reason = "assigned to Kim Lee (kim)"
	res.Items = append(res.Items, taken)

	refs := func(q homeQuery) string {
		var got []string
		for _, r := range buildRows([]forgesync.Result{res}, q) {
			if r.section == secSprintOpen {
				got = append(got, r.ref())
			}
		}
		return strings.Join(got, " ")
	}
	if got := refs(homeQuery{}); got != "g/tracker#6" {
		t.Errorf("sprint open = %q, want only the unassigned #6", got)
	}
	if got := refs(homeQuery{text: "invoices"}); got != "g/tracker#7" {
		t.Errorf("search for the assigned ticket = %q, want #7", got)
	}

	it := &res.Items[3]
	r := homeRow{section: secSprintOpen, item: it}
	if got := withAuthor(r).Reason; got != "by Sam Smith (sam)" {
		t.Errorf("sprint row reason = %q", got)
	}
	if got := withAuthor(homeRow{section: secMine, item: it}).Reason; got != "unassigned" {
		t.Errorf("my-work row reason = %q, want it untouched", got)
	}
	m := model{g: unicodeGlyphs, w: 120, h: 40}
	if got := ansi.Strip(m.itemDetail(it, 80)); !strings.Contains(got, "by Sam Smith (sam)") {
		t.Errorf("detail lacks the author:\n%s", got)
	}
}

// The first frame after Init shows cached items even when the forge never
// answers: sync runs in the background.
func TestHomeRendersFromCacheWhileSyncHangs(t *testing.T) {
	cfg := config.Defaults()
	a := &app.App{Paths: paths.Paths{CacheDir: t.TempDir()}, Config: &cfg, Bus: nil}
	t.Cleanup(func() { a.Close() })
	st, err := a.Cache()
	if err != nil {
		t.Fatal(err)
	}
	m := model{app: a, cache: st, g: unicodeGlyphs, nc: newNotifCenter(notify.NewInbox(st)), w: 200, h: 40}
	// Simulate the cache read finishing while a sync is still in flight.
	m.syncing = true
	next, _ := m.Update(resultsMsg{results: []forgesync.Result{sampleResult()}})
	m = next.(model)
	out := ansi.Strip(m.render())
	for _, want := range []string{"Rate-limit the export endpoint", "you · 2 unresolved threads", "Sprint 42", "12w committed", "syncing"} {
		if !strings.Contains(out, want) {
			t.Errorf("first frame missing %q:\n%s", want, out)
		}
	}
}

// A narrow sprint rule squeezes the sprint and its load into one compact group
// before dropping anything, so load never disappears while the end date stays.
func TestProjectRuleKeepsLoadCompact(t *testing.T) {
	m := model{g: unicodeGlyphs}
	res := sampleResult()
	full := ansi.Strip(m.projectRule(&res, 160))
	if !strings.Contains(full, "12w committed") {
		t.Fatalf("wide rule lost the full load: %s", full)
	}
	narrow := ansi.Strip(m.projectRule(&res, 56))
	if !strings.Contains(narrow, "left · 2/12w") || strings.Contains(narrow, "committed") {
		t.Errorf("narrow rule should carry compact load: %s", narrow)
	}
}

// Waiting courts keep their emphasis without colour: the warn glyph leads the word.
func TestCourtWordCarriesGlyph(t *testing.T) {
	for _, g := range []glyphs{unicodeGlyphs, asciiGlyphs} {
		for court, word := range map[domain.Court]string{domain.CourtBlocked: "blocked", domain.CourtUpstream: "waits"} {
			it := &domain.WorkItem{Court: court}
			if got := ansi.Strip(courtWord(g, it)); got != g.Warn+" "+word {
				t.Errorf("courtWord(%s) = %q", court, got)
			}
		}
	}
}

func TestTruncLeftKeepsTail(t *testing.T) {
	if got := truncLeft(unicodeGlyphs, "/home/dev/code/ade/projects/demo-gl/api/main", 20); got != "…/demo-gl/api/main" {
		t.Errorf("truncLeft = %q", got)
	}
	if got := truncLeft(unicodeGlyphs, "/a/b", 20); got != "/a/b" {
		t.Errorf("short path changed: %q", got)
	}
}

// The help dialog lists every Home key in both its roomy and its tight form,
// and the tight form fits a short terminal.
func TestHelpListsEveryHomeKey(t *testing.T) {
	want := []string{"j", "k", "g", "G", "tab", "t", "p", "e", "o", "n", "=", "r", ":", "f", "enter", "s", "M", "c", "a", "A", "w", "y", "S", "V", "b", "L", "i", "U", "esc", "?", "q"}
	for _, full := range []bool{true, false} {
		have := map[string]bool{}
		for _, grp := range helpGroups(full) {
			for _, r := range grp.rows {
				for _, k := range strings.Fields(strings.ReplaceAll(r[0], "/", " ")) {
					have[k] = true
				}
			}
		}
		for _, k := range want {
			if !have[k] {
				t.Errorf("help (full=%v) lacks key %q", full, k)
			}
		}
	}
	for _, size := range [][2]int{{80, 24}, {76, 24}} {
		d := helpView(size[0], size[1], unicodeGlyphs)
		if got := lipgloss.Height(d); got > size[1]-2 {
			t.Errorf("help at %dx%d is %d rows", size[0], size[1], got)
		}
		if lipgloss.Width(d) > size[0] {
			t.Errorf("help at %dx%d is %d cols", size[0], size[1], lipgloss.Width(d))
		}
	}
}

// A failed pipeline is marked in the court column without colour: the failure
// glyph and the words, in both glyph sets.
func TestFailedPipelineReasonCarriesGlyph(t *testing.T) {
	for _, g := range []glyphs{unicodeGlyphs, asciiGlyphs} {
		it := &domain.WorkItem{Court: domain.CourtYou, Reason: "pipeline failed"}
		if got, want := ansi.Strip(courtText(g, it)), "you "+g.Sep+" "+g.Bad+" pipeline failed"; got != want {
			t.Errorf("courtText = %q, want %q", got, want)
		}
	}
}
