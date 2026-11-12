package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func TestNoteStampsInTheConfiguredTimezone(t *testing.T) {
	e := notesEnv(t)
	e.app.Config.User.Timezone = "Pacific/Kiritimati" // UTC+14, far from any CI zone
	ref := forge.Ref{Project: trackerProj, IID: 1, Kind: forge.KindIssue}
	e.writeRecord(ref, "")
	if out, err := e.note(ref.String(), "zoned"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	text, _ := records.New(e.app.RecordsDir).ReadNotes("sandbox-gitlab", ref)
	got := records.ParseNotes(text)
	loc, _ := time.LoadLocation("Pacific/Kiritimati")
	wall := time.Now().In(loc)
	want := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, time.UTC)
	if len(got) != 1 || got[0].At.Sub(want).Abs() > 2*time.Minute {
		t.Errorf("notes = %+v from %q: want a stamp near %s", got, text, want.Format("2006-01-02 15:04"))
	}
}
