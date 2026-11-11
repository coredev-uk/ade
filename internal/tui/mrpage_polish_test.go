package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/review"
)

// Every dialog on the page is a blank field at the top: no fragment of the
// tab underneath shows beside or below it, at desktop and narrow widths.
func TestDialogsReplaceTheBody(t *testing.T) {
	for _, w := range []int{140, 80} {
		e := newMREnv(t, fake.Premium)
		e.m.w, e.m.h = w, 44
		e.open(mrRef("api", 1).String())
		e.want("Description", "Merge request") // the Overview body, before any dialog

		check := func(name, title string) {
			t.Helper()
			e.want(title)
			e.wantNot("Description", "Work item", "Merge request  ")
			lines := strings.Split(e.screen(), "\n")
			at := -1
			for i, ln := range lines {
				if strings.Contains(ln, title) {
					at = i
					break
				}
			}
			if at < 0 || at > 4 {
				t.Errorf("w=%d %s: dialog title on row %d, want it anchored at the top:\n%s", w, name, at, strings.Join(lines, "\n"))
			}
			// The field is blank outside the dialog's own frame.
			for i, ln := range lines[2 : len(lines)-1] {
				if i > 40 {
					break
				}
				if strings.Contains(ln, "│") && !strings.HasPrefix(strings.TrimLeft(ln, " "), "│") && !strings.HasPrefix(strings.TrimLeft(ln, " "), "╭") {
					t.Errorf("w=%d %s: base-layer text beside the dialog: %q", w, name, ln)
				}
			}
		}
		e.press("m")
		check("merge", "Merge · ")
		e.press("esc")
		e.press("S")
		check("submit", "Submit review")
		e.press("esc")
		e.press("?")
		check("help", "Merge request page")
		e.press("esc")
		e.press("5", "j", "l")
		check("job log", "Job log")
	}
}

// A forge outage fails the MR, threads and diff reads together: the user gets
// one toast naming the problem and the recovery, without the event id or the
// request URL; the raw error stays in the audit log.
func TestOutageRaisesOneToast(t *testing.T) {
	e := newMREnv(t, fake.Free)
	raw := errors.New(`gitlab GET /projects/x%2Fapi/merge_requests/1: Get "http://127.0.0.1:9/api/v4/projects": dial tcp 127.0.0.1:9: connect: connection refused`)
	e.m.dial = func(context.Context, forge.Ref, string) (review.Backend, error) { return nil, raw }
	e.open(mrRef("api", 1).String())
	e.press("3") // the diff fails too
	var failed []events.Event
	for _, ev := range e.drainEvents() {
		if ev.Kind == "mr.load.failed" {
			failed = append(failed, ev)
		}
	}
	if len(failed) != 1 {
		t.Fatalf("one outage raised %d toasts, want 1: %+v", len(failed), failed)
	}
	if got, want := failed[0].Title, "Could not load api!1 · U retries"; got != want {
		t.Errorf("toast = %q, want %q", got, want)
	}
	if strings.Contains(failed[0].Title, "127.0.0.1") {
		t.Errorf("the toast shows the request URL: %q", failed[0].Title)
	}
	e.want("could not read the diff: the forge is unreachable")
	e.wantNot("127.0.0.1", "gitlab GET", "mr.load.failed")
	if es := e.audited("mr.load"); len(es) < 3 || !strings.Contains(es[0].Error, "127.0.0.1") {
		t.Errorf("the raw errors belong in the audit log: %+v", es)
	}
	e.settle(e.m.mrReloadAll()) // U: retrying and failing again is news
	n := 0
	for _, ev := range e.drainEvents() {
		if ev.Kind == "mr.load.failed" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("a failed retry raised %d toasts, want 1", n)
	}
}

func TestShortCause(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{errors.New(`gitlab GET /projects/x: Get "http://127.0.0.1:9/api/v4/x": dial tcp 127.0.0.1:9: connect: connection refused`), "the forge is unreachable"},
		{syscall.ECONNREFUSED, "the forge is unreachable"},
		{fmt.Errorf("gitlab GET /projects/x/merge_requests/1: 401: %w", forge.ErrAuth), "the forge rejected the token"},
		{errors.New("gitlab GET /projects/x: 503 Service Unavailable"), "the forge reported an error"},
		{errors.New("gitlab GET /projects/x: 429 Retry later"), "the forge is rate limiting"},
		{errors.New("gitlab POST https://gitlab.example/api/v4/x"), "the forge request failed"},
		{fmt.Errorf("gitlab GET /projects/x: %w", context.DeadlineExceeded), "the forge did not answer in time"},
		{errors.New("saved review drafts: disk full"), "saved review drafts: disk full"},
	} {
		if got := shortCause(c.err); got != c.want {
			t.Errorf("shortCause(%q) = %q, want %q", c.err, got, c.want)
		}
	}
}

// The toast names what it is about and how to dismiss it, never the dotted id.
func TestToastShowsNoEventID(t *testing.T) {
	ev := events.New("mr.load.failed", events.Error, "Could not load api!1 · U retries")
	ev.Subject = "ade-demo-env/api!1"
	box := ansi.Strip(toastBox(unicodeGlyphs, toast{ev: ev}, 60))
	if strings.Contains(box, "mr.load.failed") {
		t.Errorf("toast shows the internal event id:\n%s", box)
	}
	if !strings.Contains(box, "Could not load api!1 · U retries") || !strings.Contains(box, "ade-demo-env/api!1") || !strings.Contains(box, "esc dismiss") {
		t.Errorf("toast lacks its title, subject or dismiss hint:\n%s", box)
	}
}

// A blocking draft carries the ▲ in the diff as it does in the Review list.
func TestBlockingDraftHeadCarriesGlyph(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 1)
	d, err := review.Normalize(review.Draft{Path: "limit/bucket.go", Line: 5, Body: "Name the unit.", Severity: review.SeverityBlocking}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.Put(ref, d); err != nil {
		t.Fatal(err)
	}
	e.open(ref.String())
	e.press("3", "enter")
	e.want("your draft · ▲ blocking", "Name the unit.")
	e.press("4")
	e.want("▲ blocking")
}

// The renamed file keeps its new name and its draft count when the list column
// is tight: the directory and the old name go first.
func TestFileLabelKeepsTheNewName(t *testing.T) {
	f := forge.DiffFile{OldPath: "limit/bucket.go", NewPath: "limit/token_bucket.go"}
	for n, want := range map[int]string{
		40: "limit/{bucket.go → token_bucket.go}",
		29: "{bucket.go → token_bucket.go}",
		28: "bucket.go → token_bucket.go",
		20: "→ token_bucket.go",
		16: "token_bucket.go",
	} {
		if got := fileLabel(f, n); got != want {
			t.Errorf("fileLabel(rename, %d) = %q, want %q", n, got, want)
		}
	}
	if got := fileLabel(f, 10); lipgloss.Width(got) > 10 || !strings.HasSuffix(got, "ket.go") || !strings.HasPrefix(got, "…") {
		t.Errorf("fileLabel(rename, 10) = %q: want the name's tail behind an ellipsis", got)
	}
	moved := forge.DiffFile{OldPath: "a/old/name.go", NewPath: "b/new/token_bucket.go", Renamed: true}
	if got := fileLabel(moved, 24); !strings.HasSuffix(got, "token_bucket.go") {
		t.Errorf("fileLabel(moved, 24) = %q, want the new basename kept", got)
	}
	plain := forge.DiffFile{OldPath: "internal/very/deep/limit/bucket.go", NewPath: "internal/very/deep/limit/bucket.go"}
	if got := fileLabel(plain, 16); lipgloss.Width(got) > 16 || !strings.HasSuffix(got, "limit/bucket.go") && !strings.HasSuffix(got, "bucket.go") {
		t.Errorf("fileLabel(plain, 16) = %q, want the directory cut from the left", got)
	}

	// In the file list, the draft count survives a narrow column.
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 2)
	d, _ := review.Normalize(review.Draft{Path: "limit/token_bucket.go", Line: 1, Body: "x"}, time.Now())
	if err := e.store.Put(ref, d); err != nil {
		t.Fatal(err)
	}
	e.open(ref.String())
	e.press("3")
	list := ansi.Strip(e.m.fileLines(0, 30, 3))
	if first := strings.Split(list, "\n")[0]; !strings.Contains(first, "token_bucket.go") || !strings.Contains(first, "✎1") {
		t.Errorf("file row in 30 cells = %q, want the new name and ✎1", first)
	}
}

// The Review list's right column ends on the section rule's right edge.
func TestReviewColumnAlignsWithRule(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 1)
	long := strings.Repeat("word ", 70)
	for i, c := range []review.Draft{
		{Path: "limit/bucket.go", Line: 5, Body: "mine", Severity: review.SeverityBlocking},
		{Path: "limit/bucket.go", Line: 6, Body: long, Source: review.SourceAgent},
		{Path: "limit/bucket.go", Line: 3, Body: "no", Source: review.SourceAgent, State: review.StateDenied},
	} {
		d, err := review.Normalize(c, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		d.ID = string(rune('a' + i))
		if err := e.store.Put(ref, d); err != nil {
			t.Fatal(err)
		}
	}
	e.open(ref.String())
	e.press("4")
	for _, w := range []int{140, 80} {
		rule := lipgloss.Width(e.m.mrRule(w))
		for _, dr := range triageOrder(e.m.mr.data.drafts) {
			for _, sel := range []bool{true, false} {
				row := e.m.draftRow(dr, sel, w)
				if got := lipgloss.Width(row); got != rule {
					t.Errorf("w=%d %s/%s: row is %d cells, rule %d: %q", w, dr.Source, dr.State, got, rule, ansi.Strip(row))
				}
				plain := ansi.Strip(row)
				if !strings.HasSuffix(plain, "agent · pending") && !strings.HasSuffix(plain, "agent · denied") && !strings.HasSuffix(plain, "you · accepted") {
					t.Errorf("w=%d: row does not end with its who column: %q", w, plain)
				}
			}
		}
	}
	e.want("agent · pending", "long")
}

// The first read of a merge request says so, apart from a refresh.
func TestFirstReadIsNotARefresh(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 1).String()
	for i, r := range e.m.homeRows {
		if r.ref() == ref {
			e.m.homeSel = i
		}
	}
	cmd := e.m.openHomeRow() // the reads are in flight, not answered
	e.want("reading from the forge")
	e.wantNot("refreshing")
	e.settle(cmd)
	e.wantNot("reading from the forge")
	cmd = e.m.mrReloadAll() // U: the data is in hand, this is a refresh
	e.want("refreshing")
	e.wantNot("reading from the forge")
	e.settle(cmd)
}

// The reply dialog shows which thread is being answered.
func TestReplyDialogQuotesTheThread(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.open(mrRef("api", 1).String())
	e.press("2", "r")
	e.want("Reply · ", `general · reviewer-rae: "Is a burst of 50 too high`)
}

// A narrow draft head never cuts the severity or `long`: the triage keys take
// their own line instead.
func TestAgentDraftHeadReflowsWhenNarrow(t *testing.T) {
	e := newMREnv(t, fake.Free)
	dr, err := review.Normalize(review.Draft{
		Path: "limit/bucket.go", Line: 5, Body: strings.Repeat("word ", 70), Source: review.SourceAgent, Severity: review.SeverityBlocking,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		w     int
		lines int // rows of head before the body
	}{{120, 1}, {82, 1}, {44, 3}} {
		rows := e.m.draftBlock(dr, "", c.w)
		head := ansi.Strip(strings.Join(rows[:c.lines], "\n"))
		for _, want := range []string{"agent draft · pending triage", "▲ blocking", "· long", "y accept · n deny · e edit"} {
			if !strings.Contains(head, want) {
				t.Errorf("w=%d: head lacks %q: %q", c.w, want, head)
			}
		}
		if strings.Contains(head, "…") {
			t.Errorf("w=%d: head is cut: %q", c.w, head)
		}
		if next := ansi.Strip(rows[c.lines]); !strings.Contains(next, "word") {
			t.Errorf("w=%d: row %d should open the body, got %q", c.w, c.lines, next)
		}
	}
}

// Draft bodies wrap at a reading measure, not the width of a wide pane.
func TestDraftBodyKeepsAReadingMeasure(t *testing.T) {
	e := newMREnv(t, fake.Free)
	dr, _ := review.Normalize(review.Draft{Path: "limit/bucket.go", Line: 5, Body: strings.Repeat("word ", 90)}, time.Now())
	for _, ln := range e.m.draftBlock(dr, "", 200)[1:] {
		if w := lipgloss.Width(ln); w > 2+100 {
			t.Errorf("body row is %d cells wide, want at most 102 with its bar: %q", w, ansi.Strip(ln))
		}
	}
}

// The diff pane opens on a code line, never on the tail of a draft block.
func TestDiffViewportOpensOnACodeLine(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 1)
	for i, line := range []int{3, 4, 5, 7} {
		d, err := review.Normalize(review.Draft{
			ID: string(rune('a' + i)), Path: "limit/bucket.go", Line: line, Source: review.SourceAgent, Severity: review.SeveritySuggestion,
			Body: "A body long enough to wrap onto several rows in a narrow pane, so the block spans the window edge. " + strings.Repeat("more words ", 6),
		}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := e.store.Put(ref, d); err != nil {
			t.Fatal(err)
		}
	}
	e.m.w, e.m.h = 84, 30
	e.open(ref.String())
	e.press("3", "enter")
	f := e.m.mr.data.diff.Files[0]
	for cur := 0; cur < 7; cur++ {
		e.m.mr.lineCur = cur
		for h := 6; h <= 16; h++ {
			pane := strings.Split(ansi.Strip(e.m.codePane(f, 82, h)), "\n")
			if first := strings.TrimSpace(pane[1]); strings.HasPrefix(first, "│") {
				t.Errorf("cursor %d, height %d: the view opens inside a draft block: %q", cur, h, pane[1])
			}
		}
	}
}

// Until the jobs are read the CI rule makes no claim about how many there are.
func TestCIGuideDoesNotCountUnreadJobs(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.open(mrRef("api", 1).String())
	e.m.mr.tab = tabCI
	e.m.mr.data.jobsL = load{st: loadBusy}
	rule := ansi.Strip(e.m.mrRule(120))
	if !strings.Contains(rule, "pipeline #") || strings.Contains(rule, "jobs") {
		t.Errorf("rule while the jobs load = %q", rule)
	}
	e.m.mr.data.jobsL = load{st: loadOK}
	e.m.mr.data.jobs = nil
	if rule := ansi.Strip(e.m.mrRule(120)); !strings.Contains(rule, "0 jobs") {
		t.Errorf("rule with the jobs read = %q, want the count", rule)
	}
}
