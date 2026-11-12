package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/agile"
	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/budget"
)

// criticalBudget is the share of a budget cap from which the budget counts as
// critical: AI summaries nobody asked for (Home's last working day, the sprint
// review's retro) stop there, before the cap itself refuses calls.
const criticalBudget = 0.9

// lastDayUI is Home's "Last working day" block: what you did on the last day
// you pushed commits or opened or merged an MR, summarised by AI. It is asked
// for once a day when Home first loads; the AI cache makes a repeat free.
type lastDayUI struct {
	asked string // today's date when it was asked for
	busy  bool
	day   *agile.LastDay
	note  string // why the summary is missing (budget, confirmation, error)
}

type lastDayMsg struct {
	today string
	day   *agile.LastDay
	note  string
}

// autoAI says whether an AI call nobody asked for may go out: AI set up and
// on, and no budget cap at criticalBudget or beyond. note is why not, empty
// when AI is simply not set up.
func autoAI(a *app.App) (ok bool, note string) {
	cfg := a.Config
	if cfg.AI.Mode == "off" || len(cfg.AI.Profiles) == 0 {
		return false, ""
	}
	d, err := budget.ForApp(a)
	if err != nil {
		return true, ""
	}
	r, err := budget.Build(context.Background(), d, budget.Options{Offline: true, Days: 1, Window: time.Hour})
	if err != nil {
		return true, ""
	}
	if c := pickChip(r.Caps); c.limit > 0 && c.spent/c.limit >= criticalBudget {
		return false, fmt.Sprintf("AI summary paused: %.0f%% of the %s budget is spent", 100*c.spent/c.limit, c.period)
	}
	return true, ""
}

// loadLastDay asks for the block once a day, when AI is set up.
func (m *model) loadLastDay() tea.Cmd {
	a := m.app
	today := time.Now().Format("2006-01-02")
	if a == nil || m.ld.busy || m.ld.asked == today {
		return nil
	}
	if cfg := a.Config; cfg.AI.Mode == "off" || len(cfg.AI.Profiles) == 0 {
		return nil // AI not set up: no block at all
	}
	m.ld.asked, m.ld.busy = today, true
	ctx, c := m.ctx, m.collector()
	return func() tea.Msg {
		l, err := c.LastDay(ctx)
		if err != nil {
			return lastDayMsg{today: today, note: "last working day: " + shortErr(err)}
		}
		if l.Date == "" {
			return lastDayMsg{today: today, day: &l}
		}
		ok, note := autoAI(a)
		if !ok {
			return lastDayMsg{today: today, day: &l, note: note}
		}
		svc, err := ai.NewService(a)
		if err != nil {
			return lastDayMsg{today: today, day: &l, note: "no AI summary: " + shortErr(err)}
		}
		svc.SprintStart = budget.SprintStart(a)
		text, err := (&agile.AINarrator{Svc: svc}).NarrateLastDay(ctx, l)
		switch {
		case errors.As(err, new(*ai.ErrNeedsConfirm)):
			return lastDayMsg{today: today, day: &l, note: "AI summary needs confirmation under your budget settings"}
		case err != nil:
			return lastDayMsg{today: today, day: &l, note: "no AI summary: " + shortErr(err)}
		}
		l.Narrative = strings.TrimSpace(text)
		return lastDayMsg{today: today, day: &l}
	}
}

func (m *model) onLastDay(msg lastDayMsg) {
	m.ld.busy = false
	if msg.today == m.ld.asked {
		m.ld.day, m.ld.note = msg.day, msg.note
	}
}

// lastDayMaxEntries caps the plain list shown when there is no AI summary.
const lastDayMaxEntries = 5

// lastDayLines is the block at the top of Home: the AI summary, or the day's
// facts when there is none, with the reason faint underneath.
func (m model) lastDayLines(w int) []string {
	l := m.ld.day
	if l == nil || l.Date == "" {
		if m.ld.busy {
			return []string{section("Last working day", m.g.Sync+" reading your activity", w, m.g), ""}
		}
		return nil
	}
	guide := l.From.Format("Mon 2 Jan")
	if l.Narrative != "" {
		guide += " " + m.g.Sep + " AI summary"
	}
	out := []string{section("Last working day", guide, w, m.g)}
	if l.Narrative != "" {
		for _, ln := range m.mdLines(l.Narrative, w-4) {
			out = append(out, "   "+ln)
		}
	} else {
		for i, e := range l.Done {
			if i == lastDayMaxEntries {
				out = append(out, "   "+stFaint.Render(fmt.Sprintf("+ %d more", len(l.Done)-i)))
				break
			}
			out = append(out, "   "+stFaint.Render(e.At.In(l.From.Location()).Format("15:04"))+"  "+ansi.Truncate(e.Text, w-12, m.g.Ell))
		}
	}
	if m.ld.note != "" {
		out = append(out, "   "+stFaint.Render(ansi.Truncate(m.ld.note, w-4, m.g.Ell)))
	}
	return append(out, "")
}
