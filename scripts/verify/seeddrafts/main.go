// Command seeddrafts stores a fixed set of review drafts in a cache.db for
// scripts/verify/capture-mr.sh: the captures need an agent's pending, denied
// and over-long drafts on screen, and `ade review add` writes only your own.
// The drafts go straight into the review store, so no AI call is made and
// nothing reaches a forge. Synthetic text only.
//
//	seeddrafts <cache.db> <group/project!N>
//
// The set fits api!1's one file, limit/bucket.go (seven new lines).
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
	"github.com/coredev-uk/ade/internal/store"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: seeddrafts <cache.db> <group/project!N>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "seeddrafts:", err)
		os.Exit(1)
	}
}

func run(dbPath, refArg string) error {
	ref, err := forge.ParseRef(refArg)
	if err != nil {
		return err
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	rs := review.NewStore(st)

	long := "Burst and Rate are both plain ints, so a caller can swap them and the compiler will not notice. " +
		"A Bucket{Rate: 50, Burst: 5} refills ten times faster than it can ever hold, which is almost never what was meant. " +
		"Consider a constructor that validates Burst >= Rate/60, or named option types so the call site reads as intent."
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	drafts := []review.Draft{
		{
			Path: "limit/bucket.go", Line: 5, Source: review.SourceAgent, Severity: review.SeverityBlocking,
			Body: "Rate has no unit in its name: a caller passing per-second values gets a bucket that refills sixty times too slowly. Rename it PerMinute, or take a time.Duration.",
		},
		{Path: "limit/bucket.go", Line: 7, Source: review.SourceAgent, Severity: review.SeveritySuggestion, Body: long},
		{
			Path: "limit/bucket.go", Line: 4, Source: review.SourceAgent, State: review.StateDenied, Severity: review.SeverityNit,
			Body: "Bucket could be exported from its own file, limit/bucket.go is getting crowded.",
		},
		{
			Path: "limit/bucket.go", Line: 3, Source: review.SourceAgent, State: review.StateAccepted, Severity: review.SeverityQuestion,
			Body: "Per-tenant: where does the tenant key live? Bucket holds no identity.",
		},
		{Line: 0, Source: review.SourceYou, Body: "Overall: the shape is right, two comments below."},
	}
	for i, d := range drafts {
		d.CreatedAt = t0.Add(time.Duration(i) * time.Minute)
		d, err = review.Normalize(d, t0)
		if err != nil {
			return fmt.Errorf("draft %d: %w", i, err)
		}
		if err := rs.Put(ref, d); err != nil {
			return err
		}
	}
	fmt.Printf("seeded %d drafts on %s\n", len(drafts), ref)
	return nil
}
