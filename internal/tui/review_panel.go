package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// The review surface (docs/PLAN.md 6.9): one place for reviewing yourself and
// triaging an agent's drafts. Comments collect as drafts in cache.db (quitting
// loses nothing); nothing reaches the forge until S submits them as one batch.
// Your own drafts count as accepted; an agent's wait as pending until you
// accept, edit, or deny them. Denied drafts are never posted.

// triageOrder lists what needs a decision first: pending agent drafts, then
// everything that will be sent, then the denied.
func triageOrder(ds []review.Draft) []review.Draft {
	out := append([]review.Draft(nil), ds...)
	rank := func(d review.Draft) int {
		switch d.State {
		case review.StateDenied:
			return 2
		case review.StatePending:
			return 0
		}
		return 1
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

func pendingAgent(ds []review.Draft) int {
	n := 0
	for _, d := range ds {
		if d.Source == review.SourceAgent && d.State == review.StatePending {
			n++
		}
	}
	return n
}

func (m model) reviewGuide() string {
	d := m.mr.data
	n := len(d.drafts)
	if n == 0 {
		return "no drafts"
	}
	s := fmt.Sprintf("%d %s", n, plural(n, "draft"))
	if p := pendingAgent(d.drafts); p > 0 {
		s += fmt.Sprintf(" %s %d agent pending", m.g.Sep, p)
	}
	if k := len(sendable(d.drafts)); k > 0 {
		s += fmt.Sprintf(" %s %d will be sent", m.g.Sep, k)
	}
	return s
}

func draftLoc(dr review.Draft) string {
	switch {
	case dr.Overview():
		return "overview"
	case dr.Side == review.SideOld:
		return fmt.Sprintf("%s:%d (removed)", dr.Path, dr.Line)
	}
	return fmt.Sprintf("%s:%d", dr.Path, dr.Line)
}

func (m model) mrReview(w, h int) string {
	p := m.mr
	d := p.data
	g := m.g
	if len(d.drafts) == 0 {
		return fit("   "+stFaint.Render("No drafts yet. In the Diff tab, c comments on a line; agent review drafts land here to be accepted, edited, or denied.\n   Nothing reaches the forge until S submits."), w, h)
	}
	list := triageOrder(d.drafts)
	sel := clamp(p.revSel, 0, len(list)-1)
	var out []string
	selLine := 0
	for i, dr := range list {
		if i == sel {
			selLine = len(out)
		}
		out = append(out, m.draftRow(dr, i == sel, w))
		if i == sel {
			bar := "     " + stRule.Render(g.Vertical) + " "
			for j, ln := range strings.Split(lipgloss.Wrap(dr.Body, min(100, max(10, w-9)), " "), "\n") {
				if j == 6 {
					out = append(out, bar+stFaint.Render("…"))
					break
				}
				out = append(out, bar+ln)
			}
			if dr.Source == review.SourceAgent && dr.State == review.StatePending {
				out = append(out, bar+stFaint.Render("y accept · e edit · n deny · s severity"))
			}
			out = append(out, "")
		}
	}
	top := 0
	if selLine >= h-1 {
		top = selLine - h + 2
	}
	return fit(strings.Join(out[min(top, len(out)):], "\n"), w, h)
}

func (m model) draftRow(dr review.Draft, selected bool, w int) string {
	g := m.g
	mark := "   "
	if selected {
		mark = " " + stBold.Render(g.Selected) + " "
	}
	sev := pad(sevWord(dr.Severity), 10)
	if dr.Severity == review.SeverityBlocking {
		sev = stWarn.Render(pad(g.Warn+" blocking", 10))
	}

	var who string
	switch dr.State {
	case review.StatePending:
		who = stWarn.Bold(true).Render("agent · pending")
	case review.StateDenied:
		who = stFaint.Render(string(dr.Source) + " · denied")
	default:
		who = string(dr.Source) + " · accepted"
	}
	// The who column is right-aligned so it ends on the section rule's right
	// edge (the rule and the row are both w-1 cells).
	who = strings.Repeat(" ", max(0, 16-lipgloss.Width(who))) + who
	flag := ""
	if long(dr) {
		flag = " " + stWarn.Render("long")
	}
	loc := draftLoc(dr)
	room := w - 3 - 10 - 1 - 16 - 1 - 1
	loc = ansi.Truncate(loc, max(8, room-lipgloss.Width(flag)), "…")
	if selected {
		loc = stBold.Render(loc)
	}
	return mark + sev + " " + pad(loc+flag, max(8, room)) + " " + who
}

// --- adding, editing, triaging ---------------------------------------------------

// cursorLine is the diff line under the cursor in the Diff tab.
func (m model) cursorLine() (forge.DiffFile, review.Line, bool) {
	p := m.mr
	d := p.data
	if d == nil || d.diffL.st != loadOK || len(d.diff.Files) == 0 {
		return forge.DiffFile{}, review.Line{}, false
	}
	f := d.diff.Files[clamp(p.fileSel, 0, len(d.diff.Files)-1)]
	lines := review.Lines(f)
	if len(lines) == 0 {
		return f, review.Line{}, false
	}
	return f, lines[clamp(p.lineCur, 0, len(lines)-1)], true
}

// beginComment opens the comment dialog on the cursor line.
func (m *model) beginComment() {
	p := m.mr
	d := p.data
	f, l, ok := m.cursorLine()
	if !ok {
		m.notice(events.Info, "No diff line to comment on in this file")
		return
	}
	if d.busy != "" {
		m.notice(events.Warn, "Wait for "+d.busy+" to finish")
		return
	}
	if d.session == nil {
		m.notice(events.Info, "The diff is still loading")
		return
	}
	t := lineTarget{path: f.NewPath, line: l.New, side: review.SideNew, text: strings.TrimSpace(l.Text)}
	if l.Kind == '-' {
		t.line, t.side = l.Old, review.SideOld
	}
	p.modal = &mrModal{kind: modalComment, target: t}
}

// commitComment saves the comment dialog as a new draft, or the edit dialog as
// the edited one.
func (m *model) commitComment(md *mrModal) tea.Cmd {
	p := m.mr
	d := p.data
	body := strings.TrimSpace(md.ed.String())
	if body == "" {
		md.warn = "write the comment first"
		return nil
	}
	sev := severities[md.sev]
	if md.kind == modalComment {
		_, err := d.session.Add(review.Draft{
			Path: md.target.path, Line: md.target.line, Side: md.target.side, Body: body, Severity: sev,
		})
		if err != nil {
			md.warn = err.Error()
			return nil
		}
		d.refreshDrafts(m.reviews)
		p.modal = nil
		return nil
	}
	dr, ok := d.draft(md.draftID)
	if !ok {
		p.modal = nil
		return nil
	}
	if err := m.saveEdit(d, dr, body, sev); err != nil {
		md.warn = err.Error()
		return nil
	}
	p.modal = nil
	return nil
}

func (d *mrData) draft(id string) (review.Draft, bool) {
	for _, dr := range d.drafts {
		if dr.ID == id {
			return dr, true
		}
	}
	return review.Draft{}, false
}

// beginEdit opens the edit dialog for a draft. Editing an agent's draft
// accepts it.
func (m *model) beginEdit(dr review.Draft) {
	d := m.mr.data
	switch {
	case d.busy != "":
		m.notice(events.Warn, "Wait for "+d.busy+" to finish")
	case dr.RemoteID != 0:
		m.notice(events.Warn, "That draft was already uploaded by an interrupted submit: submit again, or deny it")
	default:
		m.mr.modal = &mrModal{
			kind: modalEditDraft, ed: newEditor(dr.Body), sev: sevIndex(dr.Severity), draftID: dr.ID,
			target: lineTarget{path: dr.Path, line: dr.Line, side: dr.Side},
		}
	}
}

// saveEdit stores the edited draft: an agent's becomes accepted.
func (m *model) saveEdit(d *mrData, dr review.Draft, body string, sev review.Severity) error {
	was := dr
	dr.Body, dr.Severity = body, sev
	if dr.Source == review.SourceAgent && dr.State == review.StatePending {
		dr.State = review.StateAccepted
	}
	dr, err := review.Normalize(dr, dr.CreatedAt)
	if err != nil {
		return err
	}
	if err := m.reviews.Put(d.ref, dr); err != nil {
		return err
	}
	d.refreshDrafts(m.reviews)
	m.auditDecision("review.comment.edit", d, dr, map[string]any{"from_severity": string(was.Severity), "to_severity": string(sev), "chars": len(body)})
	return nil
}

func (m *model) decide(dr review.Draft, st review.State, action, reason string) {
	d := m.mr.data
	if d.busy != "" {
		m.notice(events.Warn, "Wait for "+d.busy+" to finish")
		return
	}
	dr.State = st
	dr, err := review.Normalize(dr, dr.CreatedAt)
	if err == nil {
		err = m.reviews.Put(d.ref, dr)
	}
	if err != nil {
		m.notice(events.Error, "Could not save the decision: "+err.Error())
		return
	}
	d.refreshDrafts(m.reviews)
	extra := map[string]any{}
	if reason != "" {
		extra["reason"] = reason
	}
	m.auditDecision(action, d, dr, extra)
}

func (m *model) accept(dr review.Draft) {
	if dr.State != review.StateAccepted {
		m.decide(dr, review.StateAccepted, "review.comment.accept", "")
	}
}

func (m *model) acceptAll() {
	for _, dr := range m.mr.data.drafts {
		if dr.Source == review.SourceAgent && dr.State == review.StatePending {
			m.accept(dr)
		}
	}
}

// denyDraft denies a draft, with an optional one-word reason for the audit log.
func (m *model) denyDraft(id, reason string) tea.Cmd {
	if dr, ok := m.mr.data.draft(id); ok {
		m.decide(dr, review.StateDenied, "review.comment.deny", reason)
	}
	return nil
}

// removeDraft deletes a draft that never left this machine; one an
// interrupted submit already uploaded is denied instead, so the next submit
// takes it back from the forge.
func (m *model) removeDraft(dr review.Draft) {
	d := m.mr.data
	if d.busy != "" {
		m.notice(events.Warn, "Wait for "+d.busy+" to finish")
		return
	}
	if dr.RemoteID != 0 {
		m.decide(dr, review.StateDenied, "review.comment.deny", "removed")
		return
	}
	if err := m.reviews.Delete(d.ref, dr.ID); err != nil {
		m.notice(events.Error, "Could not delete the draft: "+err.Error())
		return
	}
	d.refreshDrafts(m.reviews)
	m.auditDecision("review.comment.delete", d, dr, nil)
}

func (m *model) cycleSeverity(dr review.Draft) {
	d := m.mr.data
	if d.busy != "" || dr.RemoteID != 0 {
		return
	}
	next := severities[(sevIndex(dr.Severity)+1)%len(severities)]
	if err := m.saveEdit(d, dr, dr.Body, next); err != nil {
		m.notice(events.Error, err.Error())
	}
}

// auditDecision records a triage decision: every accept, edit, deny and
// delete is audited, so a noisy reviewer model shows in the numbers.
func (m *model) auditDecision(action string, d *mrData, dr review.Draft, extra map[string]any) {
	log := m.mrAudit()
	if log == nil {
		return
	}
	params := map[string]any{
		"path": dr.Path, "line": dr.Line, "side": string(dr.Side), "severity": string(dr.Severity),
		"source": string(dr.Source), "state": string(dr.State),
	}
	for k, v := range extra {
		params[k] = v
	}
	_ = log.Append(audit.Entry{Action: action, Item: d.ref.String(), Project: d.project, Target: dr.ID, Params: params})
}

// draftsAtCursor lists the drafts anchored to the cursor line.
func (m model) draftsAtCursor() []review.Draft {
	f, l, ok := m.cursorLine()
	if !ok {
		return nil
	}
	var out []review.Draft
	for _, dr := range m.mr.data.drafts {
		if draftInFile(dr, f) && draftOnLine(dr, l) {
			out = append(out, dr)
		}
	}
	return out
}

// beginSubmit opens the submit dialog.
func (m *model) beginSubmit() {
	p := m.mr
	d := p.data
	if d == nil {
		return
	}
	if d.busy != "" {
		m.notice(events.Warn, "Wait for "+d.busy+" to finish")
		return
	}
	if d.mr.State != "opened" {
		m.notice(events.Warn, d.ref.String()+" is "+d.mr.State+": it takes no review")
		return
	}
	d.refreshDrafts(m.reviews)
	p.modal = &mrModal{kind: modalSubmit}
}
