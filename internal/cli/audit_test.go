package cli

import (
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	ok := map[string]time.Time{
		"30m":                  now.Add(-30 * time.Minute),
		"6h":                   now.Add(-6 * time.Hour),
		"1d":                   now.AddDate(0, 0, -1),
		"2w":                   now.AddDate(0, 0, -14),
		"2026-05-01T00:00:00Z": time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	}
	for in, want := range ok {
		got, err := parseSince(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "d", "1x", "-1d", "yesterday", "2026-05-01"} {
		if _, err := parseSince(bad, now); err == nil {
			t.Errorf("parseSince(%q) should fail", bad)
		}
	}
}
