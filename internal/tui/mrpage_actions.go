package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// Writes. Each runs off the UI loop through mrWrite: the forge call goes
// through review.Audited (every write leaves a forge.* audit entry), and the
// outcome comes back as a pop-up. One write runs at a time per MR.

// mrWrite starts a write on d. verb is the action in progress ("merging"):
// shown in the status strip while it runs and in the failure pop-up.
func (m *model) mrWrite(d *mrData, what, verb string, fn func(ctx context.Context, w forge.MRWriter) (mrWrote, error)) tea.Cmd {
	if d.busy != "" {
		m.notice(events.Warn, "Still "+d.busy+" "+d.ref.String()+": wait for it to finish")
		return nil
	}
	d.busy = verb
	return m.mrRun(d, what, verb, func(ctx context.Context, be review.Backend) (any, error) {
		w, err := fn(ctx, review.Audited(be, m.mrAudit(), false))
		return w, err
	})
}

func (m *model) mrAudit() *audit.Log {
	if m.app == nil {
		return nil
	}
	return m.app.Audit
}

func (m *model) actReply(discID, body string) tea.Cmd {
	d := m.mr.data
	return m.mrWrite(d, "reply", "replying", func(ctx context.Context, w forge.MRWriter) (mrWrote, error) {
		if _, err := w.Reply(ctx, d.ref, discID, body); err != nil {
			return mrWrote{}, err
		}
		return mrWrote{done: "Reply posted on " + d.ref.String(), reload: relThreads | relMR}, nil
	})
}

func (m *model) actResolve(disc forge.Discussion) tea.Cmd {
	d := m.mr.data
	want := !disc.Resolved
	verb, done := "resolving", "Thread resolved on "
	if !want {
		verb, done = "reopening", "Thread reopened on "
	}
	return m.mrWrite(d, "resolve", verb, func(ctx context.Context, w forge.MRWriter) (mrWrote, error) {
		if err := w.Resolve(ctx, d.ref, disc.ID, want); err != nil {
			return mrWrote{}, err
		}
		return mrWrote{done: done + d.ref.String(), reload: relThreads | relMR}, nil
	})
}

// approvedByMe reports whether the signed-in user's approval stands on mr.
func (m *model) approvedByMe(mr forge.MR) bool {
	me := m.me().Username
	for _, u := range mr.Approvals.ApprovedBy {
		if me != "" && u.Username == me {
			return true
		}
	}
	return false
}

// actApprove approves the reviewed head, or withdraws your approval.
func (m *model) actApprove() tea.Cmd {
	d := m.mr.data
	if d.mr.State != "opened" {
		m.notice(events.Warn, d.ref.String()+" is "+d.mr.State+": nothing to approve")
		return nil
	}
	if m.approvedByMe(d.mr) {
		return m.mrWrite(d, "unapprove", "withdrawing approval", func(ctx context.Context, w forge.MRWriter) (mrWrote, error) {
			if err := w.Unapprove(ctx, d.ref); err != nil {
				return mrWrote{}, err
			}
			return mrWrote{done: "Approval withdrawn on " + d.ref.String(), reload: relMR}, nil
		})
	}
	sha := d.mr.HeadSHA
	return m.mrWrite(d, "approve", "approving", func(ctx context.Context, w forge.MRWriter) (mrWrote, error) {
		if err := w.Approve(ctx, d.ref, sha); err != nil {
			return mrWrote{}, err
		}
		return mrWrote{done: "Approved " + d.ref.String(), reload: relMR}, nil
	})
}

// actReady flips Draft and Ready.
func (m *model) actReady() tea.Cmd {
	d := m.mr.data
	if d.mr.State != "opened" {
		m.notice(events.Warn, d.ref.String()+" is "+d.mr.State)
		return nil
	}
	draft := !d.mr.Draft
	verb, done := "marking ready", " marked ready"
	if draft {
		verb, done = "marking draft", " marked draft"
	}
	return m.mrWrite(d, "ready", verb, func(ctx context.Context, w forge.MRWriter) (mrWrote, error) {
		if err := w.SetDraft(ctx, d.ref, draft); err != nil {
			return mrWrote{}, err
		}
		return mrWrote{done: d.ref.String() + done, reload: relMR}, nil
	})
}

func (m *model) actRetry() tea.Cmd {
	d := m.mr.data
	if d.mr.Pipeline == nil {
		m.notice(events.Info, d.ref.String()+" has no pipeline to retry")
		return nil
	}
	id := d.mr.Pipeline.ID
	return m.mrWrite(d, "retry", "retrying the pipeline", func(ctx context.Context, w forge.MRWriter) (mrWrote, error) {
		if err := w.RetryPipeline(ctx, d.ref.Project, id); err != nil {
			return mrWrote{}, err
		}
		return mrWrote{done: fmt.Sprintf("Retrying failed jobs of pipeline #%d on %s", id, d.ref), reload: relMR | relJobs}, nil
	})
}

// actMerge merges the head the page showed: the forge refuses when the branch
// has moved since. With a commit prefix the merge and squash messages carry it.
func (m *model) actMerge(o forge.MergeOpts, prefix string) tea.Cmd {
	d := m.mr.data
	o.SHA = d.mr.HeadSHA
	o = review.MergeOptsFor(o, prefix, d.mr)
	verb, done := "merging", "Merged %s into %s"
	if o.AutoMerge {
		verb, done = "setting auto-merge", "%s will merge into %s when its pipeline succeeds"
	}
	target := d.mr.TargetBranch
	return m.mrWrite(d, "merge", verb, func(ctx context.Context, w forge.MRWriter) (mrWrote, error) {
		if err := w.Merge(ctx, d.ref, o); err != nil {
			return mrWrote{}, err
		}
		return mrWrote{done: fmt.Sprintf(done, d.ref, target), reload: relMR | relThreads}, nil
	})
}

// actSubmit sends the review as one batch (GitLab: draft notes, then one bulk
// publish), then approves when asked. It opens a fresh session, so comment
// positions come from the MR's latest version and the drafts from the store
// (including ones another ade process added).
func (m *model) actSubmit(summary string, approve bool) tea.Cmd {
	d := m.mr.data
	st := m.reviews
	return m.mrWrite(d, "submit", "submitting the review", func(ctx context.Context, w forge.MRWriter) (mrWrote, error) {
		s, err := review.Open(ctx, w, st, d.ref)
		if err != nil {
			return mrWrote{}, err
		}
		res, err := s.Submit(ctx, w, summary, approve, review.WithAudit(m.mrAudit()))
		if err != nil {
			return mrWrote{}, err
		}
		return mrWrote{done: submittedTitle(d.ref, res), reload: relThreads | relMR, result: &res}, nil
	})
}

func submittedTitle(ref forge.Ref, res review.Result) string {
	var parts []string
	if res.Published > 0 {
		parts = append(parts, fmt.Sprintf("%d %s in one batch", res.Published, plural(res.Published, "comment")))
	}
	if res.Approved {
		parts = append(parts, "approved")
	}
	if len(parts) == 0 {
		parts = append(parts, "nothing to publish")
	}
	return fmt.Sprintf("Review submitted on %s: %s", ref, strings.Join(parts, ", "))
}

// actLog reads the tail of a job's log for the viewer.
func (m *model) actLog(job forge.Job) tea.Cmd {
	d := m.mr.data
	m.mr.modal = &mrModal{kind: modalLog, job: job, logLoading: true}
	return m.mrRunMsg(d, mrResultMsg{what: "log", job: job.ID}, func(ctx context.Context, be review.Backend) (any, error) {
		txt, err := be.JobLog(ctx, d.ref.Project, job.ID, logTail)
		return jobLog{job: job, text: txt}, err
	})
}

type jobLog struct {
	job  forge.Job
	text string
}

// --- merge readiness -------------------------------------------------------------

// verdict says whether a merge can go ahead, and why not.
type verdict struct {
	ok     bool
	autoOK bool   // not mergeable yet, but "merge when the pipeline succeeds" is
	why    string // why not, as the dialog words it
}

func mergeVerdict(mr forge.MR) verdict {
	switch {
	case mr.State == "merged":
		return verdict{why: "already merged"}
	case mr.State != "opened":
		return verdict{why: "the merge request is " + mr.State}
	case mr.Draft:
		return verdict{why: "it is still a draft (R marks it ready)"}
	case mr.HasConflicts:
		return verdict{why: "it conflicts with " + mr.TargetBranch + ": rebase first"}
	}
	switch mr.DetailedStatus {
	case "", "mergeable":
		return verdict{ok: true}
	case "ci_still_running":
		return verdict{autoOK: true, why: "the pipeline is still running"}
	case "ci_must_pass":
		return verdict{autoOK: mr.Pipeline != nil && mr.Pipeline.Status != "failed", why: "the pipeline must pass first"}
	case "not_approved":
		return verdict{why: "it needs approval"}
	case "discussions_not_resolved":
		return verdict{why: fmt.Sprintf("%d unresolved %s", mr.Unresolved, plural(mr.Unresolved, "thread"))}
	case "need_rebase":
		return verdict{why: "it needs a rebase"}
	case "checking", "unchecked":
		return verdict{why: "GitLab is still checking whether it can merge: try again in a moment"}
	case "blocked_status":
		return verdict{why: "another merge request blocks it"}
	}
	return verdict{why: "GitLab reports " + strings.ReplaceAll(mr.DetailedStatus, "_", " ")}
}

// approvalsText reads "1 of 2" (or "1 approval" where the forge cannot say how
// many are required).
func approvalsText(mr forge.MR) string {
	n := len(mr.Approvals.ApprovedBy)
	if mr.Approvals.Required >= 0 {
		return fmt.Sprintf("%d of %d approvals", n, mr.Approvals.Required)
	}
	return fmt.Sprintf("%d %s", n, plural(n, "approval"))
}
