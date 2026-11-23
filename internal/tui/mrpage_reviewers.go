package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// v on the MR page asks for a review again: after you pushed fixes and
// answered the threads, the reviewers who already looked get a fresh request
// (and the "ready again" notification) without a trip to the web page. The
// dialog lists who reviews and where each stands, then asks whom to ask: every
// current reviewer (tab toggles it) and anyone named in the field. enter
// writes (forge.mr.request_review); nothing is written before it. No AI is
// involved.

// reviewAsk is the Request review dialog's state.
type reviewAsk struct {
	loading bool             // the reviewers are being read
	err     string           // why they could not be (the dialog still asks for named users)
	revs    []forge.Reviewer // who reviews now
	again   bool             // also ask the current reviewers
}

// requestReviewKey is v: open the dialog and read who reviews.
func (m *model) requestReviewKey() tea.Cmd {
	d := m.mr.data
	if d.mr.State != "opened" {
		m.notice(events.Warn, d.ref.String()+" is "+d.mr.State+": no review to ask for")
		return nil
	}
	if d.busy != "" {
		m.notice(events.Warn, "Wait for "+d.busy+" to finish")
		return nil
	}
	m.mr.modal = &mrModal{kind: modalRequestReview, ed: newEditor(""), rr: &reviewAsk{loading: true, again: true}}
	return m.mrRunMsg(d, mrResultMsg{what: "reviewers"}, func(ctx context.Context, be review.Backend) (any, error) {
		rv, ok := be.(forge.MRReviewers)
		if !ok {
			return nil, errors.New("this forge cannot request reviews yet")
		}
		return rv.Reviewers(ctx, d.ref)
	})
}

// onReviewers files the answer of the reviewers read in the open dialog. A
// late answer for a dialog that was closed, or that is another MR's, is dropped.
func (m *model) onReviewers(msg mrResultMsg) {
	p := m.mr
	if p == nil || p.modal == nil || p.modal.kind != modalRequestReview || p.data == nil || p.data.ref != msg.ref {
		return
	}
	rr := p.modal.rr
	rr.loading = false
	if msg.err != nil {
		rr.err = shortCause(msg.err)
		return
	}
	rr.revs, _ = msg.out.([]forge.Reviewer)
}

// requestTargets is whom enter would ask now.
func (md *mrModal) requestTargets() []review.ReviewTarget {
	return review.PlanReviewRequest(md.rr.revs, review.ParseUsernames(md.ed.String()), md.rr.again)
}

func (m *model) requestReviewModalKey(md *mrModal, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		if md.rr.loading {
			md.warn = "still reading the reviewers"
			return nil
		}
		ts := md.requestTargets()
		if len(ts) == 0 {
			md.warn = "name whom to ask: @user"
			return nil
		}
		m.mr.modal = nil
		return m.actRequestReview(ts, md.rr.again)
	case "tab", "shift+tab":
		md.rr.again = !md.rr.again
	default:
		md.ed.handle(k)
	}
	return nil
}

// actRequestReview asks the targets to review (forge.mr.request_review).
func (m *model) actRequestReview(ts []review.ReviewTarget, again bool) tea.Cmd {
	d := m.mr.data
	users := review.Usernames(ts)
	return m.mrWrite(d, "request_review", "requesting review", func(ctx context.Context, w forge.MRWriter) (mrWrote, error) {
		aw, ok := w.(*review.AuditedWriter)
		if !ok {
			return mrWrote{}, errors.New("the writer does not audit: not writing")
		}
		if err := aw.RequestReview(ctx, d.ref, users, again); err != nil {
			return mrWrote{}, err
		}
		return mrWrote{done: fmt.Sprintf("Review requested from %s on %s", atList(users), d.ref), reload: relMR}, nil
	})
}

func atList(users []string) string {
	ats := make([]string, len(users))
	for i, u := range users {
		ats[i] = "@" + u
	}
	return strings.Join(ats, " ")
}

// reviewerWord is a reviewer state as the dialog says it.
func reviewerWord(state string) string {
	return strings.ReplaceAll(state, "_", " ")
}

func (m model) requestReviewDialog(md *mrModal, inner int) (string, []string) {
	rr := md.rr
	var l []string
	switch {
	case rr.loading:
		l = append(l, stFaint.Render(spinnerFrame(m.g, m.mr.spin)+m.g.T(" reading the reviewers…")))
	case rr.err != "":
		l = append(l, stWarn.Render(m.g.Warn+" could not read the reviewers: "+rr.err))
	case len(rr.revs) == 0:
		l = append(l, stFaint.Render("no reviewers yet"))
	default:
		l = append(l, stFaint.Render("reviewers now"))
		for _, r := range rr.revs {
			word := reviewerWord(r.State)
			row := "  " + r.User.Display() + stFaint.Render(m.g.T(" · ")+word)
			if r.State == forge.ReviewerChangesRequested {
				row = "  " + r.User.Display() + stWarn.Render(m.g.T(" · ")+word)
			}
			l = append(l, ansi.Truncate(row, inner, m.g.Ell))
		}
		l = append(l, "", checkbox(m.g, rr.again, fmt.Sprintf("tab  ask the %d current %s again", len(rr.revs), plural(len(rr.revs), "reviewer"))))
	}
	l = append(l, "", stFaint.Render("also ask (new reviewers are added, the others stay)"))
	l = append(l, md.ed.view(inner, 2, "@user @user")...)
	if ts := md.requestTargets(); len(ts) > 0 {
		l = append(l, "", stFaint.Render("will ask"))
		for _, t := range ts {
			text := "@" + t.Username + stFaint.Render(m.g.T(" · ")+"added as a reviewer")
			if t.Action == review.RequestAgain {
				text = "@" + t.Username + stFaint.Render(m.g.T(" · ")+reviewerWord(t.Was)+m.g.T(" → ")+"asked again")
			}
			l = append(l, ansi.Truncate("  "+text, inner, m.g.Ell))
		}
	}
	return m.g.T("Request review · ") + m.mr.data.ref.String(), l
}

func requestReviewHint() string {
	return keys("enter", "ask", "tab", "current reviewers", "esc", "cancel")
}
