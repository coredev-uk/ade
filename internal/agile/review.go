package agile

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

// Basis says what the sprint is measured in.
const (
	BasisWeight = "weight" // some item carries a weight
	BasisCount  = "count"  // nobody weights: items are counted
)

// Totals measures a set of items. Weight sums only the weighted ones;
// Unweighted counts the rest, which are inside Count but never add (not even
// as zero) to Weight.
type Totals struct {
	Count      int `json:"count"`
	Weight     int `json:"weight"`
	Weighted   int `json:"weighted"`   // items that carry a weight
	Unweighted int `json:"unweighted"` // items that do not
}

func (t *Totals) add(w *int) {
	t.Count++
	if w == nil {
		t.Unweighted++
		return
	}
	t.Weighted++
	t.Weight += *w
}

// ReviewItem is one issue of the sprint.
type ReviewItem struct {
	Ref      forge.Ref  `json:"ref"`
	Title    string     `json:"title"`
	Weight   *int       `json:"weight,omitempty"`
	Done     bool       `json:"done"`
	KeptOpen bool       `json:"kept_open,omitempty"` // done by its workflow label, tracker issue still open
	DoneAt   *time.Time `json:"done_at,omitempty"`
	Added    bool       `json:"added,omitempty"` // entered the sprint after it started
	AddedAt  time.Time  `json:"added_at"`
	Epic     string     `json:"epic,omitempty"`
	Assigned bool       `json:"assigned"` // to you
	MRs      []string   `json:"merged_mrs,omitempty"`
}

// EpicGroup is the sprint's scope under one epic (Premium).
type EpicGroup struct {
	Epic    string   `json:"epic"` // empty: no epic
	Total   Totals   `json:"total"`
	Done    Totals   `json:"done"`
	Carried Totals   `json:"carried"`
	Refs    []string `json:"refs"`
}

// BurnPoint is the remaining work at the end of one working day.
type BurnPoint struct {
	Date      string  `json:"date"`
	Remaining int     `json:"remaining"` // in the review's basis
	Ideal     float64 `json:"ideal"`     // straight line from the sprint's opening scope to zero
}

// SprintResult is one past sprint's outcome, for velocity.
type SprintResult struct {
	Title string `json:"title"`
	Start string `json:"start"`
	Due   string `json:"due"`
	Scope Totals `json:"scope"`
	Done  Totals `json:"done"`
	// Velocity is the weight done, or nil when the sprint had no weighted
	// item (so it says nothing about weight and is left out of the average).
	Velocity *int `json:"velocity,omitempty"`
}

// Velocity is the throughput over the last closed sprints.
type Velocity struct {
	Basis   string         `json:"basis"`
	Sprints []SprintResult `json:"sprints"` // oldest first
	// Average is per sprint, in Basis: weight over sprints that had weighted
	// work, else items done over all of them. Nil with no closed sprint.
	Average *float64 `json:"average,omitempty"`
}

// Review is a sprint review.
type Review struct {
	Project string        `json:"project"`
	Sprint  domain.Sprint `json:"sprint"`
	Basis   string        `json:"basis"`
	Mine    bool          `json:"mine,omitempty"` // only items assigned to you

	// Committed is what the sprint held at its start; Added came in later.
	// Done and Carried split Committed+Added.
	Committed Totals `json:"committed"`
	Added     Totals `json:"added"`
	Done      Totals `json:"done"`
	Carried   Totals `json:"carried"`

	Items     []ReviewItem `json:"items"`
	Epics     []EpicGroup  `json:"epics,omitempty"`
	Burndown  []BurnPoint  `json:"burndown"`
	Velocity  Velocity     `json:"velocity"`
	Narrative string       `json:"narrative,omitempty"`
	// Warnings name timelines that could not be read; those items fall back to their creation date.
	Warnings []string `json:"warnings,omitempty"`
}

// issueHistory is what the forge timelines said about one issue.
type issueHistory struct {
	Labels []forge.LabelEvent
	Sprint []forge.SprintEvent
}

// ReviewInput is everything BuildReview interprets.
type ReviewInput struct {
	Project  string
	Manifest *config.Manifest
	Sprint   domain.Sprint
	Issues   []forge.Issue // the sprint's tracker issues, closed ones included
	History  map[forge.Ref]issueHistory
	Past     []PastSprint
	Me       forge.User
	Mine     bool
	Cal      Calendar
	Now      time.Time
	// Merged lists, per tracker issue, the MRs merged for it (refs, short form).
	Merged map[forge.Ref][]string
}

// PastSprint is a closed sprint with its issues, for velocity.
type PastSprint struct {
	Sprint domain.Sprint
	Issues []forge.Issue
}

// IsDone says whether the issue counts as done in the review: closed, or kept
// open with the manifest's done workflow label (teams close sprint tickets at
// the review).
func IsDone(is forge.Issue, m *config.Manifest) (done, keptOpen bool) {
	if is.State == "closed" {
		return true, false
	}
	if m == nil || m.Labels.Workflow.Done == "" {
		return false, false
	}
	f := domain.FacetsOf(is.Labels, m.Labels)
	if strings.EqualFold(f.Workflow, m.Labels.Workflow.Done) {
		return true, true
	}
	return false, false
}

func weightFor(is forge.Issue, m *config.Manifest) *int {
	if m == nil {
		return domain.ResolveWeight(is, config.WeightSource{})
	}
	return domain.ResolveWeight(is, m.Tracker.Weight)
}

// BuildReview computes the review of in.Sprint.
func BuildReview(in ReviewInput) Review {
	r := Review{Project: in.Project, Sprint: in.Sprint, Mine: in.Mine, Items: []ReviewItem{}}
	start := in.Cal.sprintDay(in.Sprint.Start.Time)
	opening := start.AddDate(0, 0, 1) // issues in the sprint by the end of day one are committed

	for _, is := range in.Issues {
		if in.Mine && !assignedTo(is, in.Me) {
			continue
		}
		h := in.History[is.Ref]
		it := ReviewItem{
			Ref: is.Ref, Title: is.Title, Weight: weightFor(is, in.Manifest), Epic: is.Epic,
			Assigned: assignedTo(is, in.Me), MRs: in.Merged[is.Ref],
		}
		it.Done, it.KeptOpen = IsDone(is, in.Manifest)
		it.AddedAt = addedAt(is, in.Sprint, h)
		it.Added = !it.AddedAt.Before(opening)
		if it.Done {
			it.DoneAt = doneAt(is, in.Manifest, h)
		}
		r.Items = append(r.Items, it)
	}
	slices.SortStableFunc(r.Items, func(a, b ReviewItem) int {
		return cmp.Or(cmp.Compare(a.Ref.Project, b.Ref.Project), cmp.Compare(a.Ref.IID, b.Ref.IID))
	})

	for _, it := range r.Items {
		if it.Added {
			r.Added.add(it.Weight)
		} else {
			r.Committed.add(it.Weight)
		}
		if it.Done {
			r.Done.add(it.Weight)
		} else {
			r.Carried.add(it.Weight)
		}
	}
	r.Basis = basisOf(r.Committed.Weighted + r.Added.Weighted)
	r.Epics = epicGroups(r.Items)
	r.Burndown = burndown(r.Items, in, r.Basis)
	r.Velocity = velocity(in.Past, in.Manifest)
	return r
}

func basisOf(weighted int) string {
	if weighted > 0 {
		return BasisWeight
	}
	return BasisCount
}

func assignedTo(is forge.Issue, me forge.User) bool {
	return slices.ContainsFunc(is.Assignees, func(u forge.User) bool { return sameUser(u, me) })
}

// addedAt is when the issue entered the sprint: the newest "add" of the
// sprint that no later "remove" undid; else, for lack of history, when the
// issue was created.
func addedAt(is forge.Issue, sp domain.Sprint, h issueHistory) time.Time {
	var at time.Time
	for _, e := range h.Sprint {
		if e.Source != sp.Source || strconvKey(e.ID) != sp.Key {
			continue
		}
		switch e.Action {
		case "add":
			at = e.At
		case "remove":
			at = time.Time{}
		}
	}
	if sp.Source == "labels" {
		for _, e := range h.Labels {
			switch {
			case !strings.EqualFold(e.Label, sp.Key):
			case e.Action == "add":
				at = e.At
			case e.Action == "remove":
				at = time.Time{}
			}
		}
	}
	if at.IsZero() {
		return is.CreatedAt
	}
	return at
}

// doneAt is when the issue finished: the earlier of its close time and the
// last time the done label was added. Without either, its last update.
func doneAt(is forge.Issue, m *config.Manifest, h issueHistory) *time.Time {
	var best *time.Time
	take := func(t time.Time) {
		if !t.IsZero() && (best == nil || t.Before(*best)) {
			best = &t
		}
	}
	if is.ClosedAt != nil {
		take(*is.ClosedAt)
	}
	if m != nil && m.Labels.Workflow.Done != "" {
		label := domain.WorkflowLabel(m.Labels.Workflow.Done, m.Labels)
		var last time.Time
		for _, e := range h.Labels {
			if strings.EqualFold(e.Label, label) {
				switch e.Action {
				case "add":
					last = e.At
				case "remove":
					last = time.Time{}
				}
			}
		}
		take(last)
	}
	if best == nil {
		t := is.UpdatedAt
		best = &t
	}
	return best
}

func epicGroups(items []ReviewItem) []EpicGroup {
	hasEpic := slices.ContainsFunc(items, func(it ReviewItem) bool { return it.Epic != "" })
	if !hasEpic {
		return nil
	}
	idx := map[string]int{}
	var out []EpicGroup
	for _, it := range items {
		i, ok := idx[it.Epic]
		if !ok {
			i = len(out)
			idx[it.Epic] = i
			out = append(out, EpicGroup{Epic: it.Epic})
		}
		g := &out[i]
		g.Total.add(it.Weight)
		if it.Done {
			g.Done.add(it.Weight)
		} else {
			g.Carried.add(it.Weight)
		}
		g.Refs = append(g.Refs, it.Ref.String())
	}
	// Named epics first (alphabetical), the epic-less rest last.
	slices.SortStableFunc(out, func(a, b EpicGroup) int {
		if (a.Epic == "") != (b.Epic == "") {
			if a.Epic == "" {
				return 1
			}
			return -1
		}
		return cmp.Compare(a.Epic, b.Epic)
	})
	return out
}

// burndown is the remaining work at the end of each working day from the
// sprint's start to today (or its end). By weight the unweighted items are
// left out; by count every item counts.
func burndown(items []ReviewItem, in ReviewInput, basis string) []BurnPoint {
	start, due := in.Cal.sprintDay(in.Sprint.Start.Time), in.Cal.sprintDay(in.Sprint.Due.Time)
	last := due
	if today := in.Cal.Day(in.Now); today.Before(last) {
		last = today
	}
	days := in.Cal.WorkdaysBetween(start, last)
	allDays := len(in.Cal.WorkdaysBetween(start, due))
	value := func(it ReviewItem) (int, bool) {
		if basis == BasisCount {
			return 1, true
		}
		if it.Weight == nil {
			return 0, false
		}
		return *it.Weight, true
	}
	opening := start.AddDate(0, 0, 1)
	var committed int
	for _, it := range items {
		if v, ok := value(it); ok && it.AddedAt.Before(opening) {
			committed += v
		}
	}
	out := make([]BurnPoint, 0, len(days))
	for i, d := range days {
		eod := d.AddDate(0, 0, 1)
		rem := 0
		for _, it := range items {
			v, ok := value(it)
			if !ok || !it.AddedAt.Before(eod) {
				continue
			}
			if it.Done && it.DoneAt != nil && it.DoneAt.Before(eod) {
				continue
			}
			rem += v
		}
		ideal := 0.0
		if allDays > 0 {
			ideal = float64(committed) * float64(allDays-(i+1)) / float64(allDays)
		}
		out = append(out, BurnPoint{Date: d.Format("2006-01-02"), Remaining: rem, Ideal: ideal})
	}
	return out
}

// velocity summarises past sprints. A sprint without any weighted item has no
// weight velocity (nil), so unweighted work never drags the average to zero.
func velocity(past []PastSprint, m *config.Manifest) Velocity {
	v := Velocity{Sprints: []SprintResult{}}
	weighted := 0
	for _, p := range past {
		res := SprintResult{Title: p.Sprint.Title, Start: p.Sprint.Start.String(), Due: p.Sprint.Due.String()}
		for _, is := range p.Issues {
			w := weightFor(is, m)
			res.Scope.add(w)
			if done, _ := IsDone(is, m); done {
				res.Done.add(w)
			}
		}
		if res.Scope.Weighted > 0 {
			w := res.Done.Weight
			res.Velocity = &w
			weighted++
		}
		v.Sprints = append(v.Sprints, res)
	}
	slices.SortStableFunc(v.Sprints, func(a, b SprintResult) int { return cmp.Compare(a.Start, b.Start) })
	v.Basis = basisOf(weighted)
	var sum, n float64
	for _, s := range v.Sprints {
		switch {
		case v.Basis == BasisWeight && s.Velocity != nil:
			sum += float64(*s.Velocity)
			n++
		case v.Basis == BasisCount && s.Scope.Count > 0:
			sum += float64(s.Done.Count)
			n++
		}
	}
	if n > 0 {
		avg := sum / n
		v.Average = &avg
	}
	return v
}
