package domain

import "github.com/coredev-uk/ade/internal/forge"

const (
	mrOpen   = "opened"
	mrMerged = "merged"
)

func openMRs(mrs []forge.MR) []forge.MR {
	var out []forge.MR
	for _, mr := range mrs {
		if mr.State == mrOpen {
			out = append(out, mr)
		}
	}
	return out
}

func anyMerged(mrs []forge.MR) bool {
	for _, mr := range mrs {
		if mr.State == mrMerged {
			return true
		}
	}
	return false
}

func pipelineStatus(mr forge.MR) string {
	if mr.Pipeline == nil {
		return ""
	}
	return mr.Pipeline.Status
}

// pipelineBusy reports a pipeline still in flight.
func pipelineBusy(status string) bool {
	return status == "running" || status == "pending" || status == "created"
}

// mrStage is where one open MR sits on the track.
func mrStage(mr forge.MR) Stage {
	if mr.Draft {
		return StageDraft
	}
	if s := pipelineStatus(mr); pipelineBusy(s) || s == "failed" {
		return StageCI
	}
	return StageReview
}

// leastStage is the least advanced stage among open MRs (there must be one).
func leastStage(open []forge.MR) Stage {
	least := mrStage(open[0])
	for _, mr := range open[1:] {
		least = min(least, mrStage(mr))
	}
	return least
}

// deriveStage computes an item's stage: the item is only as far along as its
// slowest part. Branch evidence comes from the caller (branches without an MR
// are invisible in forge data), and a child issue alone means work started.
//
// An item with several child issues is judged child by child (see
// childrenStage). With none, or one, the whole item is one piece of work: the
// least advanced open MR decides, else a merged MR, a branch, a child.
func deriveStage(tracker forge.Issue, kids []forge.Issue, mrs []forge.MR, branches []string) Stage {
	if tracker.State == "closed" {
		return StageDone
	}
	if len(kids) > 1 {
		if s, ok := childrenStage(kids, mrs, branches); ok {
			return s
		}
	}
	if open := openMRs(mrs); len(open) > 0 {
		return leastStage(open)
	}
	switch {
	case anyMerged(mrs):
		return StageMerged
	case len(branches) > 0:
		return StageBranch
	case len(kids) > 0:
		return StageStarted
	}
	return StageAssigned
}

// childrenStage is the least advanced stage over the parts of an item with
// several child issues. A child is where its open MRs leave it (the least
// advanced one), else Merged when it has a merged MR, else Branch or Started.
// MRs no child claims count as one more part, but only for what they have
// (open or merged). A closed child without an open or merged MR is ignored.
//
// Branches carry no child, so they cannot be attributed: a child without an
// MR is Branch only while no child has an MR yet (the branch is then the work
// begun), and Started once another child has its MR (the branch is then most
// likely that child's). ok is false when no part counts (every child closed
// without an MR).
func childrenStage(kids []forge.Issue, mrs []forge.MR, branches []string) (s Stage, ok bool) {
	anyMR := len(openMRs(mrs)) > 0 || anyMerged(mrs)
	for _, g := range groupMRs(kids, mrs) {
		var p Stage
		switch open := openMRs(g.MRs); {
		case len(open) > 0:
			p = leastStage(open)
		case anyMerged(g.MRs):
			p = StageMerged
		case g.Child == nil || g.Child.State == "closed":
			continue
		case len(branches) > 0 && !anyMR:
			p = StageBranch
		default:
			p = StageStarted
		}
		if !ok || p < s {
			s, ok = p, true
		}
	}
	return s, ok
}
