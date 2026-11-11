package audit

import (
	"testing"
	"time"
)

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"*", "anything.at.all", true},
		{"git.pull", "git.pull", true},
		{"git.pull", "git.pull.failed", false},
		{"git.*", "git.pull.failed", true},
		{"git.*", "gitx.pull", false},
		{"git.*.failed", "git.pull.failed", true},
		{"git.*.failed", "git.pull.ok", false},
		{"*.failed", "task.failed", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "aXcYb", false},
		{"a*a", "a", false},
	}
	for _, c := range cases {
		if got := GlobMatch(c.pat, c.name); got != c.want {
			t.Errorf("GlobMatch(%q,%q) = %v, want %v", c.pat, c.name, got, c.want)
		}
	}
}

func TestQueryFilters(t *testing.T) {
	l, err := Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	seed := []Entry{
		{TS: base.AddDate(0, -1, 0), Action: "git.clone", Project: "p1"},  // previous month file
		{TS: base, Action: "git.pull", Project: "p1", Item: "p1/api#1"},   // March
		{TS: base.Add(48 * time.Hour), Action: "hook.run", Project: "p2"}, // April file
		{TS: base.Add(49 * time.Hour), Action: "git.pull.failed", Project: "p2", Outcome: "error"},
	}
	for _, e := range seed {
		if err := l.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	actions := func(f Filter) []string {
		got, err := l.Query(f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range got {
			out = append(out, e.Action)
		}
		return out
	}
	eq := func(name string, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v, want %v", name, got, want)
			}
		}
	}
	eq("all, oldest first", actions(Filter{}), []string{"git.clone", "git.pull", "hook.run", "git.pull.failed"})
	eq("since skips old month", actions(Filter{Since: base}), []string{"git.pull", "hook.run", "git.pull.failed"})
	eq("since mid-month", actions(Filter{Since: base.Add(time.Hour)}), []string{"hook.run", "git.pull.failed"})
	eq("action glob", actions(Filter{Action: "git.*"}), []string{"git.clone", "git.pull", "git.pull.failed"})
	eq("limit keeps newest", actions(Filter{Limit: 2}), []string{"hook.run", "git.pull.failed"})
	eq("glob+limit", actions(Filter{Action: "git.*", Limit: 1}), []string{"git.pull.failed"})
	eq("project", actions(Filter{Project: "p2"}), []string{"hook.run", "git.pull.failed"})
	eq("item", actions(Filter{Item: "p1/api#1"}), []string{"git.pull"})
}
