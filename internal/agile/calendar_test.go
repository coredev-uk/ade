package agile

import (
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
)

func date(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

func utcCal() Calendar {
	c, err := NewCalendar(config.User{Timezone: "UTC", Workdays: []string{"mon", "tue", "wed", "thu", "fri"}})
	if err != nil {
		panic(err)
	}
	return c
}

func TestPrevWorkday(t *testing.T) {
	holiday := utcCal()
	holiday.Holidays["2026-11-09"] = true
	sunThu, err := NewCalendar(config.User{Timezone: "UTC", Workdays: []string{"sun", "mon", "tue", "wed", "thu"}})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		cal  Calendar
		from string
		want string
	}{
		{"monday looks back to friday", utcCal(), "2026-11-09", "2026-11-06"},
		{"tuesday looks back to monday", utcCal(), "2026-11-10", "2026-11-09"},
		{"saturday looks back to friday", utcCal(), "2026-11-07", "2026-11-06"},
		{"a holiday monday is skipped", holiday, "2026-11-10", "2026-11-06"},
		{"configured workdays: sunday looks back to thursday", sunThu, "2026-11-08", "2026-11-05"},
		{"configured workdays: monday looks back to sunday", sunThu, "2026-11-09", "2026-11-08"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cal.PrevWorkday(date(tc.from)).Format("2006-01-02"); got != tc.want {
				t.Errorf("PrevWorkday(%s) = %s, want %s", tc.from, got, tc.want)
			}
		})
	}
}

func TestNewCalendarRejectsBadConfig(t *testing.T) {
	for name, u := range map[string]config.User{
		"workday":  {Workdays: []string{"funday"}},
		"holiday":  {Holidays: []string{"25 Dec"}},
		"timezone": {Timezone: "Mars/Olympus"},
	} {
		if _, err := NewCalendar(u); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestWorkdaysBetweenSkipsWeekendsAndHolidays(t *testing.T) {
	c := utcCal()
	c.Holidays["2026-11-11"] = true
	var got []string
	for _, d := range c.WorkdaysBetween(date("2026-11-06"), date("2026-11-13")) {
		got = append(got, d.Format("01-02"))
	}
	want := []string{"11-06", "11-09", "11-10", "11-12", "11-13"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
