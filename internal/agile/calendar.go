// Package agile builds the standup and the sprint review from forge data, the
// cached work items and the audit log. Builders are pure over their inputs
// (callers pass the clock and the fetched data); the live collectors in
// collect.go do the fetching.
package agile

import (
	"fmt"
	"strconv"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
)

// Calendar says which days are working days and in which zone a day starts.
type Calendar struct {
	Workdays []time.Weekday
	Holidays map[string]bool // "YYYY-MM-DD"
	Loc      *time.Location
}

// NewCalendar reads user.workdays, user.holidays and user.timezone. Empty
// workdays mean Monday to Friday and an empty timezone the local zone.
func NewCalendar(u config.User) (Calendar, error) {
	days, err := domain.ParseWorkdays(u.Workdays)
	if err != nil {
		return Calendar{}, fmt.Errorf("user.workdays: %w", err)
	}
	if len(days) == 0 {
		days = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}
	}
	c := Calendar{Workdays: days, Holidays: map[string]bool{}, Loc: time.Local}
	for _, h := range u.Holidays {
		d, err := time.Parse("2006-01-02", h)
		if err != nil {
			return Calendar{}, fmt.Errorf("user.holidays: %q is not a YYYY-MM-DD date", h)
		}
		c.Holidays[d.Format("2006-01-02")] = true
	}
	if u.Timezone != "" {
		if c.Loc, err = time.LoadLocation(u.Timezone); err != nil {
			return Calendar{}, fmt.Errorf("user.timezone: %w", err)
		}
	}
	return c, nil
}

func (c Calendar) loc() *time.Location {
	if c.Loc == nil {
		return time.Local
	}
	return c.Loc
}

// Day truncates t to midnight of its calendar day in the calendar's zone.
func (c Calendar) Day(t time.Time) time.Time {
	y, m, d := t.In(c.loc()).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, c.loc())
}

// IsWorkday reports whether the calendar day of t is a working day.
func (c Calendar) IsWorkday(t time.Time) bool {
	d := c.Day(t)
	if c.Holidays[d.Format("2006-01-02")] {
		return false
	}
	days := c.Workdays
	if len(days) == 0 {
		return d.Weekday() >= time.Monday && d.Weekday() <= time.Friday
	}
	for _, w := range days {
		if d.Weekday() == w {
			return true
		}
	}
	return false
}

// PrevWorkday is the latest working day strictly before the day of t. With no
// working day at all in the last year it returns the day before t.
func (c Calendar) PrevWorkday(t time.Time) time.Time {
	d := c.Day(t)
	for range 366 {
		d = d.AddDate(0, 0, -1)
		if c.IsWorkday(d) {
			return d
		}
	}
	return c.Day(t).AddDate(0, 0, -1)
}

// WorkdaysBetween lists the working days in [from, to] by calendar day.
func (c Calendar) WorkdaysBetween(from, to time.Time) []time.Time {
	var out []time.Time
	for d := c.Day(from); !d.After(c.Day(to)); d = d.AddDate(0, 0, 1) {
		if c.IsWorkday(d) {
			out = append(out, d)
		}
	}
	return out
}

// sprintDay is a sprint's calendar date (UTC midnight, as forge.Date holds
// it) as midnight in the calendar's zone.
func (c Calendar) sprintDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, c.loc())
}

func strconvKey(id int64) string { return strconv.FormatInt(id, 10) }
