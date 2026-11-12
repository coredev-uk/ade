package ai

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

func note(user, body string, at time.Time, system bool) forge.Note {
	return forge.Note{Author: forge.User{Username: user}, Body: body, CreatedAt: at, System: system}
}

func TestCollectNewTakesOnlyOthersHumanCommentsAfterTheMarker(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }
	in := DigestInput{
		Ref: forge.Ref{Project: "g/api", IID: 2, Kind: forge.KindMR}, Title: "Rename", Me: "me",
		Discussions: []forge.Discussion{
			{ID: "a", Notes: []forge.Note{note("rae", "OLD", at(1), false), note("rae", "NEW-REPLY", at(5), false)}},
			{ID: "b", Position: &forge.Position{Path: "x.go", NewLine: 7}, Notes: []forge.Note{note("sam", "NEW-ON-LINE", at(4), false)}},
			{ID: "c", Notes: []forge.Note{note("me", "MINE", at(6), false)}},
			{ID: "d", Notes: []forge.Note{note("bot", "label added", at(7), true)}},
		},
	}
	c := CollectNew(in, at(2))
	var bodies []string
	for _, n := range c.Notes {
		bodies = append(bodies, n.Body+"@"+n.Where)
	}
	want := []string{"NEW-ON-LINE@x.go:7", "NEW-REPLY@reply in rae's thread on overview"}
	if strings.Join(bodies, "|") != strings.Join(want, "|") {
		t.Fatalf("new = %v, want %v", bodies, want)
	}
	// The marker catches up with my own comment, not with the system note.
	if !c.Newest.Equal(at(6)) {
		t.Fatalf("newest = %v, want %v", c.Newest, at(6))
	}
	// Nothing is new at or after the newest comment.
	if c := CollectNew(in, at(6)); c.Count() != 0 || !c.Newest.Equal(at(6)) {
		t.Fatalf("after the marker: %+v", c)
	}
	// The first look (zero marker) takes everything by others.
	if c := CollectNew(in, time.Time{}); c.Count() != 3 {
		t.Fatalf("first look = %d, want 3", c.Count())
	}
}

func TestDigestJobIsStableAndBoundsTheComments(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	in := DigestInput{Ref: forge.Ref{Project: "g/api", IID: 9, Kind: forge.KindIssue}, Title: "T"}
	var ns []forge.Note
	for i := range maxDigestNotes + 5 {
		ns = append(ns, note("rae", fmt.Sprintf("comment %02d", i), t0.Add(time.Duration(i)*time.Minute), false))
	}
	in.Discussions = []forge.Discussion{{ID: "x", Notes: ns}}
	c := CollectNew(in, time.Time{})
	if len(c.Notes) != maxDigestNotes || c.Omitted != 5 || c.Count() != maxDigestNotes+5 {
		t.Fatalf("bound: %d notes, %d omitted", len(c.Notes), c.Omitted)
	}
	j := DigestJob("gl", in, c)
	if strings.Contains(j.Prompt, "comment 00") || !strings.Contains(j.Prompt, "comment 44") || !strings.Contains(j.Prompt, "5 older new comments are left out") {
		t.Fatalf("the newest comments are kept:\n%s", j.Prompt)
	}
	if again := DigestJob("gl", in, CollectNew(in, time.Time{})); again.Prompt != j.Prompt {
		t.Fatal("the same comments must give the same prompt (the cache key)")
	}
	if j.Feature != "digest" || j.Name != "digest-issue9" {
		t.Fatalf("job = %s %s", j.Feature, j.Name)
	}
}
