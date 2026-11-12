package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/budget"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/records"
)

// Issue research (feedback F13). `R` on a Home item opens a dialog on the item's
// stored research: none yet (enter starts one), running, failed (enter tries
// again) or done, with the answer to scroll through. Starting runs the
// pre-flight here (a question the caps raise is asked in the dialog) and hands
// the call to a detached worker, exactly as `ade ai research --detach`: the
// result lands in the item's record, a notification says when it is done, and
// closing the dialog or the dashboard does not stop it. Nothing about it needs
// the dashboard: the CLI shows the same record.
//
// The dialog uses the notes dialog's slot (m.nt): both are one-item dialogs
// that own the screen, so everything that keeps other dialogs from opening
// over them (and the footer, toasts, overlay layer) already covers it.

type rsStage int

const (
	rsShow     rsStage = iota // the stored job, or none
	rsStarting                // the pre-flight runs and the worker is being started
	rsAsk                     // the pre-flight needs a yes
)

// researchState is the research mode of a notesDialog.
type researchState struct {
	stage rsStage
	job   ai.ResearchView
	have  bool          // a job exists
	pf    *ai.Preflight // rsAsk: what the call would cost
	// refresh is whether the call being asked about goes past the cache.
	refresh bool
}

// researchStartedMsg is the answer to a start.
type researchStartedMsg struct {
	d   *notesDialog
	st  ai.ResearchStart
	pf  *ai.Preflight // set when the call needs a yes first
	err error
}

// openResearch is Home's `R`.
func (m model) openResearch() (model, tea.Cmd) {
	if m.page != pageHome || m.mr != nil || m.isAgilePage() || m.sw != nil || m.cl != nil || m.agt != nil || m.nt != nil || m.wt != nil || m.sm != nil ||
		m.homeSel >= len(m.homeRows) || m.homeRows[m.homeSel].item == nil {
		return m, nil
	}
	if !m.aiOn() {
		return m.toast("ui.hint", events.Info, "Research needs AI: ai.mode is off or no profile is set", ""), nil
	}
	it := m.homeRows[m.homeSel].item
	d := &notesDialog{ref: it.Ref, title: it.Issue.Title, item: *it, rs: &researchState{}}
	for _, r := range m.results {
		if r.Project == it.Project {
			d.forgeName = r.Forge
			break
		}
	}
	m.nt = d
	m.researchLoad(d)
	return m, nil
}

// researchService is a Service that can only read the records: enough to read a
// stored job.
func (m model) researchService() *ai.Service {
	return &ai.Service{Records: records.New(m.app.RecordsDir), Now: time.Now}
}

// researchLoad reads the item's stored job (a local file).
func (m model) researchLoad(d *notesDialog) {
	d.warn = ""
	rs := d.rs
	rs.have = false
	if m.app == nil || d.forgeName == "" {
		d.warn = m.g.T("the item's forge is unknown · press U to sync, then R again")
		return
	}
	v, err := m.researchService().ReadResearch(d.forgeName, d.ref)
	switch {
	case errors.Is(err, records.ErrNoResearch):
	case err != nil:
		d.warn = m.g.T("the stored research could not be read · check the records directory (ade doctor)")
	default:
		rs.job, rs.have = v, true
	}
}

// researchStart runs the pre-flight and starts the worker off the UI loop.
func (m model) researchStart(d *notesDialog, assume, refresh bool) tea.Cmd {
	d.rs.stage, d.rs.refresh, d.warn = rsStarting, refresh, ""
	ctx, a, ref, forgeName := m.ctx, m.app, d.ref, d.forgeName
	if ctx == nil {
		ctx = context.Background()
	}
	mm := m
	return func() tea.Msg {
		be, err := mm.backend(ctx, ref, forgeName)
		if err != nil {
			return researchStartedMsg{d: d, err: err}
		}
		svc, err := ai.NewService(a)
		if err != nil {
			return researchStartedMsg{d: d, err: err}
		}
		svc.SprintStart = budget.SprintStart(a)
		in, err := ai.GatherResearch(ctx, be, ref)
		if err != nil {
			return researchStartedMsg{d: d, err: err}
		}
		job := ai.ResearchJob(forgeName, in)
		notes, err := svc.NotesFor(forgeName, ai.ResearchNoteRefs(in.Issue)...)
		if err != nil {
			return researchStartedMsg{d: d, err: err}
		}
		job = ai.WithNotes(job, notes)
		st, err := svc.StartResearch(ctx, forgeName, job, ai.ResearchOpts{Assume: assume, Refresh: refresh})
		var nc *ai.ErrNeedsConfirm
		if errors.As(err, &nc) {
			return researchStartedMsg{d: d, pf: &nc.Preflight}
		}
		return researchStartedMsg{d: d, st: st, err: err}
	}
}

// researchUpdate routes a finished start. handled is false for other messages.
func (m model) researchUpdate(msg researchStartedMsg) model {
	d := m.nt
	if d == nil || d.rs == nil || msg.d != d {
		return m
	}
	rs := d.rs
	switch {
	case msg.pf != nil:
		rs.stage, rs.pf = rsAsk, msg.pf
		return m
	case errors.Is(msg.err, ai.ErrResearchRunning):
		rs.stage = rsShow
		m.researchLoad(d)
		d.warn = m.g.T("research is already running for this issue")
		return m
	case msg.err != nil:
		rs.stage = rsShow
		m.researchLoad(d)
		d.warn = aiCause(msg.err)
		return m
	}
	rs.stage, rs.pf = rsShow, nil
	m.researchLoad(d)
	m.loadNotes() // the side panel's research block
	if msg.st.Result == nil {
		m.notice(events.Info, "Research started on "+d.ref.String()+": a notification says when it is done")
	}
	return m
}

// researchKey handles a key in the research dialog.
func (m model) researchKey(ks string) (model, tea.Cmd) {
	d := m.nt
	rs := d.rs
	switch rs.stage {
	case rsStarting:
		if ks == "esc" || ks == "q" {
			m.nt = nil // the call is on its way; its answer finds no dialog and is dropped
		}
		return m, nil
	case rsAsk:
		switch ks {
		case "y", "enter":
			return m, m.researchStart(d, true, rs.refresh)
		case "esc", "q", "n":
			rs.stage, rs.pf = rsShow, nil
		}
		return m, nil
	}
	switch ks {
	case "esc", "q":
		m.nt = nil
	case "j", "down":
		d.scroll++
	case "k", "up":
		d.scroll = max(d.scroll-1, 0)
	case "pgdown", "ctrl+d", "space", " ":
		d.scroll += 8
	case "pgup", "ctrl+u":
		d.scroll = max(d.scroll-8, 0)
	case "g", "home":
		d.scroll = 0
	case "r":
		m.researchLoad(d)
	case "enter":
		if !rs.have || rs.job.State == records.ResearchFailed {
			return m, m.researchStart(d, false, false)
		}
	case "n":
		if rs.have && rs.job.State == records.ResearchDone {
			return m, m.researchStart(d, false, true)
		}
	}
	return m, nil
}

// researchOverlay draws the dialog.
func (m model) researchOverlay(d *notesDialog, w, h int) string {
	rs := d.rs
	dw := min(w, 100)
	inner := dw - 4
	g := m.g
	l := []string{stFaint.Render(ansi.Truncate(d.title, inner, g.Ell)), ""}
	switch {
	case rs.stage == rsStarting:
		l = append(l, section("Research", g.T("asking"), inner, g), stFaint.Render(g.T("reading the issue, then the pre-flight…")))
	case rs.stage == rsAsk && rs.pf != nil:
		l = append(l, section("Research", g.T("waiting for a yes"), inner, g))
		for _, c := range rs.pf.Confirm {
			l = append(l, stWarn.Render(ansi.Truncate(g.Warn+" "+c, inner, g.Ell)))
		}
		l = append(l, stFaint.Render(ansi.Truncate(g.T(rs.pf.Describe()), inner, g.Ell)), "", stFaint.Render(g.T("Nothing is sent until you say yes.")))
	default:
		l = append(l, m.researchBody(d, inner, max(4, h-16))...)
	}
	if d.warn != "" {
		l = append(l, "", stWarn.Render(ansi.Wrap(g.Warn+" "+d.warn, inner, "")))
	}
	l = append(l, "", m.researchHint(d))
	return m.dialog(g.T("Research · ")+d.ref.String(), l, dw)
}

func (m model) researchHint(d *notesDialog) string {
	rs := d.rs
	switch {
	case rs.stage == rsStarting:
		return keys("esc", "close")
	case rs.stage == rsAsk:
		return keys("enter", "send", "esc", "cancel")
	case !rs.have:
		return keys("enter", "start in the background", "esc", "close")
	}
	switch rs.job.State {
	case records.ResearchRunning:
		return keys("r", "check again", "esc", "close (it keeps running)")
	case records.ResearchFailed:
		return keys("enter", "try again", "esc", "close")
	}
	return keys("j/k", "scroll", "n", "ask again ($)", "esc", "close")
}

// researchBody is what the dialog says about the stored job.
func (m model) researchBody(d *notesDialog, inner, rows int) []string {
	g := m.g
	rs := d.rs
	if !rs.have {
		return []string{
			section("Research", g.T("none yet"), inner, g),
			stFaint.Render(ansi.Wrap(g.T("Asks the model about this issue (its discussion, links, merge requests and your notes; it does not read code) in the background. The answer is kept in the item's record and never bought twice for an unchanged issue."), inner, "")),
		}
	}
	j := rs.job
	switch j.State {
	case records.ResearchRunning:
		return []string{
			section("Research", g.T("running"), inner, g),
			stFaint.Render(ansi.Wrap(fmt.Sprintf(g.T("%s Running since %s. Closing this or the dashboard does not stop it; a notification says when it is done."), g.Sync, j.Started.Local().Format("15:04")), inner, "")),
		}
	case records.ResearchFailed:
		out := []string{section("Research", g.T("failed"), inner, g), stWarn.Render(ansi.Wrap(g.Warn+" "+j.Error, inner, ""))}
		return out
	}
	guide := "done " + j.Finished.Local().Format("2 Jan 15:04")
	switch {
	case j.CacheHit:
		guide += " " + g.Sep + " cached, $0"
	case j.CostUSD > 0:
		guide += fmt.Sprintf(" %s $%.4f", g.Sep, j.CostUSD)
	}
	if j.Model != "" {
		guide += " " + g.Sep + " " + j.Model
	}
	out := []string{section("Research", ansi.Truncate(guide, max(10, inner-12), g.Ell), inner, g)}
	lines := m.mdLines(j.Text, inner)
	if len(lines) == 0 {
		return append(out, stFaint.Render(g.T("the answer's file could not be read")))
	}
	d.scroll = min(d.scroll, max(len(lines)-rows, 0))
	end := min(len(lines), d.scroll+rows)
	if d.scroll > 0 {
		out = append(out, stFaint.Render(fmt.Sprintf("%s %d %s above", g.Up, d.scroll, plural(d.scroll, "line"))))
	}
	out = append(out, lines[d.scroll:end]...)
	if end < len(lines) {
		out = append(out, stFaint.Render(fmt.Sprintf(g.T("%s %d more %s · j scrolls"), g.Down, len(lines)-end, plural(len(lines)-end, "line"))))
	}
	return out
}

// --- the side panel ----------------------------------------------------------------

// loadResearch reads each listed item's research job, once per refresh, like
// loadNotes: Home's detail pane reads the map, never the files.
func (m *model) loadResearch() {
	m.research = map[string]records.Research{}
	if m.app == nil {
		return
	}
	svc := m.researchService()
	for _, r := range m.results {
		if r.Forge == "" {
			continue
		}
		for i := range r.Items {
			if v, err := svc.ReadResearch(r.Forge, r.Items[i].Ref); err == nil {
				m.research[markKey(r.Project, r.Items[i].Ref.String())] = v.Research
			}
		}
	}
}

// researchLines is the detail pane's research block: where the item's research
// stands. Nothing when it has none.
func (m model) researchLines(it *domain.WorkItem, w int) []string {
	r, ok := m.research[markKey(it.Project, it.Ref.String())]
	if !ok {
		return nil
	}
	g := m.g
	var s string
	switch r.State {
	case records.ResearchRunning:
		s = stFaint.Render(g.Sync + " running since " + r.Started.Local().Format("15:04"))
	case records.ResearchFailed:
		s = stWarn.Render(g.Warn + " failed " + g.Sep + " R tries again")
	default:
		s = stOK.Render(g.OK) + " done " + r.Finished.Local().Format("2 Jan 15:04") + stFaint.Render(" "+g.Sep+" R shows it")
	}
	return []string{"", stBold.Render("research"), ansi.Truncate(s, w, g.Ell)}
}
