package forgesync

import (
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
)

// Event kinds raised by a sync.
const (
	EvIssueAssigned  = "issue.assigned"
	EvIterationMoved = "issue.iteration_changed"
	EvCIFailed       = "ci.failed"
	EvCIPassed       = "ci.passed"
	EvMRComment      = "mr.comment"
	EvMRApproved     = "mr.approved"
	EvMRMerged       = "mr.merged"
	EvReviewAsked    = "mr.review_requested"
	EvSyncDone       = "sync.done"
	EvSyncFailed     = "sync.failed"
)

// diff lists what changed between two syncs of the same project.
func diff(old, cur Result) []events.Event {
	oldItems := map[string]domain.WorkItem{}
	oldMRs := map[forge.Ref]forge.MR{}
	for _, wi := range old.Items {
		oldItems[wi.Ref.String()] = wi
		for _, mr := range wi.MRs {
			oldMRs[mr.Ref] = mr
		}
	}
	for _, mr := range old.Reviews {
		if _, ok := oldMRs[mr.Ref]; !ok {
			oldMRs[mr.Ref] = mr
		}
	}

	var out []events.Event
	seen := map[forge.Ref]bool{}
	for _, wi := range cur.Items {
		prev, known := oldItems[wi.Ref.String()]
		out = append(out, itemEvents(cur.Project, wi, prev, known)...)
		for _, mr := range wi.MRs {
			if seen[mr.Ref] {
				continue
			}
			seen[mr.Ref] = true
			out = append(out, mrEvents(cur.Project, wi, mr, oldMRs)...)
		}
	}
	oldReviews := map[forge.Ref]bool{}
	for _, mr := range old.Reviews {
		oldReviews[mr.Ref] = true
	}
	for _, mr := range cur.Reviews {
		if !oldReviews[mr.Ref] {
			e := newEvent(cur.Project, EvReviewAsked, events.Info, mr.Ref.String(),
				fmt.Sprintf("review requested on %s: %s", mrLabel(mr.Ref), mr.Title), mr.WebURL)
			out = append(out, e)
		}
	}
	return out
}

func newEvent(project, kind string, sev events.Severity, subject, title, link string) events.Event {
	e := events.New(kind, sev, title)
	e.Project, e.Subject, e.Link = project, subject, link
	return e
}

func itemEvents(project string, wi domain.WorkItem, prev domain.WorkItem, known bool) []events.Event {
	var out []events.Event
	subject := wi.Ref.String()
	if wi.Assigned && (!known || !prev.Assigned) {
		out = append(out, newEvent(project, EvIssueAssigned, events.Info, subject,
			fmt.Sprintf("assigned to you: %s", wi.Issue.Title), wi.Issue.WebURL))
	}
	if known && sprintKey(prev) != sprintKey(wi) {
		out = append(out, newEvent(project, EvIterationMoved, events.Info, subject,
			fmt.Sprintf("%s moved to %s", wi.Issue.Title, sprintTitle(wi)), wi.Issue.WebURL))
	}
	return out
}

func sprintKey(wi domain.WorkItem) string {
	if wi.Sprint == nil {
		return ""
	}
	return wi.Sprint.Key
}

func sprintTitle(wi domain.WorkItem) string {
	if wi.Sprint == nil {
		return "no sprint"
	}
	return wi.Sprint.Title
}

// mrEvents compares one MR with its state at the previous sync. Changes need
// a previous state, except a failing pipeline, which is news even on a
// freshly appeared MR.
func mrEvents(project string, wi domain.WorkItem, mr forge.MR, oldMRs map[forge.Ref]forge.MR) []events.Event {
	prev, known := oldMRs[mr.Ref]
	subject := wi.Ref.String()
	label := mrLabel(mr.Ref)
	var out []events.Event
	emit := func(kind string, sev events.Severity, title string) {
		out = append(out, newEvent(project, kind, sev, subject, title, mr.WebURL))
	}

	now, was := pipelineStatus(mr), pipelineStatus(prev)
	switch {
	case now == "failed" && was != "failed":
		emit(EvCIFailed, events.Error, "pipeline failed on "+label)
	case known && now == "success" && was != "success":
		emit(EvCIPassed, events.Success, "pipeline passed on "+label)
	}
	if !known {
		return out
	}
	if mr.Unresolved+mr.Resolved > prev.Unresolved+prev.Resolved {
		emit(EvMRComment, events.Info, "new thread on "+label)
	}
	if n := len(mr.Approvals.ApprovedBy); n > len(prev.Approvals.ApprovedBy) {
		emit(EvMRApproved, events.Success, fmt.Sprintf("%s approved by %s", label, approvers(mr, prev)))
	}
	if mr.State == "merged" && prev.State != "merged" {
		emit(EvMRMerged, events.Success, label+" merged")
	}
	return out
}

func pipelineStatus(mr forge.MR) string {
	if mr.Pipeline == nil {
		return ""
	}
	return mr.Pipeline.Status
}

// approvers names the users who approved since prev.
func approvers(mr, prev forge.MR) string {
	had := map[string]bool{}
	for _, u := range prev.Approvals.ApprovedBy {
		had[u.Username] = true
	}
	var names []string
	for _, u := range mr.Approvals.ApprovedBy {
		if !had[u.Username] {
			names = append(names, u.Username)
		}
	}
	return strings.Join(names, ", ")
}

// mrLabel is "api!1": the last path segment of the project and the MR number.
func mrLabel(ref forge.Ref) string {
	proj := ref.Project
	if i := strings.LastIndex(proj, "/"); i >= 0 {
		proj = proj[i+1:]
	}
	return fmt.Sprintf("%s!%d", proj, ref.IID)
}

// summary is the end-of-run event: a transient status line, or a recorded
// warning (some projects failed) or error (all did). A failure that is the
// same as last run's is transient too: a project that keeps failing (an
// unsupported forge, an expired token) is news once, not every interval.
func summary(results []Result, prev map[string]Result) events.Event {
	items, failed, fresh := 0, 0, 0
	var details []string
	for _, r := range results {
		items += len(r.Items)
		if r.Err != "" {
			failed++
			details = append(details, r.Project+": "+r.Err)
			if p, ok := prev[r.Project]; !ok || p.Err != r.Err {
				fresh++
			}
		}
	}
	if failed == 0 {
		e := events.New(EvSyncDone, events.Info, fmt.Sprintf("synced %s · %s", plural(len(results), "project"), plural(items, "item")))
		e.Transient = true
		return e
	}
	sev := events.Warn
	if failed == len(results) {
		sev = events.Error
	}
	e := events.New(EvSyncFailed, sev, fmt.Sprintf("sync failed for %d of %s", failed, plural(len(results), "project")))
	e.Detail = strings.Join(details, "\n")
	e.Transient = fresh == 0
	return e
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
