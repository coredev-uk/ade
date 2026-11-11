package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseSprintLength(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"2w":  14 * 24 * time.Hour,
		"14d": 14 * 24 * time.Hour,
		"1W":  7 * 24 * time.Hour,
	} {
		got, err := ParseSprintLength(in)
		if err != nil || got != want {
			t.Errorf("ParseSprintLength(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "w", "0w", "-2w", "2", "2m", "xw"} {
		if _, err := ParseSprintLength(in); err == nil {
			t.Errorf("ParseSprintLength(%q) accepted", in)
		}
	}
}

func TestValidateIterations(t *testing.T) {
	parse := func(iter string) []Issue {
		m, err := ParseManifest([]byte("name = \"x\"\n[tracker]\niterations = "+iter+"\n"), "x.ade.toml")
		if err != nil {
			t.Fatal(err)
		}
		return m.Validate(nil)
	}
	if got := parse(`{ source = "labels", pattern = "Sprint::{n}", start = "2026-11-02", length = "2w", overlap = 2 }`); HasErrors(got) {
		t.Errorf("valid iterations rejected: %v", got)
	}
	for iter, path := range map[string]string{
		`{ start = "02/11/2026" }`: "tracker.iterations.start",
		`{ length = "fortnight" }`: "tracker.iterations.length",
		`{ overlap = -1 }`:         "tracker.iterations.overlap",
	} {
		found := false
		for _, is := range parse(iter) {
			found = found || (is.Severity == SeverityError && strings.HasPrefix(is.Path, path))
		}
		if !found {
			t.Errorf("%s: no error at %s", iter, path)
		}
	}
}
