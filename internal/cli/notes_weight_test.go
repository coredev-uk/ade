package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/store"
)

// cacheResults stores a synced project holding items, as `ade sync` leaves it.
func cacheResults(t *testing.T, st *store.Store, items ...domain.WorkItem) {
	t.Helper()
	state, err := json.Marshal(forgesync.Result{Project: "demo-gl", Forge: "sandbox-gitlab", Tracker: trackerProj})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO sync_state (project, json) VALUES (?, ?)`, "demo-gl", string(state)); err != nil {
		t.Fatal(err)
	}
	for _, wi := range items {
		b, _ := json.Marshal(wi)
		if _, err := st.DB.Exec(`INSERT INTO work_items (project, ref, json) VALUES (?, ?, ?)`, "demo-gl", wi.Ref.String(), string(b)); err != nil {
			t.Fatal(err)
		}
	}
}

const (
	trackerProj = fake.GroupPath + "/tracker"
	apiProject  = fake.GroupPath + "/api"
)

// notesEnv is the AI env with the sandbox manifest registered and a token the
// real forge opener can use against the fake.
func notesEnv(t *testing.T) *cliEnv {
	t.Helper()
	e := classifyEnv(t)
	t.Setenv("ADE_TEST_FORGE_TOKEN", fake.Token)
	f := e.app.Config.Forges["sandbox-gitlab"]
	f.TokenEnv = "ADE_TEST_FORGE_TOKEN"
	e.app.Config.Forges["sandbox-gitlab"] = f
	return e
}

func (e *cliEnv) issue(args ...string) (string, error) { return e.run(newIssueCmd, args...) }

func (e *cliEnv) mustIssue(args ...string) string {
	e.t.Helper()
	out, err := e.issue(args...)
	if err != nil {
		e.t.Fatalf("ade issue %v: %v\n%s", args, err, out)
	}
	return out
}

func labelsOf(t *testing.T, e *cliEnv, project string, iid int) []string {
	t.Helper()
	is, err := gitlab.New(e.srv.URL, fake.Token).Issue(context.Background(), forge.Ref{Project: project, IID: iid, Kind: forge.KindIssue})
	if err != nil {
		t.Fatal(err)
	}
	return is.Labels
}

func TestIssueWeightSwapsLabelsOnFreeAndChildFollows(t *testing.T) {
	e := notesEnv(t)
	api := gitlab.New(e.srv.URL, fake.Token)
	ctx := context.Background()
	// tracker#1 and its child api#1 carry the old weight.
	for _, r := range []forge.Ref{{Project: trackerProj, IID: 1, Kind: forge.KindIssue}, {Project: apiProject, IID: 1, Kind: forge.KindIssue}} {
		if err := api.SetLabels(ctx, r, []string{"weight::5"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	tracker := trackerProj + "#1"

	// --dry-run shows the writes and writes nothing.
	out := e.mustIssue("weight", tracker, "2", "--dry-run")
	if !strings.Contains(out, "would write: "+tracker+": weight 5 → 2 (label: add weight::2; remove weight::5)") ||
		!strings.Contains(out, "(child)") {
		t.Fatalf("dry run:\n%s", out)
	}
	if got := labelsOf(t, e, trackerProj, 1); !slices.Contains(got, "weight::5") {
		t.Fatalf("a dry run wrote: %v", got)
	}
	if len(e.audited("forge.issue.weight")) != 0 {
		t.Fatal("a dry run is not audited as a write")
	}

	out = e.mustIssue("weight", tracker, "2")
	if !strings.Contains(out, "done: "+tracker) {
		t.Fatalf("output:\n%s", out)
	}
	for _, c := range []struct {
		proj string
	}{{trackerProj}, {apiProject}} {
		if got := labelsOf(t, e, c.proj, 1); !slices.Contains(got, "weight::2") || slices.Contains(got, "weight::5") {
			t.Errorf("%s#1 labels = %v: want the new weight and the old one explicitly removed", c.proj, got)
		}
	}
	if es := e.audited("forge.issue.weight"); len(es) != 2 || es[0].Params["from"] != "5" || es[0].Params["to"] != "2" || es[1].Params["child"] != true {
		t.Fatalf("audit = %+v", es)
	}

	// Same weight again: nothing to do and nothing audited.
	if out = e.mustIssue("weight", tracker, "2"); !strings.Contains(out, "already 2") || len(e.audited("forge.issue.weight")) != 2 {
		t.Fatalf("repeat:\n%s", out)
	}

	// --no-child leaves the child alone; --json reports the plans.
	asJSON(t)
	var res struct {
		Ref     string `json:"ref"`
		DryRun  bool   `json:"dry_run"`
		Results []struct {
			Plan struct {
				Ref    string   `json:"ref"`
				Mode   string   `json:"mode"`
				Add    []string `json:"add"`
				Remove []string `json:"remove"`
				Child  bool     `json:"child"`
			} `json:"plan"`
			Applied bool `json:"applied"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(e.mustIssue("weight", tracker, "none", "--no-child")), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 1 || !res.Results[0].Applied || res.Results[0].Plan.Mode != "label" || !slices.Equal(res.Results[0].Plan.Remove, []string{"weight::2"}) {
		t.Fatalf("json = %+v", res)
	}
	if got := labelsOf(t, e, trackerProj, 1); slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, "weight::") }) {
		t.Errorf("tracker labels after none = %v", got)
	}
	if got := labelsOf(t, e, apiProject, 1); !slices.Contains(got, "weight::2") {
		t.Errorf("--no-child touched the child: %v", got)
	}
}

func TestIssueWeightRejectsBadValuesAndBadRefs(t *testing.T) {
	e := notesEnv(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"weight", trackerProj + "#1", "0"}, "1 or more"},
		{[]string{"weight", trackerProj + "#1", "--", "-2"}, "1 or more"},
		{[]string{"weight", trackerProj + "#1", "heavy"}, "whole number or none"},
		{[]string{"weight", fake.GroupPath + "/api!1", "2"}, "merge request"},
		{[]string{"weight", "other/place#1", "2"}, "not in the tracker project"},
		{[]string{"weight", trackerProj + "#999", "2"}, "not found"},
	} {
		out, err := e.issue(tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, out %q; want %q", tc.args, err, out, tc.want)
		}
	}
	if len(e.audited("forge.issue.weight")) != 0 {
		t.Error("a rejected weight was audited as a write")
	}
}

// writeRecord makes the item's record with the notes, as sync plus the user would.
func (e *cliEnv) writeRecord(ref forge.Ref, notes string) string {
	e.t.Helper()
	st := records.New(e.app.RecordsDir)
	wi := domain.WorkItem{Ref: ref, Issue: forge.Issue{Ref: ref, Title: "Rate-limit the export endpoint", Labels: []string{"type::story"}}, Stage: domain.StageReview, Court: domain.CourtYou}
	if _, _, err := st.WriteSnapshot("sandbox-gitlab", wi, nil, 5); err != nil {
		e.t.Fatal(err)
	}
	if err := st.WriteNotes("sandbox-gitlab", ref, notes); err != nil {
		e.t.Fatal(err)
	}
	return st.ItemPath("sandbox-gitlab", ref)
}

func (e *cliEnv) item(args ...string) (string, error) { return e.run(newItemCmd, args...) }

func TestItemShowsSnapshotAndNotesAndEditsThem(t *testing.T) {
	e := notesEnv(t)
	ref := forge.Ref{Project: trackerProj, IID: 1, Kind: forge.KindIssue}
	path := e.writeRecord(ref, "ask support about 50 req/min\n")

	out, err := e.item(ref.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{ref.String() + " · stage review · court you", "record: " + path, "# Rate-limit the export endpoint", "## Notes", "ask support about 50 req/min"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// A bare number is the registered project's tracker.
	if out2, err := e.item("1"); err != nil || out2 != out {
		t.Errorf("ade item 1: %v\n%s", err, out2)
	}

	asJSON(t)
	out, err = e.item(ref.String())
	var js itemOut
	if err != nil || json.Unmarshal([]byte(out), &js) != nil || js.Notes != "ask support about 50 req/min\n" || js.Path != path ||
		js.Frontmatter["stage"] != "review" || !strings.HasPrefix(js.Snapshot, "# Rate-limit") || js.Forge != "sandbox-gitlab" {
		t.Fatalf("json = %+v err %v\n%s", js, err, out)
	}
	jsonOut = false

	// --edit runs the configured editor on the record and shows the notes afterwards.
	script := filepath.Join(t.TempDir(), "ed.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'added in the editor' >> \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", script)
	e.app.Config.Host.Editor = "$EDITOR"
	out, err = e.item(ref.String(), "--edit")
	if err != nil || !strings.Contains(out, "added in the editor") || !strings.Contains(out, "ask support about 50 req/min") {
		t.Fatalf("--edit: %v\n%s", err, out)
	}
	if n, _ := records.New(e.app.RecordsDir).ReadNotes("sandbox-gitlab", ref); !strings.Contains(n, "added in the editor") {
		t.Errorf("the edit did not land in the record: %q", n)
	}
}

func TestItemWithoutARecordSaysSo(t *testing.T) {
	e := notesEnv(t)
	_, err := e.item(trackerProj + "#9")
	if err == nil || !strings.Contains(err.Error(), "no item record yet") {
		t.Fatalf("err = %v", err)
	}
}

func TestNotesFeedAISummariesAndNoNotesLeavesThemOut(t *testing.T) {
	e, calls := aiEnvFor(t, "[]")
	mr := forge.Ref{Project: apiProject, IID: 2, Kind: forge.KindMR}
	st := records.New(e.app.RecordsDir)
	// The forge name the AI commands resolve to here is the single configured one.
	wi := domain.WorkItem{Ref: mr, Issue: forge.Issue{Ref: mr, Title: "Rename"}}
	if _, _, err := st.WriteSnapshot("sandbox", wi, nil, 5); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteNotes("sandbox", mr, "the rename is blocked on the web team"); err != nil {
		t.Fatal(err)
	}

	out := e.mustAI("summarize", renameMR, "--dry-run")
	if !strings.Contains(out, "includes your notes") {
		t.Fatalf("preview lacks the notes marker:\n%s", out)
	}
	if out = e.mustAI("summarize", renameMR, "--dry-run", "--no-notes"); strings.Contains(out, "notes") {
		t.Fatalf("--no-notes preview mentions notes:\n%s", out)
	}
	asJSON(t)
	var dry aiOut
	if err := json.Unmarshal([]byte(e.mustAI("summarize", renameMR, "--dry-run")), &dry); err != nil {
		t.Fatal(err)
	}
	if !dry.IncludesNotes || dry.Preflight == nil || !dry.Preflight.Notes {
		t.Fatalf("json dry run = %+v", dry)
	}
	jsonOut = false
	if calls() != 0 {
		t.Fatal("a dry run called the model")
	}

	// Notes are part of the cache key: with, without, and with again.
	if out = e.mustAI("summarize", renameMR); !strings.Contains(out, "includes your notes") || calls() != 1 {
		t.Fatalf("first run (calls=%d):\n%s", calls(), out)
	}
	if out = e.mustAI("summarize", renameMR, "--no-notes"); strings.Contains(out, "cached") || calls() != 2 {
		t.Fatalf("--no-notes must not reuse the answer that used notes (calls=%d):\n%s", calls(), out)
	}
	if out = e.mustAI("summarize", renameMR); !strings.Contains(out, "cached · $0.00") || calls() != 2 {
		t.Fatalf("the same notes hit the cache (calls=%d):\n%s", calls(), out)
	}
	if err := st.WriteNotes("sandbox", mr, "now unblocked"); err != nil {
		t.Fatal(err)
	}
	if out = e.mustAI("summarize", renameMR); strings.Contains(out, "cached") || calls() != 3 {
		t.Fatalf("edited notes buy a new answer (calls=%d):\n%s", calls(), out)
	}
}

func TestClassifyJSONCarriesTheWeightSuggestion(t *testing.T) {
	e := classifyEnv(t)
	e.app.Config.AI.Mode = "off"
	tracker := trackerProj + "#11"
	// Closed, weighted bug issues in the cache the suggestion compares with.
	st, err := e.app.Cache()
	if err != nil {
		t.Fatal(err)
	}
	three := 3
	done := domain.WorkItem{
		Project: "demo-gl", Ref: forge.Ref{Project: trackerProj, IID: 10, Kind: forge.KindIssue}, Stage: domain.StageDone, Weight: &three, Type: "bug", Apps: []string{"api"},
		Issue: forge.Issue{Title: "Exports page totals mismatch fixed", State: "closed"},
	}
	cacheResults(t, st, done)
	asJSON(t)
	// Give tracker#11 the facets so only the weight is open.
	if err := gitlab.New(e.srv.URL, fake.Token).SetLabels(context.Background(), forge.Ref{Project: trackerProj, IID: 11, Kind: forge.KindIssue}, []string{"type::bug", "app::api"}, nil); err != nil {
		t.Fatal(err)
	}
	var out classifyOut
	if err := json.Unmarshal([]byte(e.mustAI("classify", tracker)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Suggestion.Weight == nil || *out.Suggestion.Weight != 3 || !strings.Contains(out.Suggestion.WeightReason, "median of 1") || len(out.Suggestion.Labels) != 0 {
		t.Fatalf("suggestion = %+v", out.Suggestion)
	}
	jsonOut = false
	text := e.mustAI("classify", tracker)
	if !strings.Contains(text, "suggested weight: 3") || !strings.Contains(text, "ade issue weight "+tracker+" 3") {
		t.Fatalf("text:\n%s", text)
	}
}
