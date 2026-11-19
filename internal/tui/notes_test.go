package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/records"
)

// newNotesEnv is Home with tracker#11 selected and a record for it.
func newNotesEnv(t *testing.T) (*swEnvT, *records.Store) {
	t.Helper()
	e := newClassifyEnv(t)
	e.m.results[0].Forge = "sandbox-gitlab"
	e.m.w = 220 // a preview pane wide enough that the notes keep their phrases on one line
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

func ctrlKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

func (e *swEnvT) pressMsg(k tea.KeyPressMsg) {
	e.t.Helper()
	next, cmd := e.m.Update(k)
	e.m = next.(model)
	e.settle(cmd)
}

// Text written freely under `## Notes` (in $EDITOR, by hand) is the notes: the
// detail pane shows it as written, not "none yet".
func TestDetailPaneShowsFreeFormNotes(t *testing.T) {
	e, st := newNotesEnv(t)
	e.want("none yet · n writes one")
	if err := st.WriteNotes("sandbox-gitlab", trk(11), "support says totals lag after a filter change\n\nask Rae about the cache key"); err != nil {
		t.Fatal(err)
	}
	e.m.loadNotes()
	e.want("support says totals lag after a filter change", "ask Rae about the cache key", "n edits")
	e.wantNot("none yet")
}

// Long notes show their first lines, cut with a faint ellipsis like the
// description preview.
func TestDetailPaneCutsLongNotes(t *testing.T) {
	e, st := newNotesEnv(t)
	var lines []string
	for i := range 30 {
		lines = append(lines, fmt.Sprintf("line %02d of the notes", i))
	}
	if err := st.WriteNotes("sandbox-gitlab", trk(11), strings.Join(lines, "\n\n")); err != nil {
		t.Fatal(err)
	}
	e.m.loadNotes()
	e.want("line 00 of the notes", "…")
	e.wantNot("line 29 of the notes")
}

// n turns the pane's notes into an editor prefilled with them; typing and esc
// save only the notes section, and the pane shows the text at once, with no
// sync. Home's keys type while it is open.
func TestNotesEditInPlaceSavesOnEscWithoutASync(t *testing.T) {
	e, st := newNotesEnv(t)
	if err := st.WriteNotes("sandbox-gitlab", trk(11), "kept line"); err != nil {
		t.Fatal(err)
	}
	e.m.loadNotes()
	sel := e.m.homeSel
	e.press("n")
	if e.m.ne == nil {
		t.Fatal("n on a Home item opens the notes editor")
	}
	e.want("editing", "kept line", "ctrl+s save", "esc save and close", "ctrl+e edit in")
	e.typ("check the export job")
	e.press("enter")
	e.typ("q j k")
	if e.m.ne == nil || e.m.homeSel != sel {
		t.Fatal("Home's keys acted while the notes were edited")
	}
	e.press("esc")
	if e.m.ne != nil {
		t.Fatal("esc leaves the editor")
	}
	got, _ := st.ReadNotes("sandbox-gitlab", trk(11))
	if got != "kept line\ncheck the export job\nq j k\n" {
		t.Errorf("notes = %q", got)
	}
	e.want("check the export job", "q j k")
	e.wantNot("editing")
	if e.m.syncing || e.m.updating {
		t.Error("saving the notes started a refresh")
	}
	rec, _ := st.Read("sandbox-gitlab", trk(11))
	if b, _ := os.ReadFile(rec.Path); !strings.Contains(string(b), "# Exports page sometimes shows stale totals") {
		t.Errorf("record lost its snapshot:\n%s", b)
	}
}

// An item no sync has written a record for gets one from the cache: notes
// never wait for a sync. ctrl+s saves like esc.
func TestNotesEditMakesTheRecordFromTheCache(t *testing.T) {
	e, _ := newNotesEnv(t)
	e.m.results[0].Items = append(e.m.results[0].Items, e.item(12, "Never synced"))
	e.m.rebuildHome("")
	e.selectRow(trk(12).String())
	e.press("n")
	st := records.New(e.app.RecordsDir)
	if _, err := st.Read("sandbox-gitlab", trk(12)); err != nil {
		t.Fatalf("n did not make the record: %v", err)
	}
	e.want("your notes on this item")
	e.typ("first words")
	e.pressMsg(ctrlKey('s'))
	if e.m.ne != nil {
		t.Fatal("ctrl+s leaves the editor")
	}
	if got, _ := st.ReadNotes("sandbox-gitlab", trk(12)); got != "first words\n" {
		t.Errorf("notes = %q", got)
	}
	e.want("first words")
}

// ctrl+e saves what was typed and hands the record to the editor at the
// notes; the pane shows what the editor wrote when it returns.
func TestNotesEditCtrlEHandsTheRecordToTheEditor(t *testing.T) {
	e, st := newNotesEnv(t)
	e.app.Config.Host.Editor = "$EDITOR"
	t.Setenv("VISUAL", "my-test-editor")
	var got *exec.Cmd
	var before string
	old := notesExec
	notesExec = func(c *exec.Cmd, done func(error) tea.Msg) tea.Cmd {
		got = c
		before, _ = st.ReadNotes("sandbox-gitlab", trk(11))
		return func() tea.Msg {
			if err := st.WriteNotes("sandbox-gitlab", trk(11), "written in the editor"); err != nil {
				t.Error(err)
			}
			return done(nil)
		}
	}
	t.Cleanup(func() { notesExec = old })

	e.press("n")
	e.typ("typed first")
	e.pressMsg(ctrlKey('e'))
	if got == nil {
		t.Fatal("ctrl+e did not start the editor")
	}
	rec, _ := st.Read("sandbox-gitlab", trk(11))
	if args := strings.Join(got.Args, " "); !strings.Contains(args, rec.Path) || !strings.Contains(args, "my-test-editor") {
		t.Errorf("editor command %q: want host.editor and the record", args)
	}
	if before != "typed first\n" {
		t.Errorf("notes when the editor started = %q: want what was typed", before)
	}
	if e.m.ne != nil {
		t.Error("the inline editor stayed open behind $EDITOR")
	}
	e.want("written in the editor")
}

// Below 100 columns the editor takes the detail view without tab, and the
// list comes back when it closes.
func TestNotesEditOnANarrowTerminal(t *testing.T) {
	e, _ := newNotesEnv(t)
	e.m.w, e.m.h = 80, 30
	e.press("n")
	e.want("editing")
	e.press("esc")
	e.wantNot("editing")
	if e.m.showDetail {
		t.Error("closing the editor left the detail view on")
	}
}

func TestNotesEditOnlyOnAnItemRow(t *testing.T) {
	e, _ := newNotesEnv(t)
	e.m.page = pageTrees
	e.press("n")
	if e.m.ne != nil {
		t.Error("the notes editor opened off the Home page")
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
	e.m.w, e.m.h = 220, 48
	e.press("N")
	if e.m.nt == nil || e.m.nt.rs != nil {
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

func TestNotesAreReadOncePerRefreshNotPerRender(t *testing.T) {
	e, st := newNotesEnv(t)
	e.m.w, e.m.h = 220, 48
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

// On the MR page, n on Overview edits the item's notes the same way; on
// Review, n stays deny.
func TestMRPageOverviewEditsTheItemsNotes(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.m.app.RecordsDir = t.TempDir()
	selectTracker(e)
	e.press("enter")
	if e.m.mr == nil || e.m.mr.tab != tabOverview || e.m.mr.item == nil {
		t.Fatal("enter on the item did not open its MR page on Overview")
	}
	e.want("n edit notes")
	e.press("n")
	if e.m.ne == nil {
		t.Fatal("n on Overview did not open the notes editor")
	}
	e.want("editing", "ctrl+s save")
	e.typ("ask about the burst")
	e.press("esc")
	tr := forge.Ref{Project: grp + "/tracker", IID: 1}
	if got, _ := records.New(e.m.app.RecordsDir).ReadNotes("f", tr); got != "ask about the burst\n" {
		t.Errorf("notes = %q", got)
	}
	if e.m.mr == nil {
		t.Fatal("esc in the editor left the MR page")
	}
	e.want("ask about the burst")

	e.press("4")
	e.press("n")
	if e.m.ne != nil {
		t.Error("n on Review opened the notes editor")
	}
}
