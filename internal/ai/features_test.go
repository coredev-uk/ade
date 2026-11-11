package ai

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/ledger"
	"github.com/coredev-uk/ade/internal/review"
	"github.com/coredev-uk/ade/internal/store"
)

var mrRef = forge.Ref{Project: "grp/api", IID: 7, Kind: forge.KindMR}

func reviewDiff() forge.Diff {
	return forge.Diff{BaseSHA: "b", StartSHA: "s", HeadSHA: "h", Files: []forge.DiffFile{{
		OldPath: "svc/limit.go", NewPath: "svc/limit.go",
		Diff: "@@ -10,3 +10,5 @@ func f() {\n ctx := c\n-old := 1\n+n := 5\n+m := 6\n return\n",
	}}}
}

func newSession(t *testing.T) (*review.Session, *env) {
	t.Helper()
	e := newEnv(t)
	sess, err := review.Resume(review.NewStore(e.st), mrRef, reviewDiff())
	if err != nil {
		t.Fatal(err)
	}
	return sess, e
}

func TestReviewDraftsAreValidatedAgainstTheDiffAndStayPending(t *testing.T) {
	sess, e := newSession(t)
	svc := e.svc
	out := "```json\n" + `[
	  {"path":"svc/limit.go","line":11,"side":"new","severity":"blocking","body":"n is never validated; reject n<=0.","suggestion":"n := max(5, 1)"},
	  {"path":"svc/limit.go","line":11,"side":"old","severity":"question","body":"Why was old removed?"},
	  {"path":"svc/limit.go","line":999,"side":"new","severity":"blocking","body":"nonsense line"},
	  {"path":"nope.go","line":1,"side":"new","severity":"nit","body":"no such file"},
	  {"path":"svc/limit.go","line":12,"side":"new","severity":"catastrophic","body":"bad severity"},
	  {"path":"svc/limit.go","line":12,"side":"new","severity":"nit","body":""}
	]` + "\n```"
	cs, err := ParseReview(out)
	if err != nil {
		t.Fatal(err)
	}
	added, rejected, err := svc.AddReviewDrafts(sess, cs, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 || len(rejected) != 4 {
		t.Fatalf("added %d rejected %d: %+v", len(added), len(rejected), rejected)
	}
	a := added[0]
	if a.Source != review.SourceAgent || a.State != review.StatePending || a.Severity != review.SeverityBlocking || a.Path != "svc/limit.go" || a.Line != 11 || a.Side != review.SideNew {
		t.Fatalf("draft = %+v", a)
	}
	if !strings.Contains(a.Body, "```suggestion:-0+0\nn := max(5, 1)\n```") {
		t.Fatalf("suggestion block missing: %q", a.Body)
	}
	if added[1].Side != review.SideOld || added[1].Line != 11 {
		t.Fatalf("the removed line is addressed on the old side: %+v", added[1])
	}
	for _, d := range added {
		if d.Selected() {
			t.Fatalf("a pending agent draft must not be sent: %+v", d)
		}
	}
	// Each draft is audited with the profile that wrote it.
	drafts, _ := svc.Audit.Query(audit.Filter{Action: ledger.DraftAction})
	if len(drafts) != 2 || drafts[0].Params["profile"] != "p" || drafts[0].Target != a.ID {
		t.Fatalf("draft audit entries: %+v", drafts)
	}
	// Persisted in cache.db: another process (the TUI, `ade review list`) sees them.
	stored, err := review.NewStore(e.st).List(mrRef)
	if err != nil || len(stored) != 2 || stored[0].State != review.StatePending {
		t.Fatalf("stored drafts: %+v %v", stored, err)
	}
}

func TestRepeatedReviewPassDoesNotDuplicateDrafts(t *testing.T) {
	sess, e := newSession(t)
	svc := e.svc
	cs := []ReviewComment{{Path: "svc/limit.go", Line: 11, Side: "new", Severity: "suggestion", Body: "Tighten this."}}
	for range 2 {
		if _, _, err := svc.AddReviewDrafts(sess, cs, "p"); err != nil {
			t.Fatal(err)
		}
	}
	if len(sess.Drafts) != 1 {
		t.Fatalf("drafts = %d, want 1", len(sess.Drafts))
	}
}

func TestNitsAreBatchedIntoOneComment(t *testing.T) {
	cs := []ReviewComment{
		{Path: "a.go", Line: 1, Severity: "nit", Body: "rename x"},
		{Path: "a.go", Line: 4, Severity: "blocking", Body: "leak"},
		{Path: "b.go", Line: 9, Severity: "nit", Body: "trailing space"},
	}
	got := batchNits(cs)
	if len(got) != 2 {
		t.Fatalf("got %d comments: %+v", len(got), got)
	}
	var nit ReviewComment
	for _, c := range got {
		if c.Severity == "nit" {
			nit = c
		}
	}
	if nit.Path != "a.go" || nit.Line != 1 || !strings.Contains(nit.Body, "a.go:1 rename x") || !strings.Contains(nit.Body, "b.go:9 trailing space") {
		t.Fatalf("batched nit: %+v", nit)
	}
	if one := []ReviewComment{cs[0]}; len(batchNits(one)) != 1 {
		t.Fatal("a single nit stays as it is")
	}
}

func TestParseReviewToleratesFencesAndProse(t *testing.T) {
	for name, in := range map[string]string{
		"bare":   `[{"path":"a","line":1,"body":"x"}]`,
		"fenced": "```json\n[{\"path\":\"a\",\"line\":1,\"body\":\"x\"}]\n```",
		"prose":  "Here you go:\n[{\"path\":\"a\",\"line\":1,\"body\":\"x\"}]",
	} {
		cs, err := ParseReview(in)
		if err != nil || len(cs) != 1 {
			t.Errorf("%s: %v %v", name, cs, err)
		}
	}
	if cs, err := ParseReview(`[]`); err != nil || len(cs) != 0 {
		t.Errorf("[]: %v %v", cs, err)
	}
	if _, err := ParseReview("looks fine to me"); err == nil {
		t.Error("prose without JSON must be an error, not an empty review")
	}
	var many strings.Builder
	many.WriteString("[")
	for i := range 30 {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`{"path":"a","line":1,"body":"x"}`)
	}
	many.WriteString("]")
	if cs, _ := ParseReview(many.String()); len(cs) != maxReviewComments {
		t.Errorf("comments are bounded: %d", len(cs))
	}
}

func TestReviewRatesFollowTriageDecisions(t *testing.T) {
	e := newEnv(t)
	sess, _ := review.Resume(review.NewStore(e.st), mrRef, reviewDiff())
	cs := []ReviewComment{
		{Path: "svc/limit.go", Line: 11, Severity: "blocking", Body: "one"},
		{Path: "svc/limit.go", Line: 12, Severity: "question", Body: "two"},
		{Path: "svc/limit.go", Line: 10, Severity: "suggestion", Body: "three"},
	}
	added, _, err := e.svc.AddReviewDrafts(sess, cs, "p")
	if err != nil || len(added) != 3 {
		t.Fatalf("%v %v", added, err)
	}
	AuditDecision(e.log, mrRef, added[0].ID, Accept, "")
	AuditDecision(e.log, mrRef, added[1].ID, Deny, "noisy")
	AuditDecision(e.log, mrRef, added[1].ID, Deny, "")
	rates, err := ledger.ReviewRates(e.log, time.Time{})
	if err != nil || len(rates) != 1 {
		t.Fatalf("%+v %v", rates, err)
	}
	if r := rates[0]; r.Profile != "p" || r.Drafts != 3 || r.Accepted != 1 || r.Denied != 1 || r.Pending != 1 || r.Edited != 0 {
		t.Fatalf("rates = %+v", r)
	}
}

func TestClassifySuggestsOnlyMissingFacetsWithAllowedValues(t *testing.T) {
	in := ClassifyInput{
		Issue:      forge.Issue{Ref: forge.Ref{Project: "grp/tracker", IID: 11}, Title: "Export times out", Labels: []string{"workflow::todo"}},
		Labels:     config.Labels{Type: config.LabelFacet{Pattern: "type::{value}"}, App: config.LabelFacet{Pattern: "app::{value}"}},
		KnownTypes: []string{"story", "bug"}, KnownApps: []string{"api", "web"},
	}
	if m := in.Missing(); len(m) != 2 {
		t.Fatalf("missing = %v", m)
	}
	job, err := ClassifyJob("sandbox", in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(job.Prompt, "MISSING facets: type, app") || !strings.Contains(job.Prompt, "Allowed app values: api, web") {
		t.Fatalf("prompt:\n%s", job.Prompt)
	}
	s, err := ParseSuggestion(`{"type":"Bug","apps":["api","mobile","api"],"reason":"timeout in export endpoint"}`, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.Labels, ",") != "type::bug,app::api" || len(s.Dropped) != 1 || !strings.Contains(s.Dropped[0], "mobile") {
		t.Fatalf("suggestion = %+v", s)
	}
	// A type outside the team's values is dropped, not invented.
	s, _ = ParseSuggestion(`{"type":"epic","apps":[]}`, in)
	if len(s.Labels) != 0 || len(s.Dropped) != 1 {
		t.Fatalf("suggestion = %+v", s)
	}
	// An issue that has both facets needs no call at all.
	in.Issue.Labels = []string{"type::bug", "app::api"}
	if _, err := ClassifyJob("sandbox", in); err != ErrNothingToClassify {
		t.Fatalf("err = %v", err)
	}
	// With only the app missing, a suggested type is ignored.
	in.Issue.Labels = []string{"type::bug"}
	s, _ = ParseSuggestion(`{"type":"story","apps":["web"]}`, in)
	if strings.Join(s.Labels, ",") != "app::web" || len(s.Dropped) != 1 {
		t.Fatalf("suggestion = %+v", s)
	}
}

func TestSummaryPromptIsStableAndTracksTheHeadSHA(t *testing.T) {
	a, b := summarizeJob("h1"), summarizeJob("h1")
	if a.Prompt != b.Prompt {
		t.Fatal("same MR, different prompt: the cache could never hit")
	}
	if summarizeJob("h2").Prompt == a.Prompt {
		t.Fatal("the head SHA must be part of the prompt")
	}
	mr := forge.MR{Ref: mrRef, Title: "t", Unresolved: 3}
	mr2 := mr
	mr2.Unresolved = 0
	mr2.Pipeline = &forge.Pipeline{Status: "failed"}
	d := reviewDiff()
	if SummarizeJob("f", mr, d, nil).Prompt != SummarizeJob("f", mr2, d, nil).Prompt {
		t.Fatal("thread counts and pipeline state move without the code moving; they must not change the prompt")
	}
}

func TestNumberedDiffMarksSidesAndClipsLargeFiles(t *testing.T) {
	out := renderDiff(reviewDiff(), true)
	for _, want := range []string{"### svc/limit.go (modified)", "10  | ctx := c", "11 -| old := 1", "11 +| n := 5", "12 +| m := 6"} {
		if !strings.Contains(out, want) {
			t.Errorf("numbered diff lacks %q:\n%s", want, out)
		}
	}
	big := forge.Diff{Files: []forge.DiffFile{{NewPath: "big.txt", Diff: "@@ -1,1 +1,1 @@\n+" + strings.Repeat("x", 100000)}}}
	if got := renderDiff(big, false); len(got) > maxFileDiff+200 || !strings.Contains(got, "(clipped)") {
		t.Errorf("file diff must be clipped; got %d bytes", len(got))
	}
	var files []forge.DiffFile
	for i := range 20 {
		files = append(files, forge.DiffFile{NewPath: "f" + string(rune('a'+i)), Diff: "@@ -1,1 +1,1 @@\n+" + strings.Repeat("y", 5000)})
	}
	if got := renderDiff(forge.Diff{Files: files}, false); len(got) > maxDiffTotal+maxFileDiff+500 || !strings.Contains(got, "more files omitted") {
		t.Errorf("whole diff must be bounded; got %d bytes", len(got))
	}
}

func TestInputHashSeparatesParts(t *testing.T) {
	if InputHash("ab", "c") == InputHash("a", "bc") {
		t.Fatal("parts must be length-prefixed")
	}
	if InputHash("v1", "role", "model", "s", "p") == InputHash("v1", "role", "model2", "s", "p") {
		t.Fatal("model is part of the key")
	}
}

func TestStoreRegistersTheAITables(t *testing.T) {
	// The ledger table and cache index register themselves with the store.
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, table := range []string{"ai_cache", "ledger_entries", "billing_snapshots"} {
		var n int
		if err := st.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}
