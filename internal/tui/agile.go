package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/agile"
	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/budget"
	"github.com/coredev-uk/ade/internal/clipboard"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forgesync"
)

// The Standup and Sprint review pages (docs/PLAN.md 6.4). Both read the sync
// cache at once and then ask the forge for activity and history in the
// background, so opening one never blocks on the network.

// agileKind says which agile page is open.
type agileKind int

const (
	agileNone agileKind = iota
	agileStandup
	agileReview
)

// agileUI is the state of both pages. It lives in model.ag.
type agileUI struct {
	kind agileKind
	back page // page to return to on esc

	standup    *agile.Standup
	standupErr error
	standupGen int // bumped per load: a slow answer for an older request is dropped

	review     *agile.Review
	reviewErr  error
	reviewGen  int
	reviewProj string // ADE project; "" = the first with data
	reviewSpr  string // sprint title; "" = the current one
	selRef     string // carried item `>` moves (J/K); "" = the first
	team       bool   // the whole team's review; false (the default) is your retro

	loading   bool
	narrating bool
	// pf is the narrative's pre-flight while it waits for a yes (inline y/n
	// dialog, like the other AI surfaces); nothing was sent.
	pf     *ai.Preflight
	scroll int
}

// agileMsg is implemented by the messages the pages exchange with Update.
type agileMsg interface{ agileMsg() }

type (
	standupMsg struct {
		gen int
		s   agile.Standup
		err error
	}
	reviewMsg struct {
		gen int
		r   agile.Review
		err error
	}

	// narrativeMsg is the answer of an AI narrative request. auto marks one
	// nobody asked for (the retro written when the review opens): its
	// failures stay quiet.
	narrativeMsg struct {
		kind    agileKind
		standup *agile.Standup
		review  *agile.Review
		res     ai.Result
		err     error
		auto    bool
	}
)

func (narrativeMsg) agileMsg() {}
func (standupMsg) agileMsg()   {}
func (reviewMsg) agileMsg()    {}

func (m model) collector() *agile.Collector {
	c := agile.NewCollector(m.app)
	if m.sync != nil && m.sync.Open != nil {
		c.Open = m.sync.Open
	}
	return c
}

// isAgilePage reports whether a Standup or Review page is showing.
func (m model) isAgilePage() bool { return m.ag.kind != agileNone }

// openStandup shows the Standup page and loads it.
func (m model) openStandup() (model, tea.Cmd) {
	if m.mr != nil {
		return m, nil // the MR page owns the screen
	}
	m.enterAgile(agileStandup)
	return m.reload()
}

// openReview shows the Sprint review page and loads it.
func (m model) openReview() (model, tea.Cmd) {
	if m.mr != nil {
		return m, nil
	}
	m.enterAgile(agileReview)
	return m.reload()
}

func (m *model) enterAgile(k agileKind) {
	if !m.isAgilePage() {
		m.ag.back = m.page
	}
	m.ag.kind, m.ag.scroll, m.help, m.ag.pf = k, 0, false, nil
}

// reload asks for the open page's data again. The generation is bumped on
// the copy of the model that Update returns, so callers keep the result.
func (m model) reload() (model, tea.Cmd) {
	m.ag.loading = true
	switch m.ag.kind {
	case agileStandup:
		m.ag.standupGen++
		gen, c, ctx := m.ag.standupGen, m.collector(), m.ctx
		return m, func() tea.Msg {
			s, err := c.Standup(ctx, time.Time{})
			return standupMsg{gen: gen, s: s, err: err}
		}
	case agileReview:
		m.ag.reviewGen++
		if m.ag.reviewProj == "" {
			m.ag.reviewProj = m.reviewResult().Project // several projects: the first with sprints, not an error
		}
		gen, c, ctx := m.ag.reviewGen, m.collector(), m.ctx
		o := agile.ReviewOptions{Project: m.ag.reviewProj, Sprint: m.ag.reviewSpr, Mine: !m.ag.team}
		return m, func() tea.Msg {
			r, err := c.Review(ctx, o)
			return reviewMsg{gen: gen, r: r, err: err}
		}
	}
	return m, nil
}

// agileUpdate applies a page message.
func (m model) agileUpdate(msg agileMsg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case standupMsg:
		if msg.gen != m.ag.standupGen {
			return m, nil
		}
		m.ag.loading = false
		if msg.err != nil {
			m.ag.standupErr = msg.err
			return m, nil
		}
		s := msg.s
		if old := m.ag.standup; old != nil && old.Date == s.Date {
			s.Narrative = old.Narrative // a reload keeps the narrative already bought
		}
		m.ag.standup, m.ag.standupErr = &s, nil
	case reviewMsg:
		if msg.gen != m.ag.reviewGen {
			return m, nil
		}
		m.ag.loading = false
		if msg.err != nil {
			m.ag.reviewErr = msg.err
			return m, nil
		}
		r := msg.r
		if old := m.ag.review; old != nil && old.Sprint.Title == r.Sprint.Title && old.Project == r.Project && old.Mine == r.Mine {
			r.Narrative = old.Narrative
		}
		m.ag.review, m.ag.reviewErr = &r, nil
		if r.Narrative == "" {
			return m.narrateAuto()
		}
	case narrativeMsg:
		return m.narrativeDone(msg)
	}
	return m, nil
}

// agileKey handles a key while an agile page is open. ok is false for keys
// the global handler should take (quit, help, notifications, refresh).
func (m model) agileKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.agileAsking() && k.String() != "ctrl+c" {
		next, cmd := m.agileAskKey(k.String())
		return next, cmd, true
	}
	switch ks := k.String(); ks {
	case "esc":
		if m.help {
			return m, nil, false
		}
		if m.nc.dismissToasts() {
			return m, nil, true
		}
		m.ag.kind, m.page = agileNone, m.ag.back
		return m, nil, true
	case "t":
		m.ag.kind, m.page = agileNone, pageHome
		return m, nil, true
	case "S":
		if m.ag.kind == agileStandup {
			m.ag.kind, m.page = agileNone, m.ag.back
			return m, nil, true
		}
		next, cmd := m.openStandup()
		return next, cmd, true
	case "V":
		if m.ag.kind == agileReview {
			m.ag.kind, m.page = agileNone, m.ag.back
			return m, nil, true
		}
		next, cmd := m.openReview()
		return next, cmd, true
	case "j", "down":
		m.ag.scroll++
	case "k", "up":
		m.ag.scroll = max(0, m.ag.scroll-1)
	case "g", "home":
		m.ag.scroll = 0
	case "G", "end":
		m.ag.scroll = 1 << 20 // clamped when drawn
	case "pgdown", "space":
		m.ag.scroll += max(1, m.h-8)
	case "pgup":
		m.ag.scroll = max(0, m.ag.scroll-max(1, m.h-8))
	case "r":
		next, cmd := m.reload()
		return next, cmd, true
	case "y":
		next, cmd := m.copyAgile()
		return next, cmd, true
	case "n":
		next, cmd := m.narrate(false)
		return next, cmd, true
	case ">", "M", "J", "K":
		if m.ag.kind != agileReview {
			return m, nil, false
		}
		switch ks {
		case "J":
			m.stepCarried(1)
		case "K":
			m.stepCarried(-1)
		default:
			next, cmd := m.openReviewMove(ks == "M")
			return next, cmd, true
		}
	case "[", "]", "m", "p":
		if m.ag.kind != agileReview {
			return m, nil, false
		}
		if ks == "p" && len(m.sprintProjects()) < 2 {
			return m, nil, true // nothing to cycle to
		}
		m.stepReview(ks)
		next, cmd := m.reload()
		return next, cmd, true
	default:
		return m, nil, false
	}
	return m, nil, true
}

// stepReview moves the review to another sprint ([ older, ] newer), switches
// between your retro and the team's review, or cycles the project.
func (m *model) stepReview(key string) {
	res := m.reviewResult()
	m.ag.selRef = ""
	switch key {
	case "m":
		m.ag.team = !m.ag.team
	case "p":
		names := m.sprintProjects()
		cur := -1
		for i, n := range names {
			if n == res.Project {
				cur = i
			}
		}
		if len(names) > 0 {
			m.ag.reviewProj, m.ag.reviewSpr = names[(cur+1)%len(names)], ""
		}
	default:
		sp := res.Sprints
		title := m.ag.reviewSpr
		if title == "" && m.ag.review != nil {
			title = m.ag.review.Sprint.Title
		}
		i := -1
		for j, s := range sp {
			if s.Title == title {
				i = j
			}
		}
		if i < 0 {
			return
		}
		if key == "[" {
			i--
		} else {
			i++
		}
		if i >= 0 && i < len(sp) {
			m.ag.reviewSpr = sp[i].Title
		}
	}
}

// sprintProjects names the cached projects that have sprints: the ones the
// review can show and `p` cycles.
func (m model) sprintProjects() []string {
	var names []string
	for _, r := range m.results {
		if len(r.Sprints) > 0 {
			names = append(names, r.Project)
		}
	}
	return names
}

// reviewResult is the cached project the review page concerns.
func (m model) reviewResult() forgesync.Result {
	name := m.ag.reviewProj
	if name == "" && m.ag.review != nil {
		name = m.ag.review.Project
	}
	for _, r := range m.results {
		if r.Project == name {
			return r
		}
	}
	for _, r := range m.results {
		if len(r.Sprints) > 0 {
			return r
		}
	}
	return forgesync.Result{}
}

// copyAgile copies the open report as Markdown with OSC 52.
func (m model) copyAgile() (model, tea.Cmd) {
	var text, what string
	switch {
	case m.ag.kind == agileStandup && m.ag.standup != nil:
		text, what = agile.RenderStandup(*m.ag.standup, agile.Markdown), "standup"
	case m.ag.kind == agileReview && m.ag.review != nil:
		r := *m.ag.review
		text, what = agile.RenderReview(r, agile.Markdown), strings.ToLower(agile.ReviewTitle(r))
	default:
		return m, nil
	}
	seq, err := clipboard.Sequence(text)
	if err != nil {
		e := events.New("ui.copy_failed", events.Warn, "could not copy the "+what+": "+shortErr(err))
		e.Transient = true
		m.nc.push(e)
		return m, nil
	}
	e := events.New("ui.copied", events.Info, "copied the "+what+" as Markdown")
	e.Transient = true
	m.nc.push(e)
	return m, tea.Raw(seq)
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, ": "); i > 0 && errors.Is(err, clipboard.ErrTooLarge) {
		return s[i+2:]
	}
	return s
}

func fmtDay(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.Format("Mon 2 Jan")
}

func fmtSpan(g glyphs, start, due string) string {
	a, err1 := time.Parse("2006-01-02", start)
	b, err2 := time.Parse("2006-01-02", due)
	if err1 != nil || err2 != nil {
		return start + g.T(" – ") + due
	}
	if a.Month() == b.Month() {
		return fmt.Sprintf(g.T("%d–%s"), a.Day(), b.Format("2 Jan"))
	}
	return a.Format("2 Jan") + g.T(" – ") + b.Format("2 Jan")
}

// narrate asks the AI service for the open report's narrative. The call is
// cached by content (a repeat costs $0), audited and capped. With AI
// unavailable it says why; when the pre-flight needs a yes it asks inline
// (agileAskKey), and assume answers that.
func (m model) narrate(assume bool) (model, tea.Cmd) {
	if m.ag.narrating {
		return m, nil
	}
	var st *agile.Standup
	var rv *agile.Review
	switch {
	case m.ag.kind == agileStandup && m.ag.standup != nil:
		s := *m.ag.standup
		st = &s
	case m.ag.kind == agileReview && m.ag.review != nil:
		r := *m.ag.review
		rv = &r
	default:
		return m, nil
	}
	if ok, why := m.aiAvail(); !ok {
		m.pushAISkipped("no AI narrative: " + why)
		return m, nil
	}
	m.ag.narrating = true
	kind, ctx, a := m.ag.kind, m.ctx, m.app
	return m, func() tea.Msg {
		svc, err := ai.NewService(a)
		if err != nil {
			return narrativeMsg{kind: kind, err: err}
		}
		svc.SprintStart = budget.SprintStart(a)
		n := &agile.AINarrator{Svc: svc, Tune: func(j *ai.Job) { j.Assume = assume }}
		msg := narrativeMsg{kind: kind}
		if st != nil {
			var s agile.Standup
			s, msg.err = agile.WithNarrative(ctx, n, *st)
			msg.standup = &s
		} else {
			var r agile.Review
			r, msg.err = agile.WithReviewNarrative(ctx, n, *rv)
			msg.review = &r
		}
		msg.res = n.Last
		return msg
	}
}

// narrateAuto writes the retro nobody asked for when the review opens: only
// with AI set up and the budget not critical (lastday.go), and never with a
// prompt. A skipped or failed one leaves the page as it was.
func (m model) narrateAuto() (model, tea.Cmd) {
	a := m.app
	if a == nil || m.ag.kind != agileReview {
		return m, nil
	}
	if !m.aiOn() {
		return m, nil
	}
	next, cmd := m.narrate(false)
	if cmd == nil {
		return next, nil
	}
	return next, func() tea.Msg {
		if ok, _ := autoAI(a); !ok {
			return narrativeMsg{kind: agileReview, auto: true, err: errAutoSkipped}
		}
		msg := cmd()
		if nm, ok := msg.(narrativeMsg); ok {
			nm.auto = true
			return nm
		}
		return msg
	}
}

// errAutoSkipped is an automatic narrative the budget or AI settings ruled out.
var errAutoSkipped = errors.New("automatic AI summary skipped")

func (m model) narrativeDone(msg narrativeMsg) (tea.Model, tea.Cmd) {
	m.ag.narrating = false
	if msg.auto && msg.err != nil {
		return m, nil // nobody asked: say nothing
	}
	if nc := new(*ai.ErrNeedsConfirm); errors.As(msg.err, nc) {
		// Asked inline (agileAskKey answers it), but only on the page the call
		// was for: one the user has left or switched has nobody to answer.
		if m.isAgilePage() && msg.kind == m.ag.kind {
			m.ag.pf = &(*nc).Preflight
		}
		return m, nil
	}
	if msg.err != nil {
		m.pushAISkipped("no AI narrative: " + ai.Human(msg.err))
		return m, nil
	}
	if msg.standup != nil && m.ag.standup != nil && m.ag.standup.Date == msg.standup.Date {
		m.ag.standup.Narrative = msg.standup.Narrative
	}
	if rv := m.ag.review; msg.review != nil && rv != nil && rv.Sprint.Title == msg.review.Sprint.Title && rv.Project == msg.review.Project && rv.Mine == msg.review.Mine {
		m.ag.review.Narrative = msg.review.Narrative
	}
	cost := fmt.Sprintf("$%.4f", msg.res.CostUSD)
	if msg.res.CacheHit {
		cost = "cached, $0"
	}
	if note := msg.res.CutShort(); note != "" {
		cost += ", " + note
	}
	e := events.New("ui.ai_narrative", events.Info, "narrative written ("+cost+")")
	e.Transient = true
	m.nc.push(e)
	return m, nil
}

// pushAISkipped toasts why an AI step was left out.
func (m *model) pushAISkipped(text string) {
	e := events.New("ui.ai_skipped", events.Warn, text)
	e.Transient = true
	m.nc.push(e)
}

// agileAskKey answers the narrative's pre-flight: y sends the call, anything
// that closes the dialog sends nothing and leaves the report as it is.
func (m model) agileAskKey(ks string) (model, tea.Cmd) {
	switch ks {
	case "y", "enter":
		m.ag.pf = nil
		return m.narrate(true)
	case "n", "esc", "q":
		m.ag.pf = nil
	}
	return m, nil
}

// agileAsking reports whether the narrative's pre-flight dialog is showing.
func (m model) agileAsking() bool { return m.isAgilePage() && m.ag.pf != nil }

// agileAskOverlay is the narrative's pre-flight dialog.
func (m model) agileAskOverlay(w int) string {
	if !m.agileAsking() {
		return ""
	}
	dw := min(w, 100)
	l := m.preflightLines(m.ag.pf, dw-4)
	l = append(l, "", keys("y", "send", "esc", "cancel"))
	title := "AI narrative · standup"
	if m.ag.kind == agileReview {
		title = "AI narrative · sprint retro"
	}
	return m.dialog(m.g.T(title), l, dw)
}
