package tui

import (
	"sort"
	"strings"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
)

// homeRow is one selectable line on Home: a work item or an MR awaiting
// your review. Section rules are not rows; they are drawn between groups.
type homeRow struct {
	project string
	section homeSection
	item    *domain.WorkItem // nil for review rows
	mr      *forge.MR        // set for review rows
}

func (r homeRow) ref() string {
	if r.item != nil {
		return r.item.Ref.String()
	}
	if r.mr != nil {
		return r.mr.Ref.String()
	}
	return ""
}

type homeSection int

const (
	secMine homeSection = iota
	secReviews
	secSprintOpen
	secNext
)

func (s homeSection) title() string {
	return [...]string{"My work", "Reviews requested", "Sprint · open", "Next sprint"}[s]
}

// homeFilter narrows Home to one kind of attention.
type homeFilter int

const (
	hfAll homeFilter = iota
	hfMine
	hfTriage
	hfBlocked
	hfDrift
)

func (f homeFilter) String() string {
	return [...]string{"all", "waiting on you", "needs triage", "blocked", "label drift"}[f]
}

func (f homeFilter) keep(r homeRow) bool {
	it := r.item
	switch f {
	case hfMine:
		return (it != nil && it.Court == domain.CourtYou) || r.mr != nil
	case hfTriage:
		return it != nil && len(it.Triage) > 0
	case hfBlocked:
		return it != nil && (it.Blocked || it.Court == domain.CourtBlocked || it.Court == domain.CourtUpstream)
	case hfDrift:
		return it != nil && it.Drift
	default:
		return true
	}
}

// courtRank orders My work: what you can act on first, then what waits on
// others, then what nobody holds.
var courtRank = map[domain.Court]int{
	domain.CourtYou: 0, domain.CourtBlocked: 1, domain.CourtUpstream: 2,
	domain.CourtReviewers: 3, domain.CourtCI: 4, domain.CourtAuthor: 5, domain.CourtNobody: 6,
}

// buildHomeRows flattens sync results into Home's selectable rows, grouped
// per project and section, filtered by f.
func buildHomeRows(results []forgesync.Result, f homeFilter) []homeRow {
	var rows []homeRow
	for ri := range results {
		res := &results[ri]
		var mine, open, next []*domain.WorkItem
		for i := range res.Items {
			it := &res.Items[i]
			switch {
			case it.Stage == domain.StageDone:
				// done items count in sprint load, not as rows
			case it.Assigned:
				mine = append(mine, it)
			case inSprint(it, res.Window.Current):
				open = append(open, it)
			case inSprint(it, res.Window.Next):
				next = append(next, it)
			}
		}
		sortMine(mine)
		sortByRef(open)
		sortByRef(next)
		add := func(sec homeSection, items []*domain.WorkItem) {
			for _, it := range items {
				r := homeRow{project: res.Project, section: sec, item: it}
				if f.keep(r) {
					rows = append(rows, r)
				}
			}
		}
		add(secMine, mine)
		for i := range res.Reviews {
			r := homeRow{project: res.Project, section: secReviews, mr: &res.Reviews[i]}
			if f.keep(r) {
				rows = append(rows, r)
			}
		}
		add(secSprintOpen, open)
		add(secNext, next)
	}
	return rows
}

func inSprint(it *domain.WorkItem, s *domain.Sprint) bool {
	return s != nil && it.Sprint != nil && it.Sprint.Key == s.Key
}

func sortMine(items []*domain.WorkItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if ra, rb := courtRank[a.Court], courtRank[b.Court]; ra != rb {
			return ra < rb
		}
		if a.Stage != b.Stage {
			return a.Stage > b.Stage // closest to done first: finish before starting
		}
		return a.Ref.IID < b.Ref.IID
	})
}

func sortByRef(items []*domain.WorkItem) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].Ref.IID < items[j].Ref.IID })
}

// sprintLoad sums weights for the current sprint: committed (every item in
// it), done (closed), and unweighted counts, so load is honest about gaps.
type sprintLoad struct {
	committed, done, unweighted, items int
}

func loadOf(res *forgesync.Result) sprintLoad {
	var l sprintLoad
	for i := range res.Items {
		it := &res.Items[i]
		if !inSprint(it, res.Window.Current) {
			continue
		}
		l.items++
		if it.Weight == nil {
			l.unweighted++
			continue
		}
		l.committed += *it.Weight
		if it.Stage == domain.StageDone {
			l.done += *it.Weight
		}
	}
	return l
}

// shortRef drops the group from a ref within its own project's section:
// "ade-demo-env/tracker#1" -> "#1" when it is the tracker, else "api#1".
func shortRef(ref forge.Ref, tracker string) string {
	s := ref.String()
	if ref.Project == tracker {
		return s[len(ref.Project):]
	}
	if i := strings.LastIndex(ref.Project, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}
