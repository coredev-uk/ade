package tui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

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

	// An item the sync has not written a record for gets one from the cache:
	// notes never wait for a sync.
	e.m.results[0].Items = append(e.m.results[0].Items, e.item(12, "Never synced"))
	e.m.rebuildHome("")
	e.selectRow(trk(12).String())
	called := false
	old := notesExec
	notesExec = func(*exec.Cmd, func(error) tea.Msg) tea.Cmd { called = true; return nil }
	t.Cleanup(func() { notesExec = old })
	e.open(model.openNotes)
	e.want("no notes yet · press e to write some")
	e.wantNot("press U")
	if _, err := records.New(e.app.RecordsDir).Read("sandbox-gitlab", trk(12)); err != nil {
		t.Fatalf("opening the dialog did not make the record: %v", err)
	}
	e.press("e")
	if !called {
		t.Error("e did not open the editor on the record made from the cache")
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

func TestNoteAddSavesADatedLineAndShowsItInTheDetailPane(t *testing.T) {
	e, st := newNotesEnv(t)
	e.m.w, e.m.h = 140, 48
	e.press("N")
	if e.m.nt == nil || !e.m.nt.adding {
		t.Fatal("N on a Home item opens the quick-add form")
	}
	e.want("Note · "+trk(11).String(), "enter save", "tab or !")
	e.press("enter")
	e.want("write the note first")
	e.typ("waiting for support")
	e.press("enter")
	if e.m.nt != nil {
		t.Fatal("enter saves and closes the form")
	}
	got, _ := st.ReadNotes("sandbox-gitlab", trk(11))
	if !strings.Contains(got, " waiting for support\n") || strings.Contains(got, "[blocked]") || !strings.HasPrefix(got, "- 20") {
		t.Errorf("notes = %q: want one dated line", got)
	}
	e.want("notes", "waiting for support")
	e.wantNot("blocked")

	// A leading ! marks the next note blocked: the row and the pane say so.
	e.press("N")
	e.typ("!no API key yet")
	e.want("[blocked] no API key yet")
	e.press("enter")
	got, _ = st.ReadNotes("sandbox-gitlab", trk(11))
	if !strings.Contains(got, "[blocked] no API key yet\n") {
		t.Errorf("notes = %q: want the blocked line", got)
	}
	e.want("blocked", "no API key yet")
	if !strings.Contains(e.screen(), "▲blocked") {
		t.Errorf("the row lacks the blocked marker:\n%s", e.screen())
	}

	// A later note that is not blocked clears it; tab toggles blocked in the form.
	e.m.notes = nil
	e.press("N")
	e.typ("key arrived")
	e.press("enter")
	if records.Blocked(e.m.itemDatedNotes(e.m.homeRows[e.m.homeSel].item)) || strings.Contains(e.screen(), "▲blocked") {
		t.Errorf("a later note did not clear the blocked state:\n%s", e.screen())
	}
	e.press("N", "tab")
	if !e.m.nt.blocked {
		t.Error("tab did not mark the note blocked")
	}
	e.press("esc")
	if e.m.nt != nil {
		t.Error("esc cancels the form")
	}
}

func TestNoteDetailPaneHintAndNewestThree(t *testing.T) {
	e, st := newNotesEnv(t)
	e.m.w, e.m.h = 140, 48
	e.want("N adds one")
	base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	for i, txt := range []string{"first", "second", "third", "fourth"} {
		if err := st.AppendNote("sandbox-gitlab", trk(11), txt, base.Add(time.Duration(i)*time.Hour), false); err != nil {
			t.Fatal(err)
		}
	}
	e.m.loadNotes()
	e.want("fourth", "third", "second", "+ 1 older")
	e.wantNot("first", "N adds one")
}

func TestNotesAreReadOncePerRefreshNotPerRender(t *testing.T) {
	e, st := newNotesEnv(t)
	e.m.w, e.m.h = 140, 48
	if err := st.AppendNote("sandbox-gitlab", trk(11), "cached note", time.Now(), true); err != nil {
		t.Fatal(err)
	}
	e.m.loadNotes()
	e.want("cached note")
	// Edited behind the model's back: the screen keeps the refresh's copy.
	if err := st.WriteNotes("sandbox-gitlab", trk(11), "gone"); err != nil {
		t.Fatal(err)
	}
	e.want("cached note")
	e.m.setResults(e.m.results)
	e.wantNot("cached note")
}

// wallWithin reports whether the naive note time at is within a minute or two
// of now's wall clock in loc.
func wallWithin(at time.Time, loc *time.Location) bool {
	wall := time.Now().In(loc)
	want := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, time.UTC)
	return at.Sub(want).Abs() <= 2*time.Minute
}

func TestNoteAddStampsInTheConfiguredTimezone(t *testing.T) {
	e, st := newNotesEnv(t)
	e.app.Config.User.Timezone = "Pacific/Kiritimati" // UTC+14, far from any CI zone
	e.press("N")
	e.typ("zoned")
	e.press("enter")
	text, _ := st.ReadNotes("sandbox-gitlab", trk(11))
	got := records.ParseNotes(text)
	loc, _ := time.LoadLocation("Pacific/Kiritimati")
	if len(got) != 1 || !wallWithin(got[0].At, loc) {
		t.Errorf("notes = %+v from %q: want a stamp in Pacific/Kiritimati", got, text)
	}
}

func TestNoteAddShowsTheTailOfALongInput(t *testing.T) {
	e, _ := newNotesEnv(t)
	e.m.w, e.m.h = 100, 40
	e.press("N")
	e.typ("THE-HEAD-OF-IT " + strings.Repeat("and a long note ", 8) + "THE-END-OF-IT")
	e.want("THE-END-OF-IT")
	e.wantNot("THE-HEAD-OF-IT")
}
