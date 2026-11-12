package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/records"
)

func (e *cliEnv) note(args ...string) (string, error) { return e.run(newNoteCmd, args...) }

func TestNoteAppendsDatedLinesAndPrintsThemNewestFirst(t *testing.T) {
	e := notesEnv(t)
	ref := forge.Ref{Project: trackerProj, IID: 1, Kind: forge.KindIssue}
	e.writeRecord(ref, "my own words\n")

	if out, err := e.note(ref.String()); err != nil || !strings.Contains(out, "no notes yet") {
		t.Fatalf("empty: %v\n%s", err, out)
	}
	if out, err := e.note(ref.String(), "--blocked"); err == nil {
		t.Fatalf("--blocked without text was accepted:\n%s", out)
	}
	if out, err := e.note(ref.String(), "waiting", "for", "support", "--blocked"); err != nil || !strings.Contains(out, "[blocked] waiting for support") {
		t.Fatalf("blocked: %v\n%s", err, out)
	}
	out, err := e.note("1", "key arrived")
	if err != nil {
		t.Fatal(err)
	}
	if i, j := strings.Index(out, "key arrived"), strings.Index(out, "waiting for support"); i < 0 || j < 0 || i > j {
		t.Errorf("want newest first:\n%s", out)
	}
	got, _ := records.New(e.app.RecordsDir).ReadNotes("sandbox-gitlab", ref)
	if !strings.HasPrefix(got, "my own words\n") || strings.Count(got, "\n- 20") != 2 {
		t.Errorf("notes = %q: the user's text stays, two dated lines follow", got)
	}

	asJSON(t)
	out, err = e.note(ref.String())
	var js noteOut
	if err != nil || json.Unmarshal([]byte(out), &js) != nil || len(js.Notes) != 2 || js.Blocked || js.Notes[0].Text != "key arrived" || !js.Notes[1].Blocked || js.Forge != "sandbox-gitlab" {
		t.Fatalf("json = %+v err %v\n%s", js, err, out)
	}
}

func TestNoteMakesTheRecordFromTheCache(t *testing.T) {
	e := notesEnv(t)
	ref := forge.Ref{Project: trackerProj, IID: 7, Kind: forge.KindIssue}
	st, err := e.app.Cache()
	if err != nil {
		t.Fatal(err)
	}
	cacheResults(t, st, domain.WorkItem{Project: "demo-gl", Ref: ref, Issue: forge.Issue{Ref: ref, Title: "Never synced"}})

	if out, err := e.note(ref.String(), "started on it"); err != nil || !strings.Contains(out, "started on it") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := records.New(e.app.RecordsDir).Read("sandbox-gitlab", ref); err != nil {
		t.Errorf("record was not made: %v", err)
	}
	if _, err := e.note(trackerProj+"#99", "x"); err == nil {
		t.Error("a note on an item that is neither recorded nor cached was accepted")
	}
}
