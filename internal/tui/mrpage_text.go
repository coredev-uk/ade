package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// Two more AI actions on the MR page, both through ai.Service like the rest
// (`ade ai describe`, `ade ai ci-triage`): pre-flight, caps, cache, audit.
//
//   - B drafts the MR's description from its template, commits and diff. The
//     draft opens in an editor dialog; enter writes it to the MR (a forge write,
//     audited as forge.mr.description), esc throws it away. Nothing reaches the
//     forge without that enter.
//   - t on the CI tab asks for the cause and a fix of every failed job from the
//     tail of its log. The answer is shown under the job list; nothing is
//     written anywhere.

// textJob is what runTextJob needs from the page, copied off the UI loop.
type textJob struct {
	mr        forge.MR
	jobs      []forge.Job
	forgeName string
	refresh   bool
	assume    bool
}

// runTextJob reads what the job needs from the forge and runs it. It returns
// an aiAnswer, with pf set when the call waits for a yes.
func runTextJob(ctx context.Context, svc *ai.Service, a *app.App, be review.Backend, kind aiKind, t textJob) (any, error) {
	var job ai.Job
	var draftOf func(ai.Result) string
	switch kind {
	case aiDescribe:
		in, err := ai.GatherDescribe(ctx, a, be, t.mr.Ref, "")
		if err != nil {
			return nil, err
		}
		job = ai.DescribeJob(t.forgeName, in)
		draftOf = func(r ai.Result) string { return ai.PrepareDescription(r.Text, in.MR.Description) }
	case aiCITriage:
		in, err := ai.GatherCITriage(ctx, be, t.mr, t.jobs)
		if err != nil {
			return nil, err
		}
		job = ai.CITriageJob(t.forgeName, in)
	}
	job.Refresh, job.Assume = t.refresh, t.assume
	res, err := svc.Run(ctx, job)
	var nc *ai.ErrNeedsConfirm
	if errors.As(err, &nc) {
		return aiAnswer{pf: &nc.Preflight}, nil
	}
	if err != nil {
		return nil, err
	}
	ans := aiAnswer{res: res}
	if draftOf != nil {
		ans.draft = draftOf(res)
	}
	return ans, nil
}

// --- B: the MR description --------------------------------------------------------

// describeKey is B: draft the MR's description, or reopen the draft already
// made for this head.
func (m *model) describeKey() tea.Cmd {
	d := m.mr.data
	if d == nil || !m.aiOn() {
		return nil
	}
	if d.mr.State != "opened" {
		m.notice(events.Info, d.ref.String()+" is "+d.mr.State+": its description is left alone")
		return nil
	}
	k := aiKey{kind: aiDescribe}
	st := d.ai[k]
	switch {
	case st != nil && st.l.busy():
		return nil
	case st != nil && st.l.st == loadOK && st.text != "" && st.head == d.mr.HeadSHA:
		m.openDescribe(d, st)
		return nil
	}
	return m.aiAsk(d, k, false, false)
}

// deliverDescription opens a finished draft when the page still shows that MR.
func (m *model) deliverDescription(d *mrData, st *aiText, draft string) {
	st.text = draft
	if m.mr == nil || m.mr.data != d || m.mr.modal != nil {
		m.notice(events.Info, "AI description ready: B opens it")
		return
	}
	m.openDescribe(d, st)
}

// openDescribe shows the draft in the editor dialog.
func (m *model) openDescribe(d *mrData, st *aiText) {
	md := &mrModal{kind: modalDescribe, ed: newEditor(st.text)}
	md.aiNote = fmt.Sprintf(m.g.T("AI draft · %s · %s · edit it: enter writes it to the merge request, esc throws it away"), st.model, costWord(st))
	if st.cut != "" {
		md.aiCut = m.g.Warn + " " + st.cut + m.g.T(": it cannot be applied; ctrl+r asks again")
	}
	m.mr.modal = md
}

// describeModalKey handles a key in the description dialog.
func (m *model) describeModalKey(md *mrModal, k tea.KeyPressMsg) tea.Cmd {
	d := m.mr.data
	switch k.String() {
	case "enter":
		text := strings.TrimSpace(md.ed.String())
		switch {
		case md.aiCut != "":
			md.warn = "the draft was cut short: ctrl+r asks again"
		case text == "":
			md.warn = "the description is empty"
		case text == strings.TrimSpace(d.mr.Description):
			md.warn = "the merge request already has this description"
		default:
			m.mr.modal = nil
			return m.actDescribe(text)
		}
	case "ctrl+r":
		m.mr.modal = nil
		return m.aiAsk(d, aiKey{kind: aiDescribe}, true, false)
	default:
		md.ed.handle(k)
	}
	return nil
}

// actDescribe writes the description to the MR (forge.mr.description).
func (m *model) actDescribe(text string) tea.Cmd {
	d := m.mr.data
	return m.mrWrite(d, "describe", "updating the description", func(ctx context.Context, w forge.MRWriter) (mrWrote, error) {
		aw, ok := w.(*review.AuditedWriter)
		if !ok {
			return mrWrote{}, errors.New("the writer does not audit: not writing")
		}
		if err := aw.SetDescription(ctx, d.ref, text, "ai-describe"); err != nil {
			return mrWrote{}, err
		}
		return mrWrote{done: "Description updated on " + d.ref.String(), reload: relMR}, nil
	})
}

// describeDialog draws the draft's editor.
func (m model) describeDialog(md *mrModal, inner, h int) (string, []string) {
	var l []string
	if md.aiNote != "" {
		l = append(l, stFaint.Render(ansi.Wrap(md.aiNote, inner, "")))
	}
	if md.aiCut != "" {
		l = append(l, stWarn.Render(ansi.Wrap(md.aiCut, inner, "")))
	}
	rows := max(4, min(16, h-len(l)-12))
	l = append(l, "")
	l = append(l, md.ed.view(inner, rows, "the description")...)
	return m.g.T("MR description · ") + m.mr.data.ref.String(), l
}

// --- t on the CI tab: triage of failed jobs --------------------------------------------

// triageKey is t on the CI tab: ask for the cause and a fix of each failed job.
func (m *model) triageKey() tea.Cmd {
	d := m.mr.data
	if d == nil || !m.aiOn() {
		return nil
	}
	k := aiKey{kind: aiCITriage}
	st := d.ai[k]
	switch {
	case d.mr.Pipeline == nil:
		m.notice(events.Info, d.ref.String()+" has no pipeline")
		return nil
	case d.jobsL.st != loadOK:
		m.notice(events.Info, "The jobs are still being read: try again in a moment")
		return nil
	case st != nil && st.l.busy():
		return nil
	case st != nil && st.pf != nil:
		m.mr.modal = &mrModal{kind: modalAI, ai: k}
		return nil
	}
	failed := false
	for _, j := range d.jobs {
		failed = failed || j.Status == "failed"
	}
	if !failed {
		m.notice(events.Info, "No job of the pipeline failed: nothing to triage")
		return nil
	}
	return m.aiAsk(d, k, false, false) // unchanged logs are answered from the cache
}

// ciTriageLines is the triage block under the job list: the answer, or where
// the call stands.
func (m model) ciTriageLines(d *mrData, w int) []string {
	st := d.ai[aiKey{kind: aiCITriage}]
	if st == nil {
		return nil
	}
	return m.aiBlock("CI triage", st, w)
}
