package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/review"
)

// The MR page is driven the way the user drives it: key presses into the
// model, every command it returns run to completion against the fake GitLab,
// its answers fed back in. Assertions look at what the fake received.

const grp = fake.GroupPath

// settleTimeout is how long one command may take; the live test raises it: a
// real model answers in seconds and a rate-limited forge makes the client wait.
var settleTimeout = 10 * time.Second

func mrRef(proj string, iid int) forge.Ref {
	return forge.Ref{Project: grp + "/" + proj, IID: iid, Kind: forge.KindMR}
}

type mrEnv struct {
	t      *testing.T
	srv    *fake.Server
	api    *gitlab.Client
	m      model
	events <-chan events.Event
	log    *audit.Log
	store  *review.Store
}

func newMREnv(t *testing.T, tier fake.Tier) *mrEnv {
	t.Helper()
	srv := fake.New(tier)
	t.Cleanup(srv.Close)
	api := gitlab.New(srv.URL, fake.Token)
	ctx := context.Background()

	cfg := config.Defaults()
	a := &app.App{Paths: paths.Paths{CacheDir: t.TempDir()}, Config: &cfg, Bus: events.NewBus()}
	t.Cleanup(func() { a.Close() })
	log, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	a.Audit = log
	st, err := a.Cache()
	if err != nil {
		t.Fatal(err)
	}
	evs, cancel := a.Bus.Subscribe(64)
	t.Cleanup(cancel)

	me, err := api.Me(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Home lists each seeded MR as a review request, plus one work item whose
	// MR is api!1, so both ways into the page are exercised.
	var reviews []forge.MR
	for _, r := range []forge.Ref{mrRef("api", 1), mrRef("api", 2), mrRef("web", 1)} {
		mr, err := api.MR(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		reviews = append(reviews, mr)
	}
	tr := forge.Ref{Project: grp + "/tracker", IID: 1}
	item := domain.WorkItem{
		Project: "demo", Ref: tr, Issue: forge.Issue{Ref: tr, Title: "Rate-limit the export endpoint"},
		MRs: reviews[:1], Stage: domain.StageReview, Court: domain.CourtYou, Assigned: true, Repos: []string{"api"},
	}
	res := forgesync.Result{Project: "demo", Forge: "f", Tracker: grp + "/tracker", Me: me, Items: []domain.WorkItem{item}, Reviews: reviews}

	m := model{
		ctx: ctx, app: a, cache: st, g: unicodeGlyphs, w: 140, h: 44,
		nc:      newNotifCenter(notify.NewInbox(st)),
		reviews: review.NewStore(st), be: &mrBackends{},
		dial: func(context.Context, forge.Ref, string) (review.Backend, error) { return api, nil },
	}
	m.setResults([]forgesync.Result{res})
	m.homeLoaded = true
	return &mrEnv{t: t, srv: srv, api: api, m: m, events: evs, log: log, store: m.reviews}
}

// settle runs a command and everything it leads to until nothing is pending.
// Spinner and clock ticks are dropped: they only redraw.
func (e *mrEnv) settle(cmd tea.Cmd) {
	e.t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 500 {
			e.t.Fatal("commands never settled")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		done := make(chan tea.Msg, 1)
		go func() { done <- c() }()
		var msg tea.Msg
		select {
		case msg = <-done:
		case <-time.After(settleTimeout):
			e.t.Fatal("a command did not return")
		}
		switch msg := msg.(type) {
		case nil, mrSpinMsg, tickMsg:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		default:
			next, c2 := e.m.Update(msg)
			e.m = next.(model)
			queue = append(queue, c2)
		}
	}
}

func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "alt+enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	r := []rune(s)
	if len(r) != 1 {
		panic("keyMsg: unknown key " + s)
	}
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

// press sends keys one at a time, settling after each.
func (e *mrEnv) press(keys ...string) {
	e.t.Helper()
	for _, k := range keys {
		next, cmd := e.m.Update(keyMsg(k))
		e.m = next.(model)
		e.settle(cmd)
	}
}

func (e *mrEnv) typ(s string) {
	e.t.Helper()
	for _, r := range s {
		e.press(string(r))
	}
}

// open selects the Home row for ref and presses enter.
func (e *mrEnv) open(ref string) {
	e.t.Helper()
	for i, r := range e.m.homeRows {
		if r.ref() == ref {
			e.m.homeSel = i
			e.press("enter")
			if e.m.mr == nil {
				e.t.Fatalf("enter on %s did not open the page", ref)
			}
			return
		}
	}
	e.t.Fatalf("no Home row for %s", ref)
}

func (e *mrEnv) screen() string { return ansi.Strip(e.m.render()) }

func (e *mrEnv) want(s ...string) {
	e.t.Helper()
	out := e.screen()
	for _, w := range s {
		if !strings.Contains(out, w) {
			e.t.Errorf("screen lacks %q:\n%s", w, out)
		}
	}
}

func (e *mrEnv) wantNot(s ...string) {
	e.t.Helper()
	out := e.screen()
	for _, w := range s {
		if strings.Contains(out, w) {
			e.t.Errorf("screen has %q:\n%s", w, out)
		}
	}
}

func (e *mrEnv) discussions(ref forge.Ref) []forge.Discussion {
	e.t.Helper()
	ds, err := e.api.Discussions(context.Background(), ref)
	if err != nil {
		e.t.Fatal(err)
	}
	return ds
}

// drainEvents returns what the page published on the bus so far.
func (e *mrEnv) drainEvents() []events.Event {
	var out []events.Event
	for {
		select {
		case ev := <-e.events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func (e *mrEnv) audited(action string) []audit.Entry {
	e.t.Helper()
	es, err := e.log.Query(audit.Filter{Action: action})
	if err != nil {
		e.t.Fatal(err)
	}
	return es
}

func (e *mrEnv) requests(suffix string) int {
	n := 0
	for _, r := range e.srv.Requests() {
		if strings.HasSuffix(r.Path, suffix) {
			n++
		}
	}
	return n
}

// --- opening ------------------------------------------------------------------------

// Enter on an item opens its MR; the diff is read only when its tab is shown,
// and an MR already read is not fetched again on reopening.
func TestPageLoadsLazilyAndCaches(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.open(grp + "/tracker#1")
	e.want("#1 › api!1", "Rate-limit", "[Overview]", "Work item", "Merge request")
	if n := e.requests("/diffs"); n != 0 {
		t.Fatalf("the diff was read on open (%d requests)", n)
	}
	e.press("3")
	e.want("[Diff 1]", "limit/bucket.go")
	if n := e.requests("/diffs"); n != 1 {
		t.Fatalf("diff requests = %d, want 1", n)
	}
	e.press("esc", "esc") // diff pane has no focus: first esc leaves the page
	if e.m.mr != nil {
		t.Fatal("esc did not return to Home")
	}
	e.open(grp + "/tracker#1")
	e.press("3")
	if n := e.requests("/diffs"); n != 1 {
		t.Errorf("reopening re-read the diff (%d requests)", n)
	}
}

// An item with no MR still opens: Overview shows its evidence, the other
// tabs say why they are empty.
func TestItemWithoutMRShowsEvidenceOnly(t *testing.T) {
	e := newMREnv(t, fake.Free)
	res := e.m.results[0]
	res.Items[0].MRs = nil
	e.m.setResults([]forgesync.Result{res})
	e.open(grp + "/tracker#1")
	e.want("Work item", "No merge request yet")
	e.press("3")
	e.want("no merge request yet")
	e.press("a", "m", "S") // writes need an MR
	if e.m.mr.modal != nil {
		t.Error("a dialog opened on an item with no MR")
	}
}

// --- threads ----------------------------------------------------------------------

func TestResolveAndReopenThread(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 1)
	count := func() (open, done int) {
		for _, d := range e.discussions(ref) {
			switch {
			case d.Resolvable && d.Resolved:
				done++
			case d.Resolvable:
				open++
			}
		}
		return
	}
	if o, d := count(); o != 2 || d != 1 {
		t.Fatalf("seed has %d open, %d resolved; the test assumes 2 and 1", o, d)
	}
	e.open(ref.String())
	e.press("2")
	e.want("Unresolved (2)", "Resolved (1)", "[Threads 2]")

	e.press("x")
	if o, d := count(); o != 1 || d != 2 {
		t.Errorf("after x: %d open, %d resolved; want 1 and 2", o, d)
	}
	e.want("Unresolved (1)", "Resolved (2)", "1 unresolved")

	e.press("G", "x") // the last row is a resolved thread: reopen it
	if o, d := count(); o != 2 || d != 1 {
		t.Errorf("after reopening: %d open, %d resolved; want 2 and 1", o, d)
	}
	if got := len(e.audited("forge.thread.resolve")); got != 2 {
		t.Errorf("audit has %d forge.thread.resolve entries, want 2", got)
	}
	var kinds []string
	for _, ev := range e.drainEvents() {
		kinds = append(kinds, ev.Kind)
	}
	if strings.Join(kinds, " ") != "mr.resolve mr.resolve" {
		t.Errorf("events = %v", kinds)
	}
}

func TestReplyToThread(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 2)
	e.open(ref.String())
	e.press("2", "r")
	e.want("Reply · ")
	e.typ("Lazily, on Allow.")
	e.press("enter")
	var found bool
	for _, d := range e.discussions(ref) {
		for _, n := range d.Notes {
			found = found || n.Body == "Lazily, on Allow."
		}
	}
	if !found {
		t.Fatal("the reply is not on the forge")
	}
	e.want("reviewer-rae", "Lazily, on Allow.")
}

// A reply with no text is refused inside the dialog, not sent.
func TestEmptyReplyIsRefused(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.open(mrRef("api", 2).String())
	e.press("2", "r", "enter")
	e.want("write a reply first")
	if len(e.audited("forge.thread.reply")) != 0 {
		t.Error("an empty reply was sent")
	}
}

// --- approve, ready, merge, CI -----------------------------------------------

func TestApproveToggleAndReadyToggle(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("web", 1) // a draft
	e.open(ref.String())
	e.want("Draft")
	e.press("a")
	if as, _ := e.srv.Approvers(ref); len(as) != 1 {
		t.Fatalf("approvers = %v", as)
	}
	e.want("1 approval")
	e.press("a")
	if as, _ := e.srv.Approvers(ref); len(as) != 0 {
		t.Fatalf("approval was not withdrawn: %v", as)
	}
	e.press("R")
	e.want("Ready")
	if title, _ := e.srv.Title(ref); strings.HasPrefix(title, "Draft:") {
		t.Errorf("still a draft on the forge: %q", title)
	}
	e.press("R")
	if title, _ := e.srv.Title(ref); !strings.HasPrefix(title, "Draft:") {
		t.Errorf("not back to draft on the forge: %q", title)
	}
}

func TestMergeConfirmPath(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 1)
	e.open(ref.String())
	e.press("m")
	e.want("Merge · "+ref.String(), "mergeable", "squash commits", "delete the source branch")
	if merged, _, _, _ := e.srv.MergeState(ref); merged {
		t.Fatal("merged before the confirmation")
	}
	e.press("s", "d") // squash and delete the branch
	e.want("[x] s  squash commits", "[x] d  delete the source branch", "[ ] w  merge when")
	e.press("enter")
	merged, _, params, err := e.srv.MergeState(ref)
	if err != nil || !merged {
		t.Fatalf("merged = %v, %v", merged, err)
	}
	if params["squash"] != true || params["should_remove_source_branch"] != true || params["sha"] == "" {
		t.Errorf("merge parameters = %v", params)
	}
	e.want("merged")
	if es := e.audited("forge.mr.merge"); len(es) != 1 || es[0].Outcome != "ok" {
		t.Errorf("merge audit = %+v", es)
	}
	var merged1 bool
	for _, ev := range e.drainEvents() {
		merged1 = merged1 || (ev.Kind == "mr.merge" && ev.Severity == events.Success && strings.Contains(ev.Title, "Merged "+ref.String()))
	}
	if !merged1 {
		t.Error("no success pop-up for the merge")
	}
}

// A draft MR and one needing approvals are refused in the dialog; nothing is
// sent, and escape leaves the page as it was.
func TestMergeRefusals(t *testing.T) {
	t.Run("draft", func(t *testing.T) {
		e := newMREnv(t, fake.Free)
		ref := mrRef("web", 1)
		e.open(ref.String())
		e.press("m")
		e.want("cannot merge: it is still a draft")
		e.wantNot("squash commits")
		e.press("enter")
		e.want("cannot merge")
		if merged, _, _, _ := e.srv.MergeState(ref); merged {
			t.Fatal("a draft was merged")
		}
		if len(e.audited("forge.mr.merge")) != 0 {
			t.Error("a refused merge reached the forge")
		}
		e.press("esc")
		if e.m.mr.modal != nil {
			t.Error("esc did not close the dialog")
		}
	})
	t.Run("needs approval (Premium)", func(t *testing.T) {
		e := newMREnv(t, fake.Premium)
		e.open(mrRef("api", 1).String())
		e.press("m")
		e.want("cannot merge: it needs approval", "1 of 2 approvals")
	})
}

func TestRetryPipelineAndJobLog(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("web", 1) // lint failed
	e.open(ref.String())
	e.press("5")
	e.want("[CI ", "build", "lint", "test", "failed", "1 failed job")
	e.press("j", "l")
	e.want("Job log · lint", "eslint: 2 errors")
	e.press("esc", "r")
	if n, _ := e.srv.Retries(ref); n != 1 {
		t.Errorf("retries = %d", n)
	}
	if got := e.drainEvents(); len(got) == 0 || got[len(got)-1].Kind != "ci.retry" {
		t.Errorf("events = %+v", got)
	}
	if len(e.audited("forge.pipeline.retry")) != 1 {
		t.Error("retry not audited")
	}
}

// --- review ---------------------------------------------------------------------

// Two line comments and a summary go out as ONE publish; the comment on the
// renamed file lands on the right line of the new path; nothing is left
// locally; the author-visible result is one pop-up.
func TestTwoDraftsOneSubmit(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 2) // limit/bucket.go -> limit/token_bucket.go
	e.open(ref.String())
	e.press("3")
	e.want("limit/{bucket.go → token_bucket.go}", "R", "renamed")
	e.press("enter") // into the diff pane; the cursor is on line 1

	e.press("c")
	e.want("Comment · limit/token_bucket.go:1", "[comment]")
	e.typ("Doc the package.")
	e.press("tab") // severity: blocking
	e.want("[blocking]")
	e.press("enter")
	e.want("your draft", "Doc the package.")

	e.press("j", "j", "j") // the added doc line
	e.press("c")
	e.typ("Says continuously; Allow checks Tokens only.")
	e.press("enter")
	if ds, _ := e.store.List(ref); len(ds) != 2 {
		t.Fatalf("saved drafts = %d, want 2", len(ds))
	}
	if n, _ := e.srv.PendingDrafts(ref); n != 0 {
		t.Fatalf("drafts reached the forge before submit (%d)", n)
	}
	if got := len(e.discussions(ref)); got != 1 {
		t.Fatalf("discussions before = %d", got)
	}

	e.press("S")
	e.want("Submit review · "+ref.String(), "2 comments go out as one batch")
	e.typ("Two small things.")
	e.press("enter")

	if n, err := e.srv.Publishes(ref); err != nil || n != 1 {
		t.Fatalf("publishes = %d, %v; want exactly one", n, err)
	}
	if n, _ := e.srv.PendingDrafts(ref); n != 0 {
		t.Errorf("%d draft notes left unpublished", n)
	}
	ds := e.discussions(ref)
	if len(ds) != 4 { // the seeded thread, two line comments, the summary
		t.Fatalf("discussions after = %d, want 4", len(ds))
	}
	var positions int
	for _, d := range ds {
		body := d.Notes[0].Body
		switch {
		case strings.Contains(body, "Doc the package."):
			if !strings.HasPrefix(body, "**blocking:** ") {
				t.Errorf("severity lost: %q", body)
			}
			p := d.Position
			if p == nil || p.Path != "limit/token_bucket.go" || p.NewLine != 1 {
				t.Errorf("first comment position = %+v", p)
			}
			positions++
		case strings.Contains(body, "Allow checks Tokens"):
			p := d.Position
			if p == nil || p.Path != "limit/token_bucket.go" || p.NewLine != 3 {
				t.Errorf("second comment position = %+v, want new line 3 of the renamed file", p)
			}
			positions++
		case strings.Contains(body, "Two small things."):
			if d.Position != nil {
				t.Errorf("the summary should be an overview note, got %+v", d.Position)
			}
		}
	}
	if positions != 2 {
		t.Errorf("found %d positioned comments, want 2", positions)
	}
	if ds, _ := e.store.List(ref); len(ds) != 0 {
		t.Errorf("%d drafts kept after the submit", len(ds))
	}
	if es := e.audited("review.submit"); len(es) != 1 || es[0].Outcome != "ok" {
		t.Errorf("review.submit audit = %+v", es)
	}
	if es := e.audited("forge.draftnote.publish"); len(es) != 1 {
		t.Errorf("publish audited %d times, want once", len(es))
	}
	var popup *events.Event
	for _, ev := range e.drainEvents() {
		if ev.Kind == "review.submitted" {
			popup = &ev
		}
	}
	if popup == nil || popup.Severity != events.Success || !strings.Contains(popup.Title, "3 comments in one batch") {
		t.Errorf("result pop-up = %+v", popup)
	}
	e.want("4 unresolved")
}

// Submit with nothing to send, no summary and no approval is refused in the
// dialog; the forge is untouched.
func TestSubmitNothingIsRefused(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 2)
	e.open(ref.String())
	e.press("S")
	e.want("0 comments go out")
	e.press("enter")
	e.want("nothing to submit")
	if n, _ := e.srv.Publishes(ref); n != 0 {
		t.Errorf("publishes = %d", n)
	}
	e.press("esc")
	if e.m.mr.modal != nil {
		t.Error("dialog still open")
	}
}

// Approve together with the submit: the review and the approval both land.
func TestSubmitWithApproval(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 2)
	e.open(ref.String())
	e.press("S")
	e.typ("LGTM")
	e.press("tab")
	e.want("[x] also approve")
	e.press("enter")
	if as, _ := e.srv.Approvers(ref); len(as) != 1 {
		t.Errorf("approvers = %v", as)
	}
	if n, _ := e.srv.Publishes(ref); n != 1 {
		t.Errorf("publishes = %d", n)
	}
}

// An agent's drafts wait for triage. Accepted ones go out, a denied one is
// never published (and never comes back), and one left pending stays a draft.
func TestDeniedAgentDraftIsNeverPublished(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 2)
	for i, c := range []struct {
		line int
		body string
	}{{1, "agent: accepted point"}, {3, "agent: denied point"}, {5, "agent: undecided point"}} {
		d, err := review.Normalize(review.Draft{
			ID: string(rune('a' + i)), Path: "limit/token_bucket.go", Line: c.line, Body: c.body,
			Source: review.SourceAgent, Severity: review.SeveritySuggestion,
		}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := e.store.Put(ref, d); err != nil {
			t.Fatal(err)
		}
	}
	e.open(ref.String())
	e.want("Review 3")
	e.press("4")
	e.want("agent · pending", "3 drafts", "3 agent pending")

	e.press("y") // accepts the first; the next pending draft takes its place in the list
	e.press("n") // deny that one, with a reason
	e.want("Deny draft")
	e.typ("wrong")
	e.press("enter")
	e.want("agent · denied", "agent · accepted", "agent · pending")

	e.press("S")
	e.want("1 comment go", "1 agent draft still pending", "1 denied")
	e.press("enter")

	if n, _ := e.srv.Publishes(ref); n != 1 {
		t.Fatalf("publishes = %d", n)
	}
	var posted []string
	for _, d := range e.discussions(ref) {
		for _, n := range d.Notes {
			posted = append(posted, n.Body)
		}
	}
	all := strings.Join(posted, "\n")
	if !strings.Contains(all, "agent: accepted point") {
		t.Errorf("accepted draft not posted: %q", posted)
	}
	for _, never := range []string{"agent: denied point", "agent: undecided point"} {
		if strings.Contains(all, never) {
			t.Errorf("%q was posted: %q", never, posted)
		}
	}
	left, _ := e.store.List(ref)
	if len(left) != 1 || left[0].Body != "agent: undecided point" {
		t.Errorf("drafts kept = %+v; only the undecided one should remain", left)
	}
	acc, den := e.audited("review.comment.accept"), e.audited("review.comment.deny")
	if len(acc) != 1 || len(den) != 1 || den[0].Params["reason"] != "wrong" {
		t.Errorf("triage audit: accept=%+v deny=%+v", acc, den)
	}
}

// Accept-all takes every pending agent draft, and editing one accepts it with
// the new text and severity.
func TestAcceptAllAndEdit(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 2)
	for i, line := range []int{1, 3} {
		d, _ := review.Normalize(review.Draft{
			ID: string(rune('a' + i)), Path: "limit/token_bucket.go", Line: line, Body: "agent says " + string(rune('a'+i)),
			Source: review.SourceAgent,
		}, time.Now())
		if err := e.store.Put(ref, d); err != nil {
			t.Fatal(err)
		}
	}
	e.open(ref.String())
	e.press("4", "e") // edit the first pending draft
	e.want("Edit comment")
	e.typ(", edited")
	e.press("tab", "tab", "enter") // severity: suggestion
	ds, _ := e.store.List(ref)
	if ds[0].Body != "agent says a, edited" || ds[0].State != review.StateAccepted || ds[0].Severity != review.SeveritySuggestion {
		t.Errorf("edited draft = %+v", ds[0])
	}
	e.press("Y")
	ds, _ = e.store.List(ref)
	for _, d := range ds {
		if d.State != review.StateAccepted {
			t.Errorf("draft %s is %s after accept-all", d.ID, d.State)
		}
	}
	if got := len(e.audited("review.comment.edit")); got != 1 {
		t.Errorf("edit audited %d times", got)
	}
	if got := len(e.audited("review.comment.accept")); got != 1 {
		t.Errorf("accept audited %d times (the edited one is not a second accept)", got)
	}
}

// A comment on a line outside the diff is impossible from the page (the cursor
// only visits diff lines), and a removed line is anchored on the old side.
func TestCommentOnRemovedLineUsesOldSide(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 2)
	e.open(ref.String())
	e.press("3", "enter", "j", "j") // line 3 of the old file: "-// Bucket is ..."
	e.press("c")
	e.want("(removed line)")
	e.typ("Why drop this?")
	e.press("enter")
	ds, _ := e.store.List(ref)
	if len(ds) != 1 || ds[0].Side != review.SideOld || ds[0].Line != 3 || ds[0].OldPath != "limit/bucket.go" {
		t.Fatalf("draft = %+v", ds)
	}
	e.press("S", "enter")
	for _, d := range e.discussions(ref) {
		if strings.Contains(d.Notes[0].Body, "Why drop this?") {
			if p := d.Position; p == nil || p.OldLine != 3 || p.NewLine != 0 {
				t.Errorf("position = %+v, want the removed line (old 3)", p)
			}
			return
		}
	}
	t.Error("comment not on the forge")
}

// Drafts survive quitting the page and the process: they are in the store, and
// shown again on reopening.
func TestDraftsPersistAcrossReopen(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 2)
	e.open(ref.String())
	e.press("3", "enter", "c")
	e.typ("keep me")
	e.press("enter", "esc", "esc")
	if e.m.mr != nil {
		t.Fatal("still on the page")
	}
	e.m.mrCache = nil // a new process: nothing in memory
	e.open(ref.String())
	e.want("Review 1")
	e.press("4")
	e.want("limit/token_bucket.go:1", "keep me")
}

// A forge error is reported in a pop-up and the audit log, the drafts are kept,
// and the next submit can go ahead.
func TestFailedSubmitKeepsDrafts(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 2)
	e.open(ref.String())
	e.press("3", "enter", "c")
	e.typ("one")
	e.press("enter")
	e.m.dial = func(context.Context, forge.Ref, string) (review.Backend, error) { return failingBackend{e.api}, nil }
	e.m.be = &mrBackends{}
	e.press("S", "enter")
	var failed bool
	for _, ev := range e.drainEvents() {
		failed = failed || (ev.Kind == "review.submitted.failed" && ev.Severity == events.Error)
	}
	if !failed {
		t.Error("no failure pop-up")
	}
	if ds, _ := e.store.List(ref); len(ds) != 1 {
		t.Errorf("drafts after a failed submit = %d, want 1", len(ds))
	}
	if es := e.audited("review.submit"); len(es) != 1 || es[0].Outcome != "error" {
		t.Errorf("audit = %+v", es)
	}
	e.m.dial = func(context.Context, forge.Ref, string) (review.Backend, error) { return e.api, nil }
	e.m.be = &mrBackends{}
	e.press("S", "enter")
	if n, _ := e.srv.Publishes(ref); n != 1 {
		t.Errorf("publishes after retry = %d", n)
	}
}

// failingBackend refuses to publish.
type failingBackend struct{ review.Backend }

func (failingBackend) PublishDraftNotes(context.Context, forge.Ref) error {
	return forge.ErrAuth
}

// History lists what ADE did to this MR, newest first, and picks up a write
// made while the tab is open.
func TestHistoryTab(t *testing.T) {
	e := newMREnv(t, fake.Free)
	ref := mrRef("api", 1)
	e.open(ref.String())
	e.press("6")
	e.want("[History]", "No recorded actions on this item yet.")
	e.press("2", "x", "6")
	e.want("forge thread resolve", "1 recorded action")
	e.press("a") // a write while History is showing is listed once it lands
	e.want("forge approve", "2 recorded actions")
}

// An item with no MR still has a History: the item's own entries.
func TestHistoryOfItemWithoutMR(t *testing.T) {
	e := newMREnv(t, fake.Free)
	res := e.m.results[0]
	res.Items[0].MRs = nil
	e.m.setResults([]forgesync.Result{res})
	e.open(grp + "/tracker#1")
	e.press("6")
	e.want("[History]", "No recorded actions")
}
