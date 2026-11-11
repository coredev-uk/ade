package app

import (
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
)

func TestEnforceRetentionDeletesOldMonthsAndAuditsIt(t *testing.T) {
	al, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)
	for _, ts := range []time.Time{now.AddDate(0, -8, 0), now.AddDate(0, -6, 0), now.AddDate(0, -1, 0), now} {
		if err := al.Append(audit.Entry{TS: ts, Action: "seed"}); err != nil {
			t.Fatal(err)
		}
	}
	enforceRetention(al, "forever", now)
	if es, _ := al.Query(audit.Filter{Action: "audit.prune"}); len(es) != 0 {
		t.Fatalf("forever must not prune or log: %v", es)
	}
	enforceRetention(al, "120d", now)
	es, err := al.Query(audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var seeds, prunes []audit.Entry
	for _, e := range es {
		if e.Action == "seed" {
			seeds = append(seeds, e)
		} else {
			prunes = append(prunes, e)
		}
	}
	if len(seeds) != 2 {
		t.Fatalf("kept %d seed entries, want the last two months: %v", len(seeds), es)
	}
	if len(prunes) != 1 || prunes[0].Action != "audit.prune" || prunes[0].Target != "2025-07,2025-09" || prunes[0].Outcome != "ok" {
		t.Fatalf("prune entry = %+v", prunes)
	}
	// Second run has nothing left to do and adds no line.
	enforceRetention(al, "120d", now)
	if again, _ := al.Query(audit.Filter{Action: "audit.prune"}); len(again) != 1 {
		t.Fatalf("pruning twice logged twice: %v", again)
	}
}

func TestEnforceRetentionMalformedValueDeletesNothingAndStaysQuiet(t *testing.T) {
	al, _ := audit.Open(t.TempDir(), "user")
	old := time.Now().AddDate(-3, 0, 0)
	if err := al.Append(audit.Entry{TS: old, Action: "seed"}); err != nil {
		t.Fatal(err)
	}
	enforceRetention(al, "soon", time.Now())
	es, _ := al.Query(audit.Filter{})
	if len(es) != 1 || es[0].Action != "seed" {
		t.Fatalf("%v", es)
	}
}
