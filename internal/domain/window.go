package domain

import (
	"fmt"
	"strings"
	"time"
)

var defaultWorkdays = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}

// ParseWorkdays parses weekday names ("mon", "Tuesday", ...), keeping order.
func ParseWorkdays(names []string) ([]time.Weekday, error) {
	out := make([]time.Weekday, 0, len(names))
	for _, n := range names {
		d, ok := parseWeekday(n)
		if !ok {
			return nil, fmt.Errorf("unknown weekday %q", n)
		}
		out = append(out, d)
	}
	return out, nil
}

// parseWeekday accepts the full English name or its three-letter prefix.
func parseWeekday(name string) (time.Weekday, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	for d := time.Sunday; d <= time.Saturday; d++ {
		full := strings.ToLower(d.String())
		if key == full || key == full[:3] {
			return d, true
		}
	}
	return 0, false
}

// day truncates t to its calendar date, as UTC midnight (the form forge.Date
// uses), so dates compare without time-zone or clock noise.
func day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// workdaysBetween counts working days in [from, to], both inclusive.
func workdaysBetween(from, to time.Time, workdays []time.Weekday) int {
	n := 0
	for d := day(from); !d.After(day(to)); d = d.AddDate(0, 0, 1) {
		for _, w := range workdays {
			if d.Weekday() == w {
				n++
				break
			}
		}
	}
	return n
}

func contains(s Sprint, today time.Time) bool {
	return !today.Before(day(s.Start.Time)) && !today.After(day(s.Due.Time))
}

// WindowAt computes the iteration window at now. Current is the sprint whose
// [Start, Due] (inclusive) contains today; when sprints overlap the one that
// started last wins. Next (the earliest sprint starting after Current's Due) is
// set when working days from today to Due, today included, are at most overlap;
// Previous (the latest sprint ending before Current's Start) when working days
// from Start to today, today included, are at most overlap. With no current
// sprint, Current is nil and Next is the earliest sprint still to come.
// DaysLeft counts working days today..Due inclusive. An empty workdays means
// Mon–Fri; sprints with Due before Start are ignored.
func WindowAt(sprints []Sprint, now time.Time, workdays []time.Weekday, overlap int) Window {
	if len(workdays) == 0 {
		workdays = defaultWorkdays
	}
	today := day(now)
	valid := make([]Sprint, 0, len(sprints))
	for _, s := range sprints {
		if !s.Due.Before(s.Start.Time) {
			valid = append(valid, s)
		}
	}
	sortSprints(valid)

	cur := -1
	for i, s := range valid {
		if contains(s, today) {
			cur = i
		}
	}
	if cur < 0 {
		return Window{Next: firstAfter(valid, today)}
	}

	c := valid[cur]
	w := Window{Current: &c}
	w.DaysLeft = workdaysBetween(today, c.Due.Time, workdays)
	if w.DaysLeft <= overlap {
		w.Next = firstAfter(valid, day(c.Due.Time))
	}
	if workdaysBetween(c.Start.Time, today, workdays) <= overlap {
		w.Previous = lastBefore(valid, day(c.Start.Time))
	}
	return w
}

// firstAfter returns the earliest sprint starting after date.
func firstAfter(sorted []Sprint, date time.Time) *Sprint {
	for _, s := range sorted {
		if day(s.Start.Time).After(date) {
			return &s
		}
	}
	return nil
}

// lastBefore returns the latest sprint ending before date.
func lastBefore(sorted []Sprint, date time.Time) *Sprint {
	for i := len(sorted) - 1; i >= 0; i-- {
		if day(sorted[i].Due.Time).Before(date) {
			return &sorted[i]
		}
	}
	return nil
}
