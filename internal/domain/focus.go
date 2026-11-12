package domain

import (
	"cmp"
	"slices"

	"github.com/coredev-uk/ade/internal/forge"
)

// FocusMR is the merge request the item's work is on now: its only open MR,
// or with several open the least advanced one (it decides the stage). nil
// when none is open. An issue often collects earlier MRs, merged or closed;
// they stay in MRs as history.
func (wi *WorkItem) FocusMR() *forge.MR {
	return focusOf(wi.MRs)
}

func focusOf(mrs []forge.MR) *forge.MR {
	var best *forge.MR
	for i := range mrs {
		mr := &mrs[i]
		if mr.State != mrOpen {
			continue
		}
		if best == nil || mrStage(*mr) < mrStage(*best) {
			best = mr
		}
	}
	return best
}

// sortMRs orders an item's MRs so every list leads with the work in hand: the
// focus MR, the other open ones, merged ones newest first, then closed ones.
func sortMRs(mrs []forge.MR) []forge.MR {
	if len(mrs) < 2 {
		return mrs
	}
	var focus forge.Ref
	if f := focusOf(mrs); f != nil {
		focus = f.Ref
	}
	rank := func(mr forge.MR) int {
		switch {
		case mr.State == mrOpen && mr.Ref == focus:
			return 0
		case mr.State == mrOpen:
			return 1
		case mr.State == mrMerged:
			return 2
		}
		return 3
	}
	out := slices.Clone(mrs)
	slices.SortStableFunc(out, func(a, b forge.MR) int {
		if c := cmp.Compare(rank(a), rank(b)); c != 0 {
			return c
		}
		if a.MergedAt != nil && b.MergedAt != nil {
			return b.MergedAt.Compare(*a.MergedAt)
		}
		return 0
	})
	return out
}
