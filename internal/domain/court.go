package domain

import (
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
)

const blockedValue = "blocked"

// blockedReason says why the tracker is blocked, or "" when it is not: a label
// equal to "blocked", a workflow label whose value is "blocked", or an
// is_blocked_by link.
func blockedReason(tracker forge.Issue, links []forge.Link, l config.Labels) string {
	for _, label := range tracker.Labels {
		if strings.EqualFold(label, blockedValue) {
			return "label " + label
		}
		if v, ok := matchPattern(l.Workflow.Pattern, label); ok && strings.EqualFold(v, blockedValue) {
			return "label " + label
		}
	}
	for _, link := range links {
		if link.Type == "is_blocked_by" {
			return "blocked by " + link.Ref.String()
		}
	}
	return ""
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// mrVerdict says who must act on one open MR. When you are not its author the
// actions that would be yours belong to the author.
func mrVerdict(mr forge.MR, mine bool) (Court, string) {
	act := CourtYou
	if !mine {
		act = CourtAuthor
	}
	status := pipelineStatus(mr)
	switch {
	case status == "failed":
		return act, "pipeline failed"
	case mr.Unresolved > 0:
		return act, plural(mr.Unresolved, "unresolved thread", "unresolved threads")
	case mr.Draft:
		return act, "draft MR · mark ready"
	case mr.HasConflicts:
		return act, "merge conflicts"
	case pipelineBusy(status):
		return CourtCI, "pipeline " + status
	}
	got, req := len(mr.Approvals.ApprovedBy), mr.Approvals.Required
	switch {
	case req > 0 && got < req:
		return CourtReviewers, fmt.Sprintf("%d of %d approvals", got, req)
	case req < 0 && got == 0:
		return CourtReviewers, "waiting on review"
	}
	return act, "ready to merge"
}

func isMine(mr forge.MR, me string) bool {
	return me == "" || mr.Author.Username == "" || strings.EqualFold(mr.Author.Username, me)
}

// repoName maps a forge project path to its manifest repo name.
func repoName(m *config.Manifest, project string) string {
	for _, name := range m.RepoNames() {
		if strings.EqualFold(m.Repos[name].Path, project) {
			return name
		}
	}
	return ""
}

// upstreamOf returns the open MR that mr must wait for: an open MR in a repo
// that mr's repo requires.
func upstreamOf(m *config.Manifest, mr forge.MR, open []forge.MR) (forge.Ref, bool) {
	repo := repoName(m, mr.Ref.Project)
	if repo == "" {
		return forge.Ref{}, false
	}
	for _, req := range m.Repos[repo].Requires {
		for _, other := range open {
			if other.Ref != mr.Ref && repoName(m, other.Ref.Project) == req {
				return other.Ref, true
			}
		}
	}
	return forge.Ref{}, false
}

// focusMR picks the MR that decides the court: the least advanced open MR,
// preferring (on a tie) one that waits upstream so the dependency stays visible.
func focusMR(m *config.Manifest, open []forge.MR) (forge.MR, *forge.Ref) {
	var best forge.MR
	var bestUp *forge.Ref
	for i, mr := range open {
		var up *forge.Ref
		if ref, ok := upstreamOf(m, mr, open); ok {
			up = &ref
		}
		switch {
		case i == 0,
			mrStage(mr) < mrStage(best),
			mrStage(mr) == mrStage(best) && up != nil && bestUp == nil:
			best, bestUp = mr, up
		}
	}
	return best, bestUp
}

// deriveCourt decides who must act next and why. Order: done, blocked, MR
// state (focus MR), merged-but-open tracker, then pre-MR work.
func deriveCourt(item *WorkItem, m *config.Manifest, me, blocked string) (court Court, reason string, upstream *forge.Ref) {
	if item.Stage == StageDone {
		if item.KeptOpen {
			return CourtNobody, "done · issue kept open", nil
		}
		return CourtNobody, "closed", nil
	}
	if blocked != "" {
		return CourtBlocked, blocked, nil
	}
	if open := openMRs(item.MRs); len(open) > 0 {
		mr, up := focusMR(m, open)
		if up != nil {
			return CourtUpstream, "waits on " + up.String(), up
		}
		court, reason = mrVerdict(mr, isMine(mr, me))
		return court, reason, nil
	}
	switch {
	case item.Stage == StageMerged && m.Labels.Workflow.Done != "":
		return CourtYou, "set " + WorkflowLabel(m.Labels.Workflow.Done, m.Labels), nil
	case item.Stage == StageMerged:
		return CourtYou, "close tracker", nil
	case !item.Assigned && len(item.Issue.Assignees) > 0:
		return CourtNobody, "assigned to " + assigneeNames(item.Issue.Assignees), nil
	case !item.Assigned:
		return CourtNobody, "unassigned", nil
	case item.Stage == StageBranch:
		return CourtYou, "branch pushed · open MR", nil
	case item.Stage == StageStarted:
		return CourtYou, "no MR yet", nil
	}
	return CourtYou, "start work", nil
}

func assigneeNames(us []forge.User) string {
	names := make([]string, len(us))
	for i, u := range us {
		names[i] = u.Display()
	}
	return strings.Join(names, ", ")
}
