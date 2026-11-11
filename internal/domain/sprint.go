package domain

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
)

// Sprint sources, matching the values of tracker.iterations.source.
const (
	sourceIterations = "gitlab-iterations"
	sourceMilestones = "milestones"
	sourceLabels     = "labels"
)

func sortSprints(s []Sprint) {
	slices.SortStableFunc(s, func(a, b Sprint) int { return a.Start.Compare(b.Start.Time) })
}

// SprintsFromMilestones turns dated milestones into sprints, oldest first.
// Milestones without both dates are not sprints.
func SprintsFromMilestones(ms []forge.Milestone) []Sprint {
	var out []Sprint
	for _, m := range ms {
		if m.Start.IsZero() || m.Due.IsZero() {
			continue
		}
		out = append(out, milestoneSprint(m))
	}
	sortSprints(out)
	return out
}

func milestoneSprint(m forge.Milestone) Sprint {
	return Sprint{Key: strconv.FormatInt(m.ID, 10), Title: m.Title, Start: m.Start, Due: m.Due, Source: sourceMilestones}
}

// SprintsFromIterations turns iterations into sprints, oldest first.
// Cadence-generated iterations carry no title, so one is made from the
// cadence name (the argument, else the iteration's own, else "Iteration") and
// the date range: "Platform · 3–14 Nov".
func SprintsFromIterations(its []forge.Iteration, cadence string) []Sprint {
	var out []Sprint
	for _, it := range its {
		if it.Start.IsZero() || it.Due.IsZero() {
			continue
		}
		out = append(out, iterationSprint(it, cadence))
	}
	sortSprints(out)
	return out
}

func iterationSprint(it forge.Iteration, cadence string) Sprint {
	title := it.Title
	if title == "" {
		name := cmpFirst(cadence, it.Cadence, "Iteration")
		title = name + " · " + dateRange(it.Start.Time, it.Due.Time)
	}
	return Sprint{Key: strconv.FormatInt(it.ID, 10), Title: title, Start: it.Start, Due: it.Due, Source: sourceIterations}
}

func cmpFirst(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// dateRange formats "3–14 Nov", or "28 Nov – 9 Dec" across months.
func dateRange(start, due time.Time) string {
	switch {
	case start.Equal(due):
		return start.Format("2 Jan")
	case start.Year() == due.Year() && start.Month() == due.Month():
		return fmt.Sprintf("%d–%s", start.Day(), due.Format("2 Jan"))
	}
	return start.Format("2 Jan") + " – " + due.Format("2 Jan")
}

// SprintsFromLabels derives sprints from numbered labels ("Sprint::{n}") seen
// on issues. Sprint n starts cadenceStart + (n-1)·length (so cadenceStart is
// sprint 1's first day) and lasts length, rounded down to whole days; Due is
// its last day. Labels that do not match the pattern or whose number is not a
// positive integer are ignored.
func SprintsFromLabels(pattern string, cadenceStart forge.Date, length time.Duration, labels []string) []Sprint {
	days := int(length / (24 * time.Hour))
	if days < 1 {
		return nil
	}
	var out []Sprint
	seen := map[string]bool{}
	for _, label := range labels {
		v, ok := matchPattern(pattern, label)
		if !ok || seen[strings.ToLower(label)] {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			continue
		}
		seen[strings.ToLower(label)] = true
		start := cadenceStart.AddDate(0, 0, (n-1)*days)
		out = append(out, Sprint{
			Key:    label,
			Title:  label,
			Start:  forge.Date{Time: start},
			Due:    forge.Date{Time: start.AddDate(0, 0, days-1)},
			Source: sourceLabels,
		})
	}
	sortSprints(out)
	return out
}

// SprintOf finds the sprint an issue belongs to under src: its iteration, its
// milestone (by title), or its newest matching sprint label. For auto (or an
// unlisted source) the first of those that applies wins. Iterations and
// milestones missing from sprints are built from the issue's own data.
func SprintOf(issue forge.Issue, src config.IterationsSource, sprints []Sprint) *Sprint {
	switch src.Source {
	case sourceIterations, "github-project":
		return issueIteration(issue, sprints)
	case sourceMilestones:
		return issueMilestone(issue, sprints)
	case sourceLabels:
		return issueLabel(issue, src.Pattern, sprints)
	}
	if s := issueIteration(issue, sprints); s != nil {
		return s
	}
	if s := issueMilestone(issue, sprints); s != nil {
		return s
	}
	return issueLabel(issue, src.Pattern, sprints)
}

func issueIteration(issue forge.Issue, sprints []Sprint) *Sprint {
	it := issue.Iteration
	if it == nil {
		return nil
	}
	key := strconv.FormatInt(it.ID, 10)
	for _, s := range sprints {
		if s.Source == sourceIterations && s.Key == key {
			return &s
		}
	}
	if it.Start.IsZero() || it.Due.IsZero() {
		return nil
	}
	s := iterationSprint(*it, "")
	return &s
}

func issueMilestone(issue forge.Issue, sprints []Sprint) *Sprint {
	m := issue.Milestone
	if m == nil {
		return nil
	}
	for _, s := range sprints {
		if s.Source == sourceMilestones && s.Title == m.Title {
			return &s
		}
	}
	if m.Start.IsZero() || m.Due.IsZero() {
		return nil
	}
	s := milestoneSprint(*m)
	return &s
}

func issueLabel(issue forge.Issue, pattern string, sprints []Sprint) *Sprint {
	var best *Sprint
	for _, label := range issue.Labels {
		if _, ok := matchPattern(pattern, label); !ok {
			continue
		}
		for _, s := range sprints {
			if s.Source == sourceLabels && strings.EqualFold(s.Key, label) && (best == nil || s.Start.After(best.Start.Time)) {
				best = &s
			}
		}
	}
	return best
}
