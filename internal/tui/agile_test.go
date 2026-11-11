package tui

import (
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/agile"
	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func standupFixture() *agile.Standup {
	return &agile.Standup{
		Date: "2026-11-09", Yesterday: "2026-11-06",
		Done: []agile.Entry{
			{At: at("2026-11-06 09:30"), Kind: "mr.merged", Ref: "g/api!1", Text: "merged MR api!1: Rate-limit the export endpoint", Source: agile.SourceForge},
			{At: at("2026-11-06 12:00"), Kind: "audit", Text: "agent.run g/tracker#1", Source: agile.SourceAudit},
			{At: at("2026-11-06 13:00"), Kind: "mr.approved", Text: "api!1 approved by reviewer-rae", Source: agile.SourceEvents},
		},
		Today: []agile.Focus{
			{Ref: "g/tracker#1", Title: "Rate-limit the export endpoint", Stage: "review", Court: "you", Reason: "2 unresolved threads"},
			{Ref: "g/web!9", Title: "Dark mode for the export dashboard", Stage: "review", Court: "you", Reason: "review requested"},
		},
		Blockers: []agile.Blocker{{Ref: "g/tracker#5", Title: "Export timestamps ignore tenant timezone", Reason: "label workflow::blocked"}},
	}
}

func reviewFixture() *agile.Review {
	w := func(n int) *int { return &n }
	return &agile.Review{
		Project: "demo", Basis: agile.BasisWeight,
		Sprint:    domain.Sprint{Title: "Sprint 42", Start: forge.NewDate(2026, 11, 2), Due: forge.NewDate(2026, 11, 13)},
		Committed: agile.Totals{Count: 5, Weight: 10, Weighted: 3, Unweighted: 2},
		Added:     agile.Totals{Count: 1, Weight: 8, Weighted: 1},
		Done:      agile.Totals{Count: 3, Weight: 8, Weighted: 2, Unweighted: 1},
		Carried:   agile.Totals{Count: 3, Weight: 10, Weighted: 2, Unweighted: 1},
		Items: []agile.ReviewItem{
			{Ref: forge.Ref{Project: "g/tracker", IID: 2}, Title: "Upgrade export worker", Weight: w(5), Done: true, KeptOpen: true},
			{Ref: forge.Ref{Project: "g/tracker", IID: 5}, Title: "Hotfix export crash", Weight: w(8), Added: true, MRs: []string{"api!4"}},
			{Ref: forge.Ref{Project: "g/tracker", IID: 6}, Title: "Billing export times out"},
		},
		Burndown: []agile.BurnPoint{{Date: "2026-11-02", Remaining: 10, Ideal: 9}, {Date: "2026-11-03", Remaining: 7, Ideal: 8}},
		Velocity: agile.Velocity{Basis: agile.BasisWeight, Average: func() *float64 { v := 4.0; return &v }(), Sprints: []agile.SprintResult{
			{Title: "Sprint 40", Scope: agile.Totals{Count: 2, Unweighted: 2}, Done: agile.Totals{Count: 2, Unweighted: 2}},
			{Title: "Sprint 41", Scope: agile.Totals{Count: 3, Weight: 6, Weighted: 2, Unweighted: 1}, Done: agile.Totals{Count: 2, Weight: 4, Weighted: 1, Unweighted: 1}, Velocity: func() *int { v := 4; return &v }()},
		}},
		Epics: []agile.EpicGroup{{Epic: "Self-serve exports", Total: agile.Totals{Count: 2, Weight: 5, Weighted: 1, Unweighted: 1}, Done: agile.Totals{Count: 1, Weight: 5, Weighted: 1}}},
	}
}

func agileModel(g glyphs) model {
	res := sampleResult()
	return model{g: g, nc: newNotifCenter(nil), results: []forgesync.Result{res}, w: 120, h: 40}
}

func lineWidths(t *testing.T, s string, w int) {
	t.Helper()
	for i, l := range strings.Split(ansi.Strip(s), "\n") {
		if got := len([]rune(l)); got > w {
			t.Errorf("line %d is %d cells wide, over %d: %q", i, got, w, l)
		}
	}
}

func TestStandupPageShowsThreeSections(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	m.ag = agileUI{kind: agileStandup, standup: standupFixture()}
	out := ansi.Strip(m.agileBody(120, 30))
	for _, want := range []string{
		"Standup", "Mon 9 Nov", "since Fri 6 Nov",
		"Yesterday", "3 changes", "merged MR api!1: Rate-limit the export endpoint", "forge", "audit", "sync",
		"Today", "2 items in your court", "tracker#1", "2 unresolved threads", "web!9", "review requested",
		"Blockers", "1 blocker", "▲ tracker#5", "label workflow::blocked",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("standup lacks %q:\n%s", want, out)
		}
	}
	// A cached item draws its station line; a review request has no stations.
	if !strings.Contains(out, "◉") {
		t.Errorf("no train on tracker#1's line:\n%s", out)
	}
}

func TestStandupPageEmptyStatesAndErrors(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	m.ag = agileUI{kind: agileStandup, standup: &agile.Standup{Date: "2026-11-09", Yesterday: "2026-11-06"}}
	out := ansi.Strip(m.agileBody(100, 20))
	for _, want := range []string{"nothing recorded", "nothing in your court", "nothing in your way"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty standup lacks %q:\n%s", want, out)
		}
	}
	m.ag = agileUI{kind: agileStandup, standupErr: agile.ErrNoCache}
	if out := ansi.Strip(m.agileBody(100, 5)); !strings.Contains(out, "Nothing cached yet. U syncs now") {
		t.Errorf("no-cache state:\n%s", out)
	}
	m.ag = agileUI{kind: agileStandup, standupErr: errors.New("boom")}
	if out := ansi.Strip(m.agileBody(100, 5)); !strings.Contains(out, "could not build this page: boom") {
		t.Errorf("error state:\n%s", out)
	}
	m.ag = agileUI{kind: agileStandup}
	if out := ansi.Strip(m.agileBody(100, 5)); !strings.Contains(out, "reading standup…") {
		t.Errorf("loading state:\n%s", out)
	}
}

func TestAgilePagesFitNarrowTerminalsAndAsciiGlyphs(t *testing.T) {
	m := agileModel(asciiGlyphs)
	for _, kind := range []agileKind{agileStandup, agileReview} {
		m.ag = agileUI{kind: kind, standup: standupFixture(), review: reviewFixture()}
		for _, w := range []int{60, 80, 120} {
			out := m.agileBody(w, 40)
			lineWidths(t, out, w)
			if got := len(strings.Split(ansi.Strip(out), "\n")); got != 40 {
				t.Errorf("kind %d width %d: %d lines, want exactly 40", kind, w, got)
			}
			for _, glyph := range []string{"●", "◉", "○", "✓", "▲", "✕", "━", "─", "┃", "⟳", "▸"} {
				if strings.Contains(out, glyph) {
					t.Errorf("kind %d width %d: unicode glyph %q under host.glyphs=ascii", kind, w, glyph)
				}
			}
		}
	}
}

func TestReviewPageShowsCountAndWeight(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	m.ag = agileUI{kind: agileReview, review: reviewFixture()}
	out := ansi.Strip(m.agileBody(120, 60))
	for _, want := range []string{
		"Sprint review · Sprint 42", "2–13 Nov", "by weight",
		"Committed", "10w · 5 items (2 unweighted)",
		"Added", "8w · 1 item",
		"Done", "8w · 3 items (1 unweighted)",
		"Carried", "10w · 3 items (1 unweighted)",
		"8w of 18w done",
		"Velocity", "average 4.0w per sprint",
		"Sprint 40", "no weighted work", "not in the average",
		"Sprint 41", "4w of 6w", "1 unweighted not counted",
		"Burn-down", "Mon 2 Nov", "ideal 9.0",
		"By epic", "Self-serve exports", "1 of 2 items done, 5w of 5w",
		"done, kept open", "carried", "added mid-sprint", "merged api!4",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("review lacks %q:\n%s", want, out)
		}
	}
	// An unweighted item is a dash, never 0w.
	if strings.Contains(out, "0w") && !strings.Contains(out, "10w") {
		t.Errorf("unweighted must not read 0w:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "tracker#6") && !strings.Contains(l, "–") {
			t.Errorf("unweighted item line has no dash: %q", l)
		}
	}
}

func TestReviewPageCountBasis(t *testing.T) {
	r := reviewFixture()
	r.Basis = agile.BasisCount
	r.Velocity = agile.Velocity{Basis: agile.BasisCount, Sprints: []agile.SprintResult{
		{Title: "Sprint 41", Scope: agile.Totals{Count: 3, Unweighted: 3}, Done: agile.Totals{Count: 2, Unweighted: 2}},
	}}
	m := agileModel(unicodeGlyphs)
	m.ag = agileUI{kind: agileReview, review: r}
	out := ansi.Strip(m.agileBody(120, 60))
	for _, want := range []string{"by count", "6 items", "2 of 3 items done"} {
		if !strings.Contains(out, want) {
			t.Errorf("count review lacks %q:\n%s", want, out)
		}
	}
}

func TestScrollIsClampedToTheContent(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	m.ag = agileUI{kind: agileStandup, standup: standupFixture(), scroll: 1 << 20}
	out := ansi.Strip(m.agileBody(100, 12))
	if !strings.Contains(out, "label workflow::blocked") {
		t.Errorf("scrolled to the end, the last lines must show:\n%s", out)
	}
	if lines := strings.Split(out, "\n"); len(lines) != 12 {
		t.Errorf("page is %d lines, want 12:\n%s", len(lines), out)
	}
	if !strings.Contains(out, "Yesterday") {
		t.Errorf("a clamped scroll shows the last page, not a blank one:\n%s", out)
	}
}

func press(m model, key string) (model, tea.Cmd) {
	next, cmd := m.key(keyPress(key))
	return next.(model), cmd
}

func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace}
	}
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func TestKeysOpenAndLeaveAgilePages(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	m.page = pageHome
	m, cmd := press(m, "S")
	if m.ag.kind != agileStandup || cmd == nil || !m.ag.loading {
		t.Fatalf("S: kind %d, cmd %v, loading %v", m.ag.kind, cmd != nil, m.ag.loading)
	}
	m, _ = press(m, "j")
	if m.ag.scroll != 1 {
		t.Fatalf("j scrolls: %d", m.ag.scroll)
	}
	m, _ = press(m, "V") // straight to the other page
	if m.ag.kind != agileReview {
		t.Fatalf("V from standup: %d", m.ag.kind)
	}
	m, _ = press(m, "esc")
	if m.isAgilePage() || m.page != pageHome {
		t.Fatalf("esc returns to the page we came from: kind %d page %d", m.ag.kind, m.page)
	}
	m.page = pageTrees
	m, _ = press(m, "V")
	m, _ = press(m, "t")
	if m.isAgilePage() || m.page != pageHome {
		t.Fatalf("t goes home from an agile page: kind %d page %d", m.ag.kind, m.page)
	}
}

func TestStaleAnswersAreDropped(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	m.ag = agileUI{kind: agileStandup, standupGen: 2, loading: true}
	stale := agile.Standup{Date: "old"}
	next, _ := m.agileUpdate(standupMsg{gen: 1, s: stale})
	if next.(model).ag.standup != nil || !next.(model).ag.loading {
		t.Fatal("an answer for an older request must be ignored")
	}
	fresh := agile.Standup{Date: "2026-11-09"}
	next, _ = m.agileUpdate(standupMsg{gen: 2, s: fresh})
	got := next.(model).ag
	if got.standup == nil || got.standup.Date != "2026-11-09" || got.loading {
		t.Fatalf("fresh answer not applied: %+v", got)
	}
	// A failed refresh keeps the page that was already showing.
	m.ag.standup = &fresh
	next, _ = m.agileUpdate(standupMsg{gen: 2, err: errors.New("offline")})
	got = next.(model).ag
	if got.standup == nil || got.standupErr == nil {
		t.Fatalf("a failed refresh must keep the old data and show the error: %+v", got)
	}
	out := ansi.Strip(next.(model).agileBody(100, 12))
	if !strings.Contains(out, "refresh failed: offline") || !strings.Contains(out, "Standup") {
		t.Errorf("stale page with error:\n%s", out)
	}
}

func TestCopyStandupSendsMarkdownOverOSC52(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	m.ag = agileUI{kind: agileStandup, standup: standupFixture()}
	m, cmd := press(m, "y")
	if cmd == nil {
		t.Fatal("y on a loaded page must copy")
	}
	raw, ok := cmd().(tea.RawMsg)
	if !ok {
		t.Fatalf("copy cmd answered %T, want a raw escape sequence", cmd())
	}
	seq, _ := raw.Msg.(string)
	const prefix = "\x1b]52;c;"
	if !strings.HasPrefix(seq, prefix) || !strings.HasSuffix(seq, "\x07") {
		t.Fatalf("sequence %q is not OSC 52", seq)
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(seq, prefix), "\x07"))
	if err != nil {
		t.Fatal(err)
	}
	if want := agile.RenderStandup(*standupFixture(), agile.Markdown); string(payload) != want {
		t.Fatalf("copied %q, want the Markdown standup", payload)
	}
	if len(m.nc.toasts) != 1 || !strings.Contains(m.nc.toasts[0].ev.Title, "copied the standup") {
		t.Fatalf("copy must say so: %+v", m.nc.toasts)
	}

	// Nothing loaded: nothing to copy.
	m.ag = agileUI{kind: agileStandup}
	if _, cmd := press(m, "y"); cmd != nil {
		t.Fatal("y with no standup must not send a clipboard request")
	}
}

func TestReviewKeysStepSprintsAndToggleMine(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	s41 := domain.Sprint{Key: "41", Title: "Sprint 41"}
	s42 := domain.Sprint{Key: "42", Title: "Sprint 42"}
	s43 := domain.Sprint{Key: "43", Title: "Sprint 43"}
	m.results[0].Sprints = []domain.Sprint{s41, s42, s43}
	m.ag = agileUI{kind: agileReview, review: &agile.Review{Project: "demo", Sprint: s42}}
	m, cmd := press(m, "[")
	if m.ag.reviewSpr != "Sprint 41" || cmd == nil {
		t.Fatalf("[ -> %q", m.ag.reviewSpr)
	}
	m, _ = press(m, "[") // already the oldest: stays
	if m.ag.reviewSpr != "Sprint 41" {
		t.Fatalf("[ at the oldest -> %q", m.ag.reviewSpr)
	}
	m, _ = press(m, "]")
	m, _ = press(m, "]")
	if m.ag.reviewSpr != "Sprint 43" {
		t.Fatalf("] ] -> %q", m.ag.reviewSpr)
	}
	m, _ = press(m, "m")
	if !m.ag.mine {
		t.Fatal("m must switch to your items")
	}
}

func TestNarrativeAnswerAndRefusal(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	m.ag = agileUI{kind: agileStandup, standup: standupFixture(), narrating: true}
	got := standupFixture()
	got.Narrative = "Friday: merged the rate limiter."
	next, _ := m.narrativeDone(narrativeMsg{kind: agileStandup, standup: got, res: ai.Result{CacheHit: true}})
	m = next.(model)
	if m.ag.narrating || m.ag.standup.Narrative == "" {
		t.Fatalf("narrative not applied: %+v", m.ag)
	}
	if out := ansi.Strip(m.agileBody(100, 12)); !strings.Contains(out, "Friday: merged the rate limiter.") {
		t.Errorf("page does not show the narrative:\n%s", out)
	}
	if last := m.nc.toasts[len(m.nc.toasts)-1].ev.Title; !strings.Contains(last, "cached, $0") {
		t.Errorf("toast = %q", last)
	}

	// A reload of the same day keeps what was bought.
	next, _ = m.agileUpdate(standupMsg{gen: m.ag.standupGen, s: *standupFixture()})
	if next.(model).ag.standup.Narrative == "" {
		t.Error("reload dropped the narrative")
	}

	// AI off: a notice, and the page is unchanged.
	m.ag.narrating = true
	next, _ = m.narrativeDone(narrativeMsg{kind: agileStandup, err: ai.ErrDisabled})
	m = next.(model)
	if last := m.nc.toasts[len(m.nc.toasts)-1].ev.Title; !strings.Contains(last, "ai.mode") {
		t.Errorf("toast = %q", last)
	}
}

func TestNarrateNeedsLoadedPage(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	m.ag = agileUI{kind: agileStandup}
	if _, cmd := press(m, "n"); cmd != nil {
		t.Fatal("n with nothing loaded must not call the AI")
	}
	m.ag = agileUI{kind: agileStandup, standup: standupFixture()}
	next, cmd := press(m, "n")
	if cmd == nil || !next.ag.narrating {
		t.Fatal("n on a loaded page starts a narrative")
	}
	if _, cmd := press(next, "n"); cmd != nil {
		t.Fatal("a second n while one is running must not start another")
	}
}

func TestLongErrorsWrapInsteadOfRunningOffTheScreen(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	long := errors.New("sprint issues: dial tcp 127.0.0.1:1: connect: connection refused while reading the sprint from the forge and then some more words")
	m.ag = agileUI{kind: agileReview, reviewErr: long}
	out := m.agileBody(60, 8)
	lineWidths(t, out, 60)
	if got := ansi.Strip(out); !strings.Contains(got, "connection refused") || !strings.Contains(got, "could not build this page") {
		t.Errorf("error lost its words:\n%s", got)
	}
}

func TestReviewNarrativeDoesNotOutliveTheFilter(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	old := reviewFixture()
	old.Narrative = "All mine."
	m.ag = agileUI{kind: agileReview, review: old, reviewGen: 1, loading: true}
	next, _ := m.agileUpdate(reviewMsg{gen: 1, r: agile.Review{Project: old.Project, Sprint: old.Sprint, Mine: true}})
	if got := next.(model).ag.review.Narrative; got != "" {
		t.Errorf("narrative %q kept after switching to your issues only", got)
	}
	m.ag.loading = true
	next, _ = m.agileUpdate(reviewMsg{gen: 1, r: agile.Review{Project: old.Project, Sprint: old.Sprint}})
	if got := next.(model).ag.review.Narrative; got != "All mine." {
		t.Errorf("a plain reload dropped the narrative: %q", got)
	}
}

func TestNarrowTodayKeepsWholeReasonAndNarrativeParagraphsSeparate(t *testing.T) {
	m := agileModel(unicodeGlyphs)
	s := standupFixture()
	s.Narrative = "First thing.\nSecond thing."
	m.ag = agileUI{kind: agileStandup, standup: s}
	out := ansi.Strip(m.agileBody(48, 40))
	lineWidths(t, out, 48)
	if !strings.Contains(out, "2 unresolved threads") {
		t.Errorf("narrow Today cut the reason:\n%s", out)
	}
	if !regexp.MustCompile(`First thing\.\s*\n\s+┃\s*\n\s+┃ Second thing\.`).MatchString(out) {
		t.Errorf("narrative paragraphs run together:\n%s", out)
	}
}
