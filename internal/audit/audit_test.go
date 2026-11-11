package audit

import (
	"context"
	"os"
	"path/filepath"
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

func monthsOf(t *testing.T, l *Log) []string {
	t.Helper()
	names, _, err := l.months()
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func seedMonths(t *testing.T, l *Log, months ...string) {
	t.Helper()
	for _, m := range months {
		ts, err := time.Parse("2006-01-02", m+"-15")
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Append(Entry{TS: ts, Action: "seed." + m}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPruneDeletesWholeMonthsPastRetentionAndNeverTheCurrent(t *testing.T) {
	l, err := Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	seedMonths(t, l, "2025-09", "2025-10", "2025-11", "2025-12", "2026-01", "2026-02", "2026-03")
	now := time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)

	// 90d before 20 Mar is 20 Dec: Nov ended before that, December still has
	// days inside the window, so the whole of December stays.
	removed, err := l.Prune("90d", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"2025-09", "2025-10", "2025-11"}; !slicesEqual(removed, want) {
		t.Fatalf("removed %v, want %v", removed, want)
	}
	if got, want := monthsOf(t, l), []string{"2025-12", "2026-01", "2026-02", "2026-03"}; !slicesEqual(got, want) {
		t.Fatalf("left %v, want %v", got, want)
	}

	// The retention can be 0 days: everything before this month goes, this month stays.
	if removed, err = l.Prune("0d", now); err != nil || !slicesEqual(removed, []string{"2025-12", "2026-01", "2026-02"}) {
		t.Fatalf("0d removed %v %v", removed, err)
	}
	if got := monthsOf(t, l); !slicesEqual(got, []string{"2026-03"}) {
		t.Fatalf("the current month must survive any retention: %v", got)
	}
	// Surviving entries are intact and still queryable.
	if es, err := l.Query(Filter{}); err != nil || len(es) != 1 || es[0].Action != "seed.2026-03" {
		t.Fatalf("%v %v", es, err)
	}
}

func TestPruneForeverKeepsEverythingAndBadValuesAreErrors(t *testing.T) {
	l, _ := Open(t.TempDir(), "user")
	seedMonths(t, l, "2020-01", "2026-03")
	now := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	for _, retain := range []string{"forever", ""} {
		if removed, err := l.Prune(retain, now); err != nil || len(removed) != 0 {
			t.Fatalf("%q: %v %v", retain, removed, err)
		}
	}
	if got := monthsOf(t, l); len(got) != 2 {
		t.Fatalf("left %v", got)
	}
	for _, bad := range []string{"30", "d", "-5d", "1y", "forever "} {
		if _, err := l.Prune(bad, now); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got := monthsOf(t, l); len(got) != 2 {
		t.Fatalf("a rejected value must delete nothing: %v", got)
	}
}

func TestPruneIgnoresFilesItDoesNotOwn(t *testing.T) {
	l, _ := Open(t.TempDir(), "user")
	seedMonths(t, l, "2020-01")
	other := filepath.Join(l.Dir, "notes.jsonl")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Prune("30d", time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("a non-month file was deleted: %v", err)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFollowSeesNewEntriesFromAnyWriterAndMonthRollover(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, "user")
	other, _ := Open(dir, "other-process")
	if err := l.Append(Entry{TS: time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC), Action: "old"}); err != nil {
		t.Fatal(err)
	}

	got := make(chan Entry, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- l.Follow(ctx, Filter{Action: "forge.*"}, 5*time.Millisecond, func(e Entry) { got <- e })
	}()
	time.Sleep(30 * time.Millisecond) // the follower has taken its starting offsets

	wait := func(want string) {
		t.Helper()
		select {
		case e := <-got:
			if e.Action != want {
				t.Fatalf("got %s, want %s", e.Action, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no %s entry", want)
		}
	}
	other.Append(Entry{TS: time.Date(2026, 3, 31, 23, 5, 0, 0, time.UTC), Action: "git.pull"}) //nolint:errcheck // filtered out
	other.Append(Entry{TS: time.Date(2026, 3, 31, 23, 6, 0, 0, time.UTC), Action: "forge.issue.update"})
	wait("forge.issue.update") // in the file that already existed, and the backlog ("old") is not replayed
	l.Append(Entry{TS: time.Date(2026, 4, 1, 0, 1, 0, 0, time.UTC), Action: "forge.mr.comment"})
	wait("forge.mr.comment") // a file that did not exist when following began

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		t.Fatalf("unexpected extra entry %+v", e)
	default:
	}
}

func TestReadFromLeavesAPartialLineForTheNextPoll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "2026-03.jsonl")
	if err := os.WriteFile(path, []byte(`{"action":"a"}`+"\n"+`{"action":"b`), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen []string
	off, err := readFrom(path, 0, func(e Entry) { seen = append(seen, e.Action) })
	if err != nil || len(seen) != 1 || seen[0] != "a" {
		t.Fatalf("%v %v", seen, err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\"}\n")
	f.Close()
	if _, err := readFrom(path, off, func(e Entry) { seen = append(seen, e.Action) }); err != nil || len(seen) != 2 || seen[1] != "b" {
		t.Fatalf("%v %v", seen, err)
	}
}
