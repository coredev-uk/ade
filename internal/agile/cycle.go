package agile

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// Cycle time (PLAN 6.4): how long an item took from being assigned to its
// work being merged. An item without a merged MR has no cycle time (it is
// left out of the median, never counted as zero).

// Where an item's assignment time came from.
const (
	AssignedFromNote    = "note"    // the forge's "assigned to @you" system note
	AssignedFromCreated = "created" // no such note readable: the issue's creation time
)

// CycleSummary is the sprint's cycle time over the items that have one.
type CycleSummary struct {
	Items       int      `json:"items"`
	MedianHours *float64 `json:"median_hours,omitempty"`
}

// cycleOf measures one item from its assignment to its merge. ok is false
// when either time is missing or the merge predates the assignment.
func cycleOf(assigned, merged *time.Time) (hours float64, ok bool) {
	if assigned == nil || merged == nil || merged.Before(*assigned) {
		return 0, false
	}
	return merged.Sub(*assigned).Hours(), true
}

// median of hours: the middle value, or the mean of the middle two.
func median(hours []float64) *float64 {
	if len(hours) == 0 {
		return nil
	}
	s := slices.Clone(hours)
	slices.Sort(s)
	m := s[len(s)/2]
	if len(s)%2 == 0 {
		m = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	return &m
}

// FormatCycle writes hours as "45m", "30h" or "2d 6h".
func FormatCycle(hours float64) string {
	switch {
	case hours < 1:
		return fmt.Sprintf("%dm", int(hours*60+0.5))
	case hours < 48:
		return fmt.Sprintf("%dh", int(hours+0.5))
	}
	h := int(hours + 0.5)
	if h%24 == 0 {
		return fmt.Sprintf("%dd", h/24)
	}
	return fmt.Sprintf("%dd %dh", h/24, h%24)
}

// assignedNote matches GitLab's system notes: "assigned to @a", "assigned to
// @a and @b", "assigned to @a, @b and @c".
var assignedNote = regexp.MustCompile(`^assigned to (@[\w.-]+(?:(?:,| and) @[\w.-]+)*)\s*$`)

var mention = regexp.MustCompile(`@([\w.-]+)`)

// assignedAt finds when the issue was first assigned (to you, when mine): the
// first system note saying so, oldest first. nil when no note does.
func assignedAt(ds []forge.Discussion, me forge.User, mine bool) *time.Time {
	var best *time.Time
	for _, d := range ds {
		for _, n := range d.Notes {
			if !n.System {
				continue
			}
			m := assignedNote.FindStringSubmatch(strings.TrimSpace(n.Body))
			if m == nil {
				continue
			}
			if mine && !slices.ContainsFunc(mention.FindAllStringSubmatch(m[1], -1), func(u []string) bool {
				return strings.EqualFold(u[1], me.Username)
			}) {
				continue
			}
			if best == nil || n.CreatedAt.Before(*best) {
				t := n.CreatedAt
				best = &t
			}
		}
	}
	return best
}

// assignedTimes reads the discussions of the items that were merged and
// returns when each was assigned. An issue whose notes cannot be read is
// named in the warnings and falls back to its creation date in the review.
func assignedTimes(ctx context.Context, f forge.Forge, refs []forge.Ref, me forge.User, mine bool) (map[forge.Ref]time.Time, []string) {
	out := map[forge.Ref]time.Time{}
	var (
		mu    sync.Mutex
		warns []string
		wg    sync.WaitGroup
	)
	sem := make(chan struct{}, 4)
	for _, ref := range refs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			ds, err := f.Discussions(ctx, ref)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				warns = append(warns, fmt.Sprintf("%s: assignment time: %v", ref, brief(err)))
				return
			}
			if t := assignedAt(ds, me, mine); t != nil {
				out[ref] = *t
			}
		}()
	}
	wg.Wait()
	slices.Sort(warns)
	return out, warns
}
