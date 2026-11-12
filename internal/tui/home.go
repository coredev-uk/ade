package tui

import (
	"sort"
	"strings"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
)

// homeRow is one selectable line on Home: a work item or an MR awaiting
// your review (or found by the search). Section rules are not rows; they are
// drawn between groups.
type homeRow struct {
	project string
	section homeSection
	item    *domain.WorkItem // nil for review rows
	mr      *forge.MR        // set for review and search rows
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
	secPrevious // unfinished items of the sprint that just ended
	secSearch   // open MRs the forge search found beyond the synced ones
)

func (s homeSection) title(g glyphs) string {
	return g.T([...]string{"My work", "Reviews requested", "Sprint · open", "Next sprint", "Previous sprint", "Open MRs · search"}[s])
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

// homeQuery is everything that narrows or extends Home's rows: the attention
// filter, one facet value, one project, the search text and the open MRs the
// forge search returned.
type homeQuery struct {
	filter  homeFilter
	facet   string // "type" | "app" | "workflow"; "" none
	value   string
	project string
	text    string
	remote  []remoteMR
}

// remoteMR is an open MR found by the project-wide search.
type remoteMR struct {
	project string
	mr      forge.MR
}

// facetValues lists the values a facet takes in it, "type" | "app" | "workflow".
func facetValues(it *domain.WorkItem, facet string) []string {
	switch facet {
	case "type":
		if it.Type != "" {
			return []string{it.Type}
		}
	case "app":
		return it.Apps
	case "workflow":
		if it.Workflow != "" {
			return []string{it.Workflow}
		}
	}
	return nil
}

func (q homeQuery) keep(r homeRow) bool {
	if !q.filter.keep(r) {
		return false
	}
	if q.facet != "" && (r.item == nil || !contains(facetValues(r.item, q.facet), q.value)) {
		return false
	}
	return q.text == "" || fuzzyFields(q.text, rowFields(r))
}

// buildHomeRows flattens sync results into Home's selectable rows, grouped
// per project and section, filtered by f.
func buildHomeRows(results []forgesync.Result, f homeFilter) []homeRow {
	return buildRows(results, homeQuery{filter: f})
}

func buildRows(results []forgesync.Result, q homeQuery) []homeRow {
	var rows []homeRow
	for ri := range results {
		res := &results[ri]
		if q.project != "" && res.Project != q.project {
			continue
		}
		var mine, open, next, prev []*domain.WorkItem
		for i := range res.Items {
			it := &res.Items[i]
			switch {
			case it.Stage == domain.StageDone:
				// done items count in sprint load, not as rows
			case it.Assigned:
				mine = append(mine, it)
			case inSprint(it, res.Window.Current):
				// The tracker issue decides: a ticket assigned to anyone is taken
				// off the list, but stays findable by search.
				if len(it.Issue.Assignees) == 0 || q.text != "" {
					open = append(open, it)
				}
			case inSprint(it, res.Window.Next):
				next = append(next, it)
			case inSprint(it, res.Window.Previous):
				prev = append(prev, it)
			}
		}
		sortMine(mine)
		sortByRef(open)
		sortByRef(next)
		sortByRef(prev)
		add := func(sec homeSection, items []*domain.WorkItem) {
			for _, it := range items {
				r := homeRow{project: res.Project, section: sec, item: it}
				if q.keep(r) {
					rows = append(rows, r)
				}
			}
		}
		add(secMine, mine)
		have := map[string]bool{}
		for i := range res.Reviews {
			have[res.Reviews[i].Ref.String()] = true
			r := homeRow{project: res.Project, section: secReviews, mr: &res.Reviews[i]}
			if q.keep(r) {
				rows = append(rows, r)
			}
		}
		add(secSprintOpen, open)
		add(secNext, next)
		add(secPrevious, prev)
		if q.text != "" {
			for i := range q.remote {
				rm := &q.remote[i]
				if rm.project != res.Project || have[rm.mr.Ref.String()] {
					continue
				}
				r := homeRow{project: res.Project, section: secSearch, mr: &rm.mr}
				if q.filter == hfAll && q.facet == "" && q.keep(r) {
					rows = append(rows, r)
				}
			}
		}
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
