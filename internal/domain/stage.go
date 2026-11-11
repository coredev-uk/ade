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

// deriveStage computes an item's stage. With several open MRs the least
// advanced one decides: the item is only as far along as its slowest part.
// Branch evidence comes from the caller (branches without an MR are invisible
// in forge data), and a child issue alone means work started.
func deriveStage(tracker forge.Issue, kids []forge.Issue, mrs []forge.MR, branches []string) Stage {
	if tracker.State == "closed" {
		return StageDone
	}
	if open := openMRs(mrs); len(open) > 0 {
		least := mrStage(open[0])
		for _, mr := range open[1:] {
			least = min(least, mrStage(mr))
		}
		return least
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
