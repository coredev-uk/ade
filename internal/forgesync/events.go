package forgesync

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/project"
)

// Event kinds raised by a sync.
const (
	EvIssueAssigned     = "issue.assigned"
	EvIterationMoved    = "issue.iteration_changed"
	EvCIFailed          = "ci.failed"
	EvCIPassed          = "ci.passed"
	EvCICanceled        = "ci.canceled"
	EvMRComment         = "mr.comment"
	EvMRApproved        = "mr.approved"
	EvMRChangesAsked    = "mr.changes_requested"
	EvMRMerged          = "mr.merged"
	EvReviewAsked       = "mr.review_requested"
	EvWorktreeRemovable = "worktree.removable"
	EvSyncDone          = "sync.done"
	EvSyncFailed        = "sync.failed"
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
			out = append(out, mrEvents(cur.Project, cur.Me.Username, wi, mr, oldMRs)...)
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

// mergedMR is an MR that merged between two syncs and the work item it
// belongs to.
type mergedMR struct {
	item forge.Ref
	mr   forge.MR
}

// mergedMRs lists MRs that were open at old and are merged at cur, whoever
// merged them (mine included: removing the worktree is still on offer). They
// are what worktree.removable is about.
func mergedMRs(old, cur Result) []mergedMR {
	was := map[forge.Ref]string{}
	for _, wi := range old.Items {
		for _, mr := range wi.MRs {
			was[mr.Ref] = mr.State
		}
	}
	var out []mergedMR
	seen := map[forge.Ref]bool{}
	for _, wi := range cur.Items {
		for _, mr := range wi.MRs {
			if mr.State == mrMerged && was[mr.Ref] != "" && was[mr.Ref] != mrMerged && !seen[mr.Ref] {
				seen[mr.Ref] = true
				out = append(out, mergedMR{item: wi.Ref, mr: mr})
			}
		}
	}
	return out
}

// removableEvents offers to remove the local worktree each merged MR was made
// from, when the guard (clean, pushed) allows it. The detail carries the exact
// commands; ade gc applies them to every such worktree at once.
func removableEvents(ctx context.Context, ps *project.Service, name string, merged []mergedMR) []events.Event {
	var out []events.Event
	fetch := ps.NewFetcher()
	for _, m := range merged {
		t, ok, err := ps.TreeOf(ctx, name, m.mr, fetch)
		if err != nil || !ok || !t.Removable() {
			continue
		}
		e := newEvent(name, EvWorktreeRemovable, events.Info, m.item.String(),
			fmt.Sprintf("%s merged: worktree %s can go · run ade gc --yes", mrLabel(m.mr.Ref), t.Label()), m.mr.WebURL)
		e.Detail = fmt.Sprintf("worktree %s is clean and pushed.\nremove all such worktrees: ade gc --yes   (preview: ade gc --dry-run)\nor just this one: %s", t.Path, t.RemoveCommand())
		out = append(out, e)
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
// freshly appeared MR. Nothing that me did themselves is echoed back.
func mrEvents(project, me string, wi domain.WorkItem, mr forge.MR, oldMRs map[forge.Ref]forge.MR) []events.Event {
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
	case known && now == "canceled" && was != "canceled":
		emit(EvCICanceled, events.Warn, "pipeline canceled on "+label)
	}
	if !known {
		return out
	}
	if title := newComments(mr, prev, me, label); title != "" {
		emit(EvMRComment, events.Info, title)
	}
	if names := newcomers(mr.Approvals.ApprovedBy, prev.Approvals.ApprovedBy, me); len(names) > 0 {
		emit(EvMRApproved, events.Success, fmt.Sprintf("%s approved by %s", label, strings.Join(names, ", ")))
	}
	if names := newcomers(mr.ChangesRequestedBy, prev.ChangesRequestedBy, me); len(names) > 0 {
		emit(EvMRChangesAsked, events.Warn, fmt.Sprintf("changes requested on %s by %s", label, strings.Join(names, ", ")))
	}
	if mr.State == mrMerged && prev.State != mrMerged && (me == "" || mr.MergedBy != me) {
		title := label + " merged"
		if mr.MergedBy != "" {
			title += " by " + mr.MergedBy
		}
		emit(EvMRMerged, events.Success, title)
	}
	return out
}

const mrMerged = "merged"

// newcomers lists, by display name, the users in cur that prev lacked
// (matched by username), leaving out me: what I did myself is not news.
func newcomers(cur, prev []forge.User, me string) []string {
	var out []string
	for _, u := range cur {
		if u.Username != me && !slices.ContainsFunc(prev, func(p forge.User) bool { return p.Username == u.Username }) {
			out = append(out, u.Display())
		}
	}
	return out
}

// newComments words the conversation that others added since prev: new
// threads and replies counted per discussion, by note author. Where either
// sync lacks thread data (an old cache, a bare list payload) only a rise in
// the resolvable-thread count can be seen. It returns "" for nothing new.
func newComments(mr, prev forge.MR, me, label string) string {
	if mr.Threads == nil || prev.Threads == nil {
		if mr.Unresolved+mr.Resolved > prev.Unresolved+prev.Resolved {
			return "new thread on " + label
		}
		return ""
	}
	had := make(map[string]int, len(prev.Threads))
	for _, t := range prev.Threads {
		had[t.ID] = len(t.Authors)
	}
	threads, replies := 0, 0
	for _, t := range mr.Threads {
		n, existed := had[t.ID]
		fresh := 0
		for _, a := range t.Authors[min(n, len(t.Authors)):] {
			if a.Username != me {
				fresh++
			}
		}
		switch {
		case fresh == 0:
		case existed:
			replies += fresh
		default:
			threads++
		}
	}
	switch {
	case threads+replies == 0:
		return ""
	case threads == 1 && replies == 0:
		return "new thread on " + label
	case threads > 1 && replies == 0:
		return fmt.Sprintf("%d new threads on %s", threads, label)
	case threads == 0 && replies == 1:
		return "new reply on " + label
	case threads == 0:
		return fmt.Sprintf("%d new replies on %s", replies, label)
	}
	reply := "1 reply"
	if replies != 1 {
		reply = fmt.Sprintf("%d replies", replies)
	}
	return fmt.Sprintf("%s and %s on %s", plural(threads, "new thread"), reply, label)
}

func pipelineStatus(mr forge.MR) string {
	if mr.Pipeline == nil {
		return ""
	}
	return mr.Pipeline.Status
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
