package domain

import (
	"cmp"
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

// Urgency of an MR verdict within one court: lower is more pressing. It
// follows the order mrVerdict tests its cases in.
const (
	urgFailed = iota
	urgThreads
	urgDraft
	urgConflicts
	urgBusy
	urgReview
	urgReady
)

// mrVerdict says who must act on one open MR, and how pressing it is. When
// you are not its author the actions that would be yours belong to the author.
func mrVerdict(mr forge.MR, mine bool) (Court, string, int) {
	act := CourtYou
	if !mine {
		act = CourtAuthor
	}
	status := pipelineStatus(mr)
	switch {
	case status == "failed":
		return act, "pipeline failed", urgFailed
	case mr.Unresolved > 0:
		return act, plural(mr.Unresolved, "unresolved thread", "unresolved threads"), urgThreads
	case mr.Draft:
		return act, "draft MR · mark ready", urgDraft
	case mr.HasConflicts:
		return act, "merge conflicts", urgConflicts
	case pipelineBusy(status):
		return CourtCI, "pipeline " + status, urgBusy
	}
	got, req := len(mr.Approvals.ApprovedBy), mr.Approvals.Required
	switch {
	case req > 0 && got < req:
		return CourtReviewers, fmt.Sprintf("%d of %d approvals", got, req), urgReview
	case req < 0 && got == 0:
		return CourtReviewers, "waiting on review", urgReview
	}
	return act, "ready to merge", urgReady
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

// A candidate is one thing that may put the item in somebody's court: an open
// MR, or an open child of mine that has no MR yet.
type candidate struct {
	court   Court
	reason  string
	up      *forge.Ref // set when court is CourtUpstream
	ref     forge.Ref  // the MR or child the candidate is about
	prio    int        // lower wins
	urgency int        // lower wins within a prio
	stage   Stage      // lower wins within an urgency: the slower part
}

// before reports whether c outranks o; on a full tie the earlier one stays.
func (c candidate) before(o candidate) bool {
	return cmp.Or(cmp.Compare(c.prio, o.prio), cmp.Compare(c.urgency, o.urgency), cmp.Compare(c.stage, o.stage)) < 0
}

// Candidate priorities. The item sits with the court that can act soonest:
// your own move on an MR first, then a child still lacking its MR (still
// yours, but an MR you can already fix or merge is closer to done), then
// somebody else's move (the author's), then waits that no one can hurry (an
// upstream MR, a running pipeline, reviewers). This mirrors "you, upstream,
// ci, reviewers" of the single-MR rule: there too the dependent MR shows its
// wait only when it is not the one you can act on.
const (
	prioYou = iota
	prioNoMR
	prioAuthor
	prioUpstream
	prioCI
	prioReviewers
)

func mrCandidate(m *config.Manifest, mr forge.MR, open []forge.MR, me string) candidate {
	c := candidate{ref: mr.Ref, stage: mrStage(mr)}
	if up, ok := upstreamOf(m, mr, open); ok {
		c.court, c.reason, c.up, c.prio = CourtUpstream, "waits on "+up.String(), &up, prioUpstream
		return c
	}
	c.court, c.reason, c.urgency = mrVerdict(mr, isMine(mr, me))
	switch c.court {
	case CourtYou:
		c.prio = prioYou
	case CourtAuthor:
		c.prio = prioAuthor
	case CourtCI:
		c.prio = prioCI
	default:
		c.prio = prioReviewers
	}
	return c
}

// mrLess lists the open children that have neither an open nor a merged MR,
// in child order. Only items with several children have any: with one child
// the MR (or its absence) is the whole story.
func mrLess(item *WorkItem) []forge.Issue {
	if len(item.Children) < 2 {
		return nil
	}
	var out []forge.Issue
	for _, g := range item.MRsByChild() {
		c := g.Child
		if c == nil || c.State != "opened" || len(openMRs(g.MRs)) > 0 || anyMerged(g.MRs) {
			continue
		}
		out = append(out, *c)
	}
	return out
}

// needMR is mrLess narrowed to the children of mine. A child nobody holds
// belongs to whoever holds the tracker.
func needMR(item *WorkItem, me string, meID int64) []forge.Ref {
	var out []forge.Ref
	for _, c := range mrLess(item) {
		if assignedTo(c, me, meID) || len(c.Assignees) == 0 && assignedTo(item.Issue, me, meID) {
			out = append(out, c.Ref)
		}
	}
	return out
}

// focusCandidate is the court of an item with fewer than two children: the
// least advanced open MR decides, preferring on a tie one that waits upstream
// so the dependency stays visible. Its reason carries no ref.
func focusCandidate(m *config.Manifest, open []forge.MR, me string) candidate {
	var best candidate
	for i, mr := range open {
		c := mrCandidate(m, mr, open, me)
		if i == 0 || c.stage < best.stage || c.stage == best.stage && c.up != nil && best.up == nil {
			best = c
		}
	}
	return best
}

// shortRef is the repo and number of a ref without its group: "web!7".
func shortRef(ref forge.Ref) string {
	s := ref.String()
	if i := strings.LastIndex(ref.Project, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// qualify prefixes a reason with the ref it is about and, when more of the
// item sits in the same court, says how many: "web!7 · pipeline failed · +1 more".
func qualify(ref forge.Ref, reason string, more int) string {
	reason = shortRef(ref) + " · " + reason
	if more > 0 {
		reason += fmt.Sprintf(" · +%d more", more)
	}
	return reason
}

// deriveCourt decides who must act next and why. Order: done, blocked, then
// the open MRs: with fewer than two children the focus MR decides (as it
// always has); with several children every open MR (and every child of mine
// still without one) votes and the best candidate wins. Then merged-but-open
// tracker and pre-MR work.
//
// With several candidates the reason names the MR or child it is about, so a
// failed pipeline on one of three MRs is not hidden behind the least advanced
// one. Court you also counts the other candidates in your court ("+N more").
// An upstream wait keeps its own wording ("waits on api!N") and is the only
// reason that sets upstream.
func deriveCourt(item *WorkItem, m *config.Manifest, me string, meID int64, blocked string) (court Court, reason string, upstream *forge.Ref) {
	if item.Stage == StageDone {
		if item.KeptOpen {
			return CourtNobody, "done · issue kept open", nil
		}
		return CourtNobody, "closed", nil
	}
	if blocked != "" {
		return CourtBlocked, blocked, nil
	}
	open := openMRs(item.MRs)
	if len(open) > 0 && len(item.Children) < 2 {
		c := focusCandidate(m, open, me)
		return c.court, c.reason, c.up
	}
	needs := needMR(item, me, meID)
	if len(open) > 0 {
		cands := make([]candidate, 0, len(open)+len(needs))
		for _, mr := range open {
			cands = append(cands, mrCandidate(m, mr, open, me))
		}
		for _, ref := range needs {
			cands = append(cands, candidate{court: CourtYou, reason: "no MR yet", ref: ref, prio: prioNoMR})
		}
		best := 0
		for i, c := range cands {
			if c.before(cands[best]) {
				best = i
			}
		}
		c := cands[best]
		if c.court == CourtUpstream {
			return c.court, c.reason, c.up
		}
		if len(cands) == 1 {
			return c.court, c.reason, nil
		}
		more := 0
		if c.court == CourtYou {
			for i, o := range cands {
				if i != best && o.court == CourtYou {
					more++
				}
			}
		}
		return c.court, qualify(c.ref, c.reason, more), nil
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
	case item.Stage == StageStarted && len(needs) > 0:
		return CourtYou, qualify(needs[0], "no MR yet", len(needs)-1), nil
	case item.Stage == StageStarted:
		// Several children, the ones without an MR held by others: theirs to open.
		if rest := mrLess(item); len(rest) > 0 {
			return CourtNobody, qualify(rest[0].Ref, "no MR yet · "+assigneeNames(rest[0].Assignees), len(rest)-1), nil
		}
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
