package agile

import (
	"fmt"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// Format is a text rendering of a report.
type Format int

const (
	Text     Format = iota // plain lines for a terminal
	Markdown               // headings and lists, for pasting
)

type writer struct {
	strings.Builder
	md bool
}

func (w *writer) line(format string, args ...any) {
	fmt.Fprintf(&w.Builder, format, args...)
	w.WriteByte('\n')
}

func (w *writer) heading(level int, s string) {
	if w.md {
		w.line("%s %s", strings.Repeat("#", level), s)
	} else {
		w.line("%s", s)
	}
}

func (w *writer) bullet(format string, args ...any) { w.line("- "+format, args...) }

func dayLabel(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.Format("Mon 2 Jan")
}

// RenderStandup renders the standup as a deterministic bullet list.
func RenderStandup(s Standup, f Format) string {
	w := &writer{md: f == Markdown}
	w.heading(1, "Standup · "+dayLabel(s.Date))
	if s.Narrative != "" {
		w.line("")
		w.line("%s", s.Narrative)
	}
	w.line("")
	w.heading(2, "Yesterday · "+dayLabel(s.Yesterday))
	if len(s.Done) == 0 {
		w.bullet("nothing recorded")
	}
	for _, e := range s.Done {
		w.bullet("%s", e.Text)
	}
	w.line("")
	w.heading(2, "Today")
	if len(s.Today) == 0 {
		w.bullet("nothing in your court")
	}
	for _, t := range s.Today {
		line := fmt.Sprintf("%s %s", refLabel(t.Ref), t.Title)
		if t.Stage != "" {
			line += " · " + t.Stage
		}
		if t.Reason != "" {
			line += " · " + t.Reason
		}
		if t.Weight != nil {
			line += fmt.Sprintf(" · %dw", *t.Weight)
		}
		w.bullet("%s", line)
	}
	w.line("")
	w.heading(2, "Blockers")
	if len(s.Blockers) == 0 {
		w.bullet("none")
	}
	for _, b := range s.Blockers {
		w.bullet("%s %s · %s", refLabel(b.Ref), b.Title, b.Reason)
	}
	for _, warn := range s.Warnings {
		w.line("")
		w.line("warning: %s", warn)
	}
	return w.String()
}

// FormatTotals renders totals in the basis: "12w in 4 items (1 unweighted)",
// or "4 items".
func FormatTotals(t Totals, basis string) string {
	items := plural(t.Count, "item")
	if basis != BasisWeight {
		return items
	}
	s := fmt.Sprintf("%dw · %s", t.Weight, items)
	if t.Unweighted > 0 {
		s += fmt.Sprintf(" (%d unweighted)", t.Unweighted)
	}
	return s
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// RenderReview renders the sprint review.
func RenderReview(r Review, f Format) string {
	w := &writer{md: f == Markdown}
	w.heading(1, fmt.Sprintf("Sprint review · %s (%s – %s)", r.Sprint.Title, r.Sprint.Start, r.Sprint.Due))
	w.line("")
	if r.Narrative != "" {
		w.line("%s", r.Narrative)
		w.line("")
	}
	scope := "by weight"
	if r.Basis == BasisCount {
		scope = "by item count (nothing is weighted)"
	}
	w.line("Measured %s%s.", scope, map[bool]string{true: ", your items only", false: ""}[r.Mine])
	w.line("")
	w.bullet("Committed: %s", FormatTotals(r.Committed, r.Basis))
	w.bullet("Added mid-sprint: %s", FormatTotals(r.Added, r.Basis))
	w.bullet("Done: %s", FormatTotals(r.Done, r.Basis))
	w.bullet("Carried over: %s", FormatTotals(r.Carried, r.Basis))
	w.bullet("Cycle time (assigned → merged): %s", cycleSummaryText(r.Cycle))

	w.line("")
	w.heading(2, "Velocity")
	renderVelocity(w, r.Velocity)

	if len(r.Burndown) > 0 {
		w.line("")
		w.heading(2, "Burn-down (remaining "+r.Basis+")")
		renderBurndown(w, r.Burndown)
	}

	if len(r.Epics) > 0 {
		w.line("")
		w.heading(2, "By epic")
		for _, g := range r.Epics {
			name := g.Epic
			if name == "" {
				name = "no epic"
			}
			w.bullet("%s: %s", name, EpicSummary(g, r.Basis))
		}
	}

	w.line("")
	w.heading(2, "Items")
	for _, it := range r.Items {
		w.bullet("%s", itemLine(it))
		if it.Narrative != "" && w.md { // per-parent AI narrative: a Markdown export feature
			for _, l := range strings.Split(it.Narrative, "\n") {
				w.line("  %s", strings.TrimSpace(l))
			}
		}
	}
	for _, warn := range r.Warnings {
		w.line("")
		w.line("warning: %s", warn)
	}
	return w.String()
}

func itemLine(it ReviewItem) string {
	state := "carried"
	switch {
	case it.Done && it.KeptOpen:
		state = "done (kept open)"
	case it.Done:
		state = "done"
	}
	weight := "–"
	if it.Weight != nil {
		weight = fmt.Sprintf("%dw", *it.Weight)
	}
	line := fmt.Sprintf("%s · %s · %s · %s", short(it.Ref), state, weight, it.Title)
	if it.Added {
		line += " · added mid-sprint"
	}
	if len(it.MRs) > 0 {
		line += " · merged " + strings.Join(it.MRs, ", ")
	}
	if it.CycleHours != nil {
		line += " · cycle " + FormatCycle(*it.CycleHours)
		if it.AssignedFrom == AssignedFromCreated {
			line += " (from creation)"
		}
	}
	return line
}

func cycleSummaryText(c CycleSummary) string {
	if c.MedianHours == nil {
		return "no merged item to measure"
	}
	return fmt.Sprintf("median %s over %s", FormatCycle(*c.MedianHours), plural(c.Items, "item"))
}

func renderVelocity(w *writer, v Velocity) {
	if len(v.Sprints) == 0 {
		w.bullet("no closed sprints to compare")
		return
	}
	for _, s := range v.Sprints {
		switch {
		case v.Basis == BasisWeight && s.Velocity == nil:
			w.bullet("%s: no weighted work (%s done) · not in the average", s.Title, plural(s.Done.Count, "item"))
		case v.Basis == BasisWeight:
			line := fmt.Sprintf("%s: %dw done of %dw", s.Title, *s.Velocity, s.Scope.Weight)
			if s.Scope.Unweighted > 0 {
				line += fmt.Sprintf(" (%d unweighted not counted)", s.Scope.Unweighted)
			}
			w.bullet("%s", line)
		default:
			w.bullet("%s: %d of %s done", s.Title, s.Done.Count, plural(s.Scope.Count, "item"))
		}
	}
	if v.Average != nil {
		unit := " items"
		if v.Basis == BasisWeight {
			unit = "w"
		}
		w.bullet("average: %.1f%s per sprint over %s", *v.Average, unit, plural(len(v.Sprints), "sprint"))
	}
}

func renderBurndown(w *writer, pts []BurnPoint) {
	peak := 1
	for _, p := range pts {
		peak = max(peak, p.Remaining, int(p.Ideal+0.5))
	}
	for _, p := range pts {
		bar := strings.Repeat("#", (p.Remaining*20+peak-1)/peak)
		if w.md {
			w.bullet("%s: %d (ideal %.1f)", dayLabel(p.Date), p.Remaining, p.Ideal)
			continue
		}
		w.line("  %-10s %-20s %3d  ideal %.1f", dayLabel(p.Date), bar, p.Remaining, p.Ideal)
	}
}

// RenderVelocity renders the velocity alone.
func RenderVelocity(v Velocity, f Format) string {
	w := &writer{md: f == Markdown}
	w.heading(1, "Velocity · "+v.Basis)
	renderVelocity(w, v)
	return w.String()
}

// refLabel shortens "group/tracker#1" to "tracker#1" for reading; JSON keeps
// the full reference.
func refLabel(ref string) string {
	r, err := forge.ParseRef(ref)
	if err != nil {
		return ref
	}
	return short(r)
}

// EpicSummary reads "1 of 2 items done" and, by weight, ", 5w of 8w".
func EpicSummary(g EpicGroup, basis string) string {
	s := fmt.Sprintf("%d of %s done", g.Done.Count, plural(g.Total.Count, "item"))
	if basis == BasisWeight {
		s += fmt.Sprintf(", %dw of %dw", g.Done.Weight, g.Total.Weight)
	}
	return s
}
