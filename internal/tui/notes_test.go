package tui

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/records"
)

// newNotesEnv is Home with tracker#11 selected and a record for it.
func newNotesEnv(t *testing.T) (*swEnvT, *records.Store) {
	t.Helper()
	e := newClassifyEnv(t)
	e.m.results[0].Forge = "sandbox-gitlab"
	st := records.New(e.app.RecordsDir)
	wi := e.item(11, "Exports page sometimes shows stale totals")
	if _, _, err := st.WriteSnapshot("sandbox-gitlab", wi, nil, 5); err != nil {
		t.Fatal(err)
	}
	return e, st
}

func (e *swEnvT) open(f func(model) (model, tea.Cmd)) {
	e.t.Helper()
	next, cmd := f(e.m)
	e.m = next
	e.settle(cmd)
}

func TestNotesDialogShowsYourNotesAndWhereTheyGo(t *testing.T) {
	e, st := newNotesEnv(t)
	if err := st.WriteNotes("sandbox-gitlab", trk(11), "support says totals lag only after a filter change\n\n- check the cache key"); err != nil {
		t.Fatal(err)
	}
	e.open(model.openNotes)
	if e.m.nt == nil {
		t.Fatal("n on a Home item opens the dialog")
	}
	e.want("Notes · "+trk(11).String(), "Exports page sometimes shows stale totals", "yours · ADE never rewrites them",
		"support says totals lag only after a filter change", "- check the cache key",
		"Added to agent plans, runs and address-comments prompts", "--no-notes leaves them out", "e edit in", "esc close")
	e.press("esc")
	if e.m.nt != nil {
		t.Fatal("esc closes")
	}
}

func TestNotesDialogWithoutNotesOrRecord(t *testing.T) {
	e, _ := newNotesEnv(t)
	e.open(model.openNotes)
	e.want("no notes yet · press e to write some")
	e.press("esc")

	// An item that was never synced has no record: say so, and do not start an editor.
	e.m.results[0].Items = append(e.m.results[0].Items, e.item(12, "Never synced"))
	e.m.rebuildHome("")
	e.selectRow(trk(12).String())
	called := false
	old := notesExec
	notesExec = func(*exec.Cmd, func(error) tea.Msg) tea.Cmd { called = true; return nil }
	t.Cleanup(func() { notesExec = old })
	e.open(model.openNotes)
	e.want("no record for this item yet")
	e.press("e")
	e.want("no record yet")
	if called {
		t.Error("an editor was started for an item without a record")
	}
}

func TestNotesEditOpensTheRecordAtTheNotesAndReloads(t *testing.T) {
	e, st := newNotesEnv(t)
	e.app.Config.Host.Editor = "$EDITOR"
	t.Setenv("VISUAL", "my-test-editor")
	var got *exec.Cmd
	old := notesExec
	notesExec = func(c *exec.Cmd, done func(error) tea.Msg) tea.Cmd {
		got = c
		return func() tea.Msg { return done(nil) }
	}
	t.Cleanup(func() { notesExec = old })

	e.open(model.openNotes)
	rec, _ := st.Read("sandbox-gitlab", trk(11))
	e.press("e")
	if got == nil {
		t.Fatal("e did not start the editor")
	}
	args := strings.Join(got.Args, " ")
	if !strings.Contains(args, rec.Path) || !strings.Contains(args, "my-test-editor") {
		t.Errorf("editor command %q: want host.editor and the record", args)
	}
	// The editor saves; the dialog shows what was written when it returns.
	if err := st.WriteNotes("sandbox-gitlab", trk(11), "written in the editor"); err != nil {
		t.Fatal(err)
	}
	next, cmd := e.m.Update(notesEditedMsg{d: e.m.nt, err: nil})
	e.m = next.(model)
	e.settle(cmd)
	e.want("written in the editor")
	// The snapshot is still ADE's: the editor changed only the notes.
	if b, _ := os.ReadFile(rec.Path); !strings.Contains(string(b), "# Exports page sometimes shows stale totals") {
		t.Errorf("record lost its snapshot:\n%s", b)
	}
}

func TestNotesDialogScrollsLongNotes(t *testing.T) {
	e, st := newNotesEnv(t)
	var lines []string
	for i := range 60 {
		lines = append(lines, "note line "+strings.Repeat("x", i%7)+" "+string(rune('A'+i%26)))
	}
	if err := st.WriteNotes("sandbox-gitlab", trk(11), strings.Join(lines, "\n")); err != nil {
		t.Fatal(err)
	}
	e.m.h = 30
	e.open(model.openNotes)
	e.want("more lines · j scrolls")
	e.wantNot("lines above")
	e.press("j", "j")
	e.want("2 lines above")
}

func TestNotesDialogOnlyOpensOnAnItemRow(t *testing.T) {
	e, _ := newNotesEnv(t)
	e.m.page = pageTrees
	e.open(model.openNotes)
	if e.m.nt != nil {
		t.Error("the notes dialog opened off the Home page")
	}
}

func TestAgentMenuOffersNotesAndRemembersTheToggle(t *testing.T) {
	e, st := newNotesEnv(t)
	if err := st.WriteNotes("sandbox-gitlab", trk(11), "keep the legacy path"); err != nil {
		t.Fatal(err)
	}
	it := e.m.homeRows[e.m.homeSel].item
	d := &agentDialog{stage: agList, item: it, notes: e.m.itemNotes(it)}
	if d.notes != "keep the legacy path" {
		t.Fatalf("itemNotes = %q", d.notes)
	}
	e.m.agt = d
	e.want("your notes", "included")
	e.m.agt = d
	next, _ := e.m.agListKey(d, "n")
	e.m = next
	if !d.noNotes {
		t.Fatal("n should leave the notes out")
	}
	e.want("left out", "press n to include")
	// An item without notes shows nothing about them.
	d2 := &agentDialog{stage: agList, item: &domain.WorkItem{Ref: forge.Ref{Project: "g/t", IID: 1}}}
	e.m.agt = d2
	e.wantNot("your notes")
}
