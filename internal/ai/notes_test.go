package ai

import (
	"context"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

func (e *env) writeRecord(ref forge.Ref, noteText string) {
	e.t.Helper()
	wi := domain.WorkItem{Ref: ref, Issue: forge.Issue{Ref: ref, Title: "T"}}
	if _, _, err := e.svc.Records.WriteSnapshot("sandbox", wi, nil, 5); err != nil {
		e.t.Fatal(err)
	}
	if err := e.svc.Records.WriteNotes("sandbox", ref, noteText); err != nil {
		e.t.Fatal(err)
	}
}

func TestNotesReachThePromptAndTheCacheKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tracker := forge.Ref{Project: "grp/tracker", IID: 3, Kind: forge.KindIssue}
	e.writeRecord(tracker, "support wants 50 req/min, not 100")

	mr := forge.MR{Ref: mrRef, Title: "Add limiter", SourceBranch: "f", TargetBranch: "main"}
	issue := &forge.Issue{Ref: forge.Ref{Project: "grp/api", IID: 4, Kind: forge.KindIssue}, Title: "Limiter", Description: "Parent: grp/tracker#3"}
	refs := NoteRefs(mr, issue)
	if len(refs) != 3 || refs[2] != tracker {
		t.Fatalf("NoteRefs = %v: want the MR, its issue and the issue's parent tracker", refs)
	}
	notes, err := e.svc.NotesFor("sandbox", refs...)
	if err != nil || !strings.Contains(notes, "50 req/min") || !strings.Contains(notes, tracker.String()) {
		t.Fatalf("notes = %q, %v", notes, err)
	}

	plain := SummarizeJob("sandbox", mr, forge.Diff{HeadSHA: "h"}, issue)
	with := WithNotes(plain, notes)
	if !with.Notes || plain.Notes || !strings.Contains(with.Prompt, "50 req/min") || strings.Contains(plain.Prompt, "50 req/min") {
		t.Fatalf("WithNotes: notes flag %v/%v, prompt carries notes: %v", with.Notes, plain.Notes, strings.Contains(with.Prompt, "50 req/min"))
	}
	if WithNotes(plain, "  \n").Prompt != plain.Prompt {
		t.Error("blank notes must leave the job untouched")
	}

	// The preview says so, and the notes are part of the cache key.
	pf, cached, err := e.svc.Preview(ctx, with)
	if err != nil || !pf.Notes || cached || !strings.Contains(pf.Describe(), "includes your notes") {
		t.Fatalf("preview: %+v cached=%v err=%v", pf, cached, err)
	}
	if pf, _, _ = e.svc.Preview(ctx, plain); pf.Notes || strings.Contains(pf.Describe(), "notes") {
		t.Errorf("preview without notes mentions them: %s", pf.Describe())
	}
	if _, err := e.svc.Run(ctx, with); err != nil {
		t.Fatal(err)
	}
	if res, err := e.svc.Run(ctx, with); err != nil || !res.CacheHit {
		t.Fatalf("same notes should hit the cache: %+v %v", res, err)
	}
	if res, err := e.svc.Run(ctx, plain); err != nil || res.CacheHit {
		t.Fatalf("--no-notes is a different input and must not hit the notes answer: %+v %v", res, err)
	}
	e.writeRecord(tracker, "support now says 25 req/min")
	notes2, _ := e.svc.NotesFor("sandbox", refs...)
	if res, err := e.svc.Run(ctx, WithNotes(plain, notes2)); err != nil || res.CacheHit {
		t.Fatalf("edited notes must buy a new answer: %+v %v", res, err)
	}
	if !strings.Contains(e.drv.last.Prompt, "25 req/min") {
		t.Errorf("the model did not receive the notes: %q", e.drv.last.Prompt)
	}
}

func TestNotesForSkipsEmptyAndRepeatedRefs(t *testing.T) {
	e := newEnv(t)
	a := forge.Ref{Project: "g/t", IID: 1, Kind: forge.KindIssue}
	b := forge.Ref{Project: "g/t", IID: 2, Kind: forge.KindIssue}
	e.writeRecord(a, "only a")
	e.writeRecord(b, "")
	got, err := e.svc.NotesFor("sandbox", a, b, a, forge.Ref{Project: "g/t", IID: 99, Kind: forge.KindIssue})
	if err != nil || strings.Count(got, "only a") != 1 || strings.Contains(got, "g/t#2") {
		t.Errorf("notes = %q, %v", got, err)
	}
}
