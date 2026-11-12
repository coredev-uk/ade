package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/budget"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// AI on the MR page (docs/PLAN.md 6.2, 6.9). Every call goes through
// ai.Service, exactly like `ade ai summarize|draft-reply|review`: result cache,
// pre-flight against the caps, the audit entry and the artifact in the item's
// record. Calls run off the UI loop, so the page never waits on a model. With
// ai.mode = off nothing here runs and none of its keys or hints exist; with
// mode ask, a call whose pre-flight needs a yes waits in a dialog (the MR
// summary, which starts by itself, waits in its section instead).
//
// Nothing the model writes reaches the forge on its own: a summary is only
// shown, a draft reply lands in the reply editor and is posted by enter, and
// review comments land as pending agent drafts that need triage and a submit.

// aiKind is what the model is asked.
type aiKind int

const (
	aiSummary   aiKind = iota // the MR: Overview
	aiThreadSum               // one thread: Threads
	aiReply                   // a draft reply to one thread: Threads
	aiReview                  // an agent review pass over the diff
)

// aiKey names one answer on an MR: the kind, and the discussion for the
// thread kinds.
type aiKey struct {
	kind aiKind
	id   string
}

func (k aiKey) what() string {
	return [...]string{"summary", "thread summary", "draft reply", "agent review"}[k.kind]
}

// aiText is one asked-for answer and where it stands.
type aiText struct {
	l       load
	text    string
	profile string
	model   string
	cost    float64
	cached  bool
	head    string        // the MR head it was asked about
	refresh bool          // asked again, past the cache
	pf      *ai.Preflight // the call waits for a yes
	notes   bool          // the answer used the user's notes
	shown   bool          // a draft reply that has been put in the editor once

	added, rejected int // review: drafts saved, comments the diff did not have
}

// aiAnswer is what a finished call returns through mrResultMsg.out.
type aiAnswer struct {
	res      ai.Result
	pf       *ai.Preflight // set when the call needs a yes first; nothing was sent
	added    int
	rejected int
	notes    bool // the prompt carried the user's notes
}

// aiOn reports whether the page offers AI at all: ai.mode is not off and a
// profile is configured. A machine without AI sees none of it.
func (m model) aiOn() bool {
	if m.app == nil || m.app.Config == nil {
		return false
	}
	c := m.app.Config.AI
	if c.Mode == "off" || c.Profile == "" {
		return false
	}
	_, ok := c.Profiles[c.Profile]
	return ok
}

func (d *mrData) aiBusy() bool {
	for _, st := range d.ai {
		if st.l.busy() {
			return true
		}
	}
	return false
}

// discussion finds a thread by id.
func (d *mrData) discussion(id string) (forge.Discussion, bool) {
	for _, disc := range d.threads {
		if disc.ID == id {
			return disc, true
		}
	}
	return forge.Discussion{}, false
}

// maybeSummary starts the Overview's summary once the MR (its head SHA keys
// the cache) and its diff are read, and again when the head moves. A summary
// that is running, answered, failed or waiting for a yes is left alone: the
// audit log gets one line per ask, not one per redraw.
func (m *model) maybeSummary() tea.Cmd {
	p := m.mr
	d := p.data
	if d == nil || !m.aiOn() || d.mrL.st != loadOK {
		return nil
	}
	k := aiKey{kind: aiSummary}
	if st := d.ai[k]; st != nil && st.head == d.mr.HeadSHA {
		return nil
	}
	switch d.diffL.st {
	case loadIdle:
		return m.loadDiff(d) // the diff's arrival asks again
	case loadOK:
		return m.aiAsk(d, k, false, false)
	}
	return nil
}

// aiAsk runs one AI call for d off the UI loop. refresh buys a new answer past
// the cache; assume answers a pre-flight that asked for a yes.
func (m *model) aiAsk(d *mrData, k aiKey, refresh, assume bool) tea.Cmd {
	if !m.aiOn() {
		return nil
	}
	if d.ai == nil {
		d.ai = map[aiKey]*aiText{}
	}
	st := d.ai[k]
	if st == nil {
		st = &aiText{}
		d.ai[k] = st
	}
	if st.l.busy() {
		return nil
	}
	st.l, st.pf, st.head, st.refresh, st.shown = load{st: loadBusy}, nil, d.mr.HeadSHA, refresh, false

	app, reviews := m.app, m.reviews
	mr, diff, haveDiff, ref, forgeName := d.mr, d.diff, d.diffL.st == loadOK, d.ref, d.forgeKey
	disc, haveDisc := d.discussion(k.id)
	fn := func(ctx context.Context, be review.Backend) (any, error) {
		if k.kind == aiThreadSum || k.kind == aiReply {
			if !haveDisc {
				return nil, errors.New("the thread is not there any more: U reloads the page")
			}
		}
		svc, err := ai.NewService(app)
		if err != nil {
			return nil, err
		}
		svc.SprintStart = budget.SprintStart(app)
		if !haveDiff {
			if diff, err = be.MRDiff(ctx, ref); err != nil {
				return nil, fmt.Errorf("read the diff of %s: %w", ref, err)
			}
		}
		var job ai.Job
		var issue *forge.Issue
		if k.kind != aiThreadSum {
			issue = ai.LinkedIssue(ctx, be, mr)
		}
		switch k.kind {
		case aiSummary:
			job = ai.SummarizeJob(forgeName, mr, diff, issue)
		case aiThreadSum:
			job = ai.ThreadSummaryJob(forgeName, mr, disc, diff)
		case aiReply:
			job = ai.ReplyJob(forgeName, mr, disc, diff)
		case aiReview:
			threads, err := be.Discussions(ctx, ref)
			if err != nil {
				return nil, fmt.Errorf("read the threads of %s: %w", ref, err)
			}
			job = ai.ReviewJob(forgeName, mr, diff, issue, threads, nil)
		}
		if k.kind != aiThreadSum { // the user's own notes on the MR, its issue and the tracker parent
			notes, err := svc.NotesFor(forgeName, ai.NoteRefs(mr, issue)...)
			if err != nil {
				return nil, err
			}
			job = ai.WithNotes(job, notes)
		}
		job.Refresh, job.Assume = refresh, assume
		res, err := svc.Run(ctx, job)
		var nc *ai.ErrNeedsConfirm
		if errors.As(err, &nc) {
			return aiAnswer{pf: &nc.Preflight}, nil
		}
		if err != nil {
			return nil, err
		}
		ans := aiAnswer{res: res, notes: job.Notes}
		if k.kind == aiReview {
			if ans.added, ans.rejected, err = addAgentDrafts(svc, reviews, ref, diff, res); err != nil {
				return nil, err
			}
		}
		return ans, nil
	}
	return m.mrRunMsg(d, mrResultMsg{what: "ai", ai: k}, fn)
}

// addAgentDrafts turns a review answer into pending agent drafts, exactly as
// `ade ai review` does: comments on lines the diff does not show are rejected.
func addAgentDrafts(svc *ai.Service, st *review.Store, ref forge.Ref, diff forge.Diff, res ai.Result) (added, rejected int, err error) {
	if st == nil {
		return 0, 0, errors.New("the review store is not open")
	}
	comments, err := ai.ParseReview(res.Text)
	if err != nil {
		return 0, 0, err
	}
	sess, err := review.Resume(st, ref, diff)
	if err != nil {
		return 0, 0, err
	}
	ds, rej, err := svc.AddReviewDrafts(sess, comments, res.Profile)
	return len(ds), len(rej), err
}

// onAI files a finished call and decides what the user sees of it.
func (m *model) onAI(d *mrData, msg mrResultMsg) tea.Cmd {
	st := d.ai[msg.ai]
	if st == nil {
		return nil
	}
	k := msg.ai
	if msg.err != nil {
		st.l = load{st: loadFailed, err: msg.err}
		if k.kind != aiSummary { // the summary starts by itself: it reports in its own section
			m.emit(d, "ai.failed", events.Error, fmt.Sprintf("%s failed on %s", capitalize(k.what()), d.ref), aiCause(msg.err)+"\n"+msg.err.Error())
		}
		return nil
	}
	ans, _ := msg.out.(aiAnswer)
	if ans.pf != nil {
		st.l, st.pf = load{}, ans.pf
		if k.kind != aiSummary {
			m.mr.modal = &mrModal{kind: modalAI, ai: k}
		}
		return nil
	}
	st.l = load{st: loadOK}
	st.text, st.profile, st.model, st.cost, st.cached, st.notes = strings.TrimSpace(ans.res.Text), ans.res.Profile, ans.res.Model, ans.res.CostUSD, ans.res.CacheHit, ans.notes
	switch k.kind {
	case aiReply:
		m.deliverReply(d, k.id, st)
	case aiReview:
		st.added, st.rejected = ans.added, ans.rejected
		d.refreshDrafts(m.reviews)
		title := fmt.Sprintf("Agent review of %s: %s pending in the Review tab", d.ref, countWord(ans.added, "comment"))
		if ans.added == 0 {
			title = "Agent review of " + d.ref.String() + ": nothing to flag"
		}
		detail := ""
		if ans.rejected > 0 {
			detail = fmt.Sprintf("%d comment(s) were on lines the diff does not show and were dropped", ans.rejected)
		}
		m.emit(d, "mr.agent_review", events.Success, title, detail)
		return m.reloadHistory()
	}
	return nil
}

// aiCause words why a call failed for the screen.
func aiCause(err error) string {
	var nc *ai.NotInstalledError
	if stop, ok := ai.AsStop(err); ok {
		return "budget stop: " + stop.Message
	}
	switch {
	case errors.Is(err, ai.ErrDisabled):
		return "AI is off (ai.mode = off)"
	case errors.As(err, &nc):
		return nc.Error()
	}
	return oneLine(err.Error())
}

// deliverReply puts a finished draft reply in the editor when the page still
// shows that thread; otherwise it waits for r. It is never posted by itself.
func (m *model) deliverReply(d *mrData, id string, st *aiText) {
	p := m.mr
	if p == nil || p.data != d || p.tab != tabThreads || p.modal != nil {
		m.notice(events.Info, "AI draft reply ready: r opens it on its thread")
		return
	}
	list := d.threadList()
	if i := clamp(p.thSel, 0, len(list)-1); len(list) == 0 || list[i].d.ID != id {
		m.notice(events.Info, "AI draft reply ready: r opens it on its thread")
		return
	}
	m.openReply(d, id, st)
}

// openReply opens the reply editor, filled with the AI's draft when there is
// one that has not been shown yet.
func (m *model) openReply(d *mrData, id string, st *aiText) {
	md := &mrModal{kind: modalReply, thread: id, ed: newEditor("")}
	if st != nil && st.l.st == loadOK && st.text != "" && !st.shown {
		st.shown = true
		md.ed = newEditor(st.text)
		md.aiNote = fmt.Sprintf(m.g.T("AI draft · %s · %s · edit it: enter posts, esc throws it away"), st.model, costWord(st))
	}
	m.mr.modal = md
}

func costWord(st *aiText) string {
	if st.cached {
		return "cached, $0"
	}
	return fmt.Sprintf("$%.4f", st.cost)
}

// --- keys ------------------------------------------------------------------------

// aiReviewKey is A: run the agent review pass over the whole MR.
func (m *model) aiReviewKey() tea.Cmd {
	d := m.mr.data
	if d == nil || !m.aiOn() {
		return nil
	}
	if d.mr.State != "opened" {
		m.notice(events.Info, d.ref.String()+" is "+d.mr.State+": nothing to review")
		return nil
	}
	if st := d.ai[aiKey{kind: aiReview}]; st != nil && st.l.busy() {
		m.notice(events.Info, "The agent is already reviewing "+d.ref.String())
		return nil
	}
	return m.aiAsk(d, aiKey{kind: aiReview}, false, false)
}

// aiConfirmKey answers the pre-flight dialog.
func (m *model) aiConfirmKey(md *mrModal, ks string) tea.Cmd {
	d := m.mr.data
	st := d.ai[md.ai]
	switch ks {
	case "enter", "y":
		m.mr.modal = nil
		if st == nil {
			return nil
		}
		return m.aiAsk(d, md.ai, st.refresh, true)
	case "n":
		m.mr.modal = nil
		if st != nil {
			st.l, st.pf = load{}, nil
		}
	}
	return nil
}

// summaryKey is s on Overview: answer a waiting pre-flight, or ask again past
// the cache.
func (m *model) summaryKey() tea.Cmd {
	d := m.mr.data
	if d == nil || !m.aiOn() {
		return nil
	}
	k := aiKey{kind: aiSummary}
	st := d.ai[k]
	switch {
	case st != nil && st.l.busy():
		return nil
	case st != nil && st.pf != nil:
		m.mr.modal = &mrModal{kind: modalAI, ai: k}
		return nil
	case d.diffL.st != loadOK:
		return m.maybeSummary()
	}
	return m.aiAsk(d, k, true, false)
}

// threadAIKey handles d (draft a reply) and s (summarise the thread) on the
// selected thread.
func (m *model) threadAIKey(ks string, sel thread) tea.Cmd {
	d := m.mr.data
	if !m.aiOn() {
		return nil
	}
	k := aiKey{kind: aiReply, id: sel.d.ID}
	if ks == "s" {
		k.kind = aiThreadSum
	}
	if st := d.ai[k]; st != nil && st.l.busy() {
		return nil
	}
	return m.aiAsk(d, k, false, false) // reads the file's diff itself when the tab has not yet
}

// --- drawing ---------------------------------------------------------------------

// aiBlock is an answer in its section: the title with a guide word line
// ("AI · claude-haiku · $0.0021"), then the text; or why there is none.
func (m model) aiBlock(title string, st *aiText, w int) []string {
	g := m.g
	if st == nil {
		return nil
	}
	switch {
	case st.l.busy():
		return []string{subRule(title, "AI "+g.Sep+" asking the model", w, g), "   " + stFaint.Render(spinnerFrame(g, m.mr.spin)+" working in the background: the page stays usable")}
	case st.pf != nil:
		why := strings.Join(st.pf.Confirm, "; ")
		return []string{
			subRule(title, "AI "+g.Sep+" waiting for a yes", w, g),
			"   " + stWarn.Render(g.Warn+" "+ansi.Truncate(why, max(10, w-8), m.g.Ell)),
			"   " + stFaint.Render(ansi.Truncate(m.g.T(st.pf.Describe()), w-6, m.g.Ell)),
			"   " + stFaint.Render(m.g.T("s asks · nothing was sent")),
		}
	case st.l.st == loadFailed:
		return []string{
			subRule(title, "AI", w, g),
			"   " + stWarn.Render(ansi.Truncate(g.Warn+" no "+strings.ToLower(title)+": "+aiCause(st.l.err), w-6, m.g.Ell)),
		}
	case st.l.st != loadOK:
		return nil
	}
	guide := "AI " + g.Sep + " " + st.model + " " + g.Sep + " " + costWord(st)
	if st.notes {
		guide += " " + g.Sep + " includes your notes"
	}
	l := []string{subRule(title, guide, w, g)}
	for _, ln := range m.mdLines(st.text, max(10, w-6)) {
		l = append(l, "   "+ln)
	}
	return l
}

// agentLine is the Review tab's line about the agent pass: running, done, or
// how to start one. "" when AI is off.
func (m model) agentLine(w int) string {
	d := m.mr.data
	g := m.g
	if !m.aiOn() {
		return ""
	}
	st := d.ai[aiKey{kind: aiReview}]
	var s string
	switch {
	case st == nil || st.l.st == loadIdle && st.pf == nil:
		s = stFaint.Render("A runs an agent review pass: its comments arrive here as pending drafts")
	case st.l.busy():
		s = stBold.Render(spinnerFrame(g, m.mr.spin)+" agent review running") + stFaint.Render(" (the review model reads the diff, the threads and the linked issue)")
	case st.pf != nil:
		s = stWarn.Render(g.Warn+" agent review waits for a yes") + stFaint.Render(m.g.T(" · A asks again"))
	case st.l.st == loadFailed:
		s = stWarn.Render(g.Warn+" agent review failed: "+aiCause(st.l.err)) + stFaint.Render(m.g.T(" · A retries"))
	default:
		more := ""
		if st.rejected > 0 {
			more = fmt.Sprintf(" %s %d dropped", g.Sep, st.rejected) // comments on lines the diff does not show
		}
		s = stOK.Render(g.OK+" agent review done") + stFaint.Render(fmt.Sprintf(" %s %s pending%s %s %s %s %s", g.Sep, countWord(st.added, "comment"), more, g.Sep, costWord(st), g.Sep, st.model))
	}
	return " " + ansi.Truncate(s, w-2, m.g.Ell)
}

// aiModal draws the pre-flight dialog for a call waiting on a yes.
func (m model) aiModal(md *mrModal, inner int) (string, []string) {
	d := m.mr.data
	st := d.ai[md.ai]
	v := stFaint.Render(m.g.Vertical) + " "
	var l []string
	if st != nil && st.pf != nil {
		for _, c := range st.pf.Confirm {
			l = append(l, v+stWarn.Render(ansi.Truncate(m.g.Warn+" "+c, inner-3, m.g.Ell)))
		}
		l = append(l, v+stFaint.Render(ansi.Truncate(m.g.T(st.pf.Describe()), inner-3, m.g.Ell)))
	}
	l = append(l, "", stFaint.Render("Nothing is sent until you say yes."))
	return m.g.T("Ask the model · ") + md.ai.what() + m.g.T(" · ") + d.ref.String(), l
}

// threadAI is the AI part of a thread's conversation: its summary, and the
// state of a draft reply that is running, waiting for a yes, failed, or ready.
func (m model) threadAI(d *mrData, id string, w int) []string {
	var l []string
	flush := func(blk []string) { // the conversation pane has no margin of its own
		for _, ln := range blk {
			l = append(l, strings.TrimPrefix(ln, "   "))
		}
		l = append(l, "")
	}
	if st := d.ai[aiKey{kind: aiThreadSum, id: id}]; st != nil {
		if blk := m.aiBlock("Summary", st, w+3); len(blk) > 0 {
			flush(blk)
		}
	}
	st := d.ai[aiKey{kind: aiReply, id: id}]
	switch {
	case st == nil:
	case st.l.st == loadOK && !st.shown && st.text != "":
		l = append(l, stOK.Render(m.g.OK+" AI draft reply ready")+stFaint.Render(m.g.T(" · r opens it in the editor")), "")
	case st.l.st != loadOK:
		if blk := m.aiBlock("Draft reply", st, w+3); len(blk) > 0 {
			flush(blk)
		}
	}
	return l
}
