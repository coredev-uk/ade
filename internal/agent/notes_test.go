package agent

import (
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/records"
)

func TestNotesFeedPlanRunAndAddressPrompts(t *testing.T) {
	st := records.New(t.TempDir())
	tracker := forge.Ref{Project: "g/tracker", IID: 3, Kind: forge.KindIssue}
	child := forge.Ref{Project: "g/api", IID: 4, Kind: forge.KindIssue}
	for ref, text := range map[forge.Ref]string{tracker: "keep the old CSV path until Friday", child: "", {Project: "g/api", IID: 9, Kind: forge.KindIssue}: "unrelated"} {
		wi := domain.WorkItem{Ref: ref, Issue: forge.Issue{Ref: ref, Title: "T"}}
		if _, _, err := st.WriteSnapshot("gl", wi, nil, 5); err != nil {
			t.Fatal(err)
		}
		if err := st.WriteNotes("gl", ref, text); err != nil {
			t.Fatal(err)
		}
	}
	tgt := Target{Forge: "gl", Tracker: tracker, Child: &child, Worktree: "/w", Repo: "api", Branch: "4-x"}
	c := Context{Issue: forge.Issue{Ref: tracker, Title: "Limiter"}, Threads: []Thread{{ID: "t1", Notes: []forge.Note{{Body: "why?"}}}}}
	if err := c.AddNotes(st, tgt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Notes, "keep the old CSV path") || strings.Contains(c.Notes, "unrelated") {
		t.Fatalf("notes = %q: want the tracker's notes only", c.Notes)
	}
	for name, p := range map[string]string{
		"plan": PlanPrompt(tgt, c), "run": RunPrompt(tgt, c, "the plan"), "address": AddressPrompt(tgt, c),
	} {
		if !strings.Contains(p, "keep the old CSV path until Friday") || !strings.Contains(p, "Your notes on this item") {
			t.Errorf("%s prompt lacks the notes:\n%s", name, p)
		}
	}
	// Without AddNotes (--no-notes) nothing of them is in the prompt.
	bare := Context{Issue: c.Issue}
	if p := PlanPrompt(tgt, bare); strings.Contains(p, "notes") {
		t.Errorf("prompt without notes mentions them:\n%s", p)
	}

	// With a child that has notes too, each block names its ref.
	if err := st.WriteNotes("gl", child, "child note"); err != nil {
		t.Fatal(err)
	}
	var c2 Context
	if err := c2.AddNotes(st, tgt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c2.Notes, "Notes on g/tracker#3:") || !strings.Contains(c2.Notes, "Notes on g/api#4:") {
		t.Errorf("notes = %q", c2.Notes)
	}
}
