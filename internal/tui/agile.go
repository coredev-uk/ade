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
	mine       bool

	loading   bool
	narrating bool
	scroll    int
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

	// narrativeMsg is the answer of an AI narrative request.
	narrativeMsg struct {
		kind    agileKind
		standup *agile.Standup
		review  *agile.Review
		res     ai.Result
		err     error
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
	m.ag.kind, m.ag.scroll, m.help = k, 0, false
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
		o := agile.ReviewOptions{Project: m.ag.reviewProj, Sprint: m.ag.reviewSpr, Mine: m.ag.mine}
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
	case narrativeMsg:
		return m.narrativeDone(msg)
	}
	return m, nil
}

// agileKey handles a key while an agile page is open. ok is false for keys
// the global handler should take (quit, help, notifications, refresh).
func (m model) agileKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
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
		next, cmd := m.narrate()
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

// stepReview moves the review to another sprint ([ older, ] newer), toggles
// "mine", or cycles the project.
func (m *model) stepReview(key string) {
	res := m.reviewResult()
	m.ag.selRef = ""
	switch key {
	case "m":
		m.ag.mine = !m.ag.mine
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
		text, what = agile.RenderReview(*m.ag.review, agile.Markdown), "sprint review"
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
// cached by content (a repeat costs $0), audited and capped; with ai.mode off
// or a needed confirmation it only says so, since a key press cannot confirm.
func (m model) narrate() (model, tea.Cmd) {
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
	m.ag.narrating = true
	kind, ctx, a := m.ag.kind, m.ctx, m.app
	return m, func() tea.Msg {
		svc, err := ai.NewService(a)
		if err != nil {
			return narrativeMsg{kind: kind, err: err}
		}
		svc.SprintStart = budget.SprintStart(a)
		n := &agile.AINarrator{Svc: svc}
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

func (m model) narrativeDone(msg narrativeMsg) (tea.Model, tea.Cmd) {
	m.ag.narrating = false
	if msg.err != nil {
		text, subject := "no AI narrative: "+msg.err.Error(), ""
		if errors.As(msg.err, new(*ai.ErrNeedsConfirm)) {
			text = "AI narrative needs confirmation"
			subject = "run: ade standup --ai --yes"
			if msg.review != nil {
				subject = "run: ade sprint review --ai --yes"
			}
		}
		e := events.New("ui.ai_skipped", events.Warn, text)
		e.Subject = subject
		e.Transient = true
		m.nc.push(e)
		return m, nil
	}
	if msg.standup != nil && m.ag.standup != nil && m.ag.standup.Date == msg.standup.Date {
		m.ag.standup.Narrative = msg.standup.Narrative
	}
	if msg.review != nil && m.ag.review != nil && m.ag.review.Sprint.Title == msg.review.Sprint.Title {
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
