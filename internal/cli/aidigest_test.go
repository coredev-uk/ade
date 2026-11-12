package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/records"
)

// digestEnv is the fake-GitLab CLI env with a stub `omp` that keeps every
// prompt it is sent: prompt(n) is the n-th (1-based), calls() how many came.
func digestEnv(t *testing.T) (e *cliEnv, prompt func(n int) string, calls func() int) {
	t.Helper()
	e = newCLIEnv(t)
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	stub := filepath.Join(dir, "omp")
	script := "#!/bin/sh\necho x >> " + count + "\nn=$(wc -l < " + count + ")\ncat > " + dir + "/prompt.$n\ncat " + filepath.Join(dir, "answer.jsonl") + "\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "answer.jsonl"), []byte(ompTurn(t, "- reviewer-rae asks about the timeout\nNeeds you: answer rae", 0.003)), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.RecordsDir = t.TempDir()
	e.app.Config.AI.Mode, e.app.Config.AI.Profile = "auto", "sub"
	e.app.Config.AI.Profiles["sub"] = config.AIProfile{
		Driver: "omp", Command: stub,
		Models: map[string]string{"explain": "claude-haiku-4-5"},
		Prices: map[string]config.Price{"claude-haiku-4-5": {Input: 1, Output: 5}},
	}
	return e, func(n int) string {
			b, err := os.ReadFile(filepath.Join(dir, "prompt."+string(rune('0'+n))))
			if err != nil {
				t.Fatalf("prompt %d: %v", n, err)
			}
			return string(b)
		}, func() int {
			b, _ := os.ReadFile(count)
			return strings.Count(string(b), "x")
		}
}

const digestIssue = fake.GroupPath + "/tracker#6"

func (e *cliEnv) forgeNote(ref, author, body string) {
	e.t.Helper()
	e.srv.Advance(time.Hour)
	if _, err := e.srv.AddNote(mustRef(e.t, ref), "", author, body); err != nil {
		e.t.Fatal(err)
	}
}

func mustRef(t *testing.T, s string) forge.Ref {
	t.Helper()
	r, err := forge.ParseRef(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *cliEnv) seenAt(ref string) time.Time {
	e.t.Helper()
	got, err := records.New(e.app.RecordsDir).CommentsSeen("sandbox", mustRef(e.t, ref))
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

func TestAIDigestSummarisesOnlyWhatIsNewAndAdvancesTheMarker(t *testing.T) {
	e, prompt, calls := digestEnv(t)
	e.forgeNote(digestIssue, "reviewer-rae", "FIRST-COMMENT: please confirm the export timeout is 30s.")
	e.forgeNote(digestIssue, "sandbox-dev", "MY-OWN-COMMENT: yes it is.")

	// First look: everything by others is new; my own comment is not sent.
	out := e.mustAI("digest", digestIssue)
	if !strings.Contains(out, "1 new comment since the beginning") || !strings.Contains(out, "reviewer-rae asks about the timeout") || !strings.Contains(out, "marked seen up to") {
		t.Fatalf("first digest:\n%s", out)
	}
	p1 := prompt(1)
	if !strings.Contains(p1, "FIRST-COMMENT") || strings.Contains(p1, "MY-OWN-COMMENT") {
		t.Fatalf("prompt 1 must hold the first comment and not my own:\n%s", p1)
	}
	if e.seenAt(digestIssue).IsZero() {
		t.Fatal("the marker was not saved")
	}

	// Nothing new since: no call, no cost, and the marker stays.
	marker := e.seenAt(digestIssue)
	out = e.mustAI("digest", digestIssue)
	if !strings.Contains(out, "no new comments since") || calls() != 1 || !e.seenAt(digestIssue).Equal(marker) {
		t.Fatalf("second digest (calls=%d):\n%s", calls(), out)
	}

	// A new comment arrives: only it is summarised, the old one is not sent again.
	e.forgeNote(digestIssue, "reviewer-rae", "SECOND-COMMENT: and what about retries?")
	out = e.mustAI("digest", digestIssue)
	if !strings.Contains(out, "1 new comment since") || calls() != 2 {
		t.Fatalf("third digest (calls=%d):\n%s", calls(), out)
	}
	p2 := prompt(2)
	if !strings.Contains(p2, "SECOND-COMMENT") || strings.Contains(p2, "FIRST-COMMENT") {
		t.Fatalf("prompt 2 must hold only the comment after the marker:\n%s", p2)
	}
	if !e.seenAt(digestIssue).After(marker) {
		t.Fatal("the marker did not advance past the new comment")
	}
}

func TestAIDigestPeekDryRunAndFailuresLeaveTheMarkerAlone(t *testing.T) {
	e, prompt, calls := digestEnv(t)
	e.forgeNote(digestIssue, "reviewer-rae", "COMMENT-ONE")

	out := e.mustAI("digest", digestIssue, "--dry-run")
	if !strings.Contains(out, "dry run") || !strings.Contains(out, "marker unchanged (dry run)") || calls() != 0 || !e.seenAt(digestIssue).IsZero() {
		t.Fatalf("dry run (calls=%d):\n%s", calls(), out)
	}
	out = e.mustAI("digest", digestIssue, "--peek")
	if !strings.Contains(out, "marker unchanged (--peek)") || calls() != 1 || !e.seenAt(digestIssue).IsZero() {
		t.Fatalf("peek (calls=%d):\n%s", calls(), out)
	}
	// The same comments are still new, and asking again is a free cache hit.
	out = e.mustAI("digest", digestIssue)
	if !strings.Contains(out, "cached · $0.00") || calls() != 1 || e.seenAt(digestIssue).IsZero() {
		t.Fatalf("after peek (calls=%d):\n%s", calls(), out)
	}
	if !strings.Contains(prompt(1), "COMMENT-ONE") {
		t.Fatal("prompt lost the comment")
	}

	// A refused call (the cap is reached) must not mark anything seen.
	e.forgeNote(digestIssue, "reviewer-rae", "COMMENT-TWO")
	marker := e.seenAt(digestIssue)
	e.app.Config.Budget.DailyUSD = 0.000001
	if _, err := e.ai("digest", digestIssue); err == nil {
		t.Fatal("the cap must stop the digest")
	}
	if !e.seenAt(digestIssue).Equal(marker) || calls() != 1 {
		t.Fatalf("a stopped digest moved the marker or called the model (calls=%d)", calls())
	}
}

func TestAIDigestListsTheCommentsWithAIOff(t *testing.T) {
	e, _, calls := digestEnv(t)
	e.app.Config.AI.Mode = "off"
	e.forgeNote(digestIssue, "reviewer-rae", "Please look at the timeout.")
	out := e.mustAI("digest", digestIssue)
	if !strings.Contains(out, "reviewer-rae") || !strings.Contains(out, "Please look at the timeout.") || !strings.Contains(out, "no model was asked") || calls() != 0 {
		t.Fatalf("with AI off the new comments are listed (calls=%d):\n%s", calls(), out)
	}
	if e.seenAt(digestIssue).IsZero() {
		t.Fatal("a listed digest also moves the marker")
	}
	asJSON(t)
	var res digestOut
	if err := json.Unmarshal([]byte(e.mustAI("digest", digestIssue, "--no-ai", "--since", "7d")), &res); err != nil {
		t.Fatal(err)
	}
	if res.Source != "list" || res.NewComments != 1 || res.Since == nil || res.Until == nil || res.CostUSD != 0 {
		t.Fatalf("json = %+v", res)
	}
}

func TestAIDigestOnAnMRSkipsSystemNotesAndMyOwnReplies(t *testing.T) {
	e, prompt, _ := digestEnv(t)
	e.mustAI("digest", rateMR)
	p := prompt(1)
	// The seeded review: reviewer-rae's comments are news, my reply and the
	// "assigned to" system note are not.
	for _, want := range []string{"Is a burst of 50 too high", "add a test for the 429 path"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	for _, not := range []string{"Keeping 50 for now", "assigned to"} {
		if strings.Contains(p, not) {
			t.Errorf("prompt must not carry %q:\n%s", not, p)
		}
	}
	// A reply in a thread says whose thread it is, without sending the earlier text.
	e.srv.Advance(time.Hour)
	disc, err := gitlab.New(e.app.Config.Forges["sandbox"].URL, fake.Token).Discussions(context.Background(), mustRef(t, rateMR))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.AddNote(mustRef(t, rateMR), disc[1].ID, "reviewer-rae", "REPLY-TEXT: fine, 50 it is."); err != nil {
		t.Fatal(err)
	}
	e.mustAI("digest", rateMR)
	p = prompt(2)
	if !strings.Contains(p, "REPLY-TEXT") || !strings.Contains(p, "reply in reviewer-rae's thread") || strings.Contains(p, "Is a burst of 50 too high") {
		t.Fatalf("a reply is summarised on its own:\n%s", p)
	}
}

func TestAIStandInIsLocalMarkedCachedAndNeverPosted(t *testing.T) {
	e, prompt, calls := digestEnv(t)
	api := gitlab.New(e.app.Config.Forges["sandbox"].URL, fake.Token)
	empty, err := api.CreateIssue(context.Background(), fake.GroupPath+"/api", forge.IssueDraft{Title: "Handle webhook timeouts gracefully", Labels: []string{"type::bug"}})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Description != "" {
		t.Fatalf("fixture has a description: %q", empty.Description)
	}
	ref := empty.Ref.String()

	out := e.mustAI("standin", ref)
	for _, want := range []string{ai.StandInLabel, "never posted to the forge", "reviewer-rae asks about the timeout", "— $0.0030"} {
		if !strings.Contains(out, want) {
			t.Errorf("stand-in output lacks %q:\n%s", want, out)
		}
	}
	if p := prompt(1); !strings.Contains(p, "Handle webhook timeouts gracefully") || !strings.Contains(p, "type::bug") {
		t.Fatalf("the prompt is built from the title and labels:\n%s", p)
	}
	// Nothing reached the forge: the description is still empty, no write was audited.
	after, err := api.Issue(context.Background(), empty.Ref)
	if err != nil || after.Description != "" || !after.UpdatedAt.Equal(empty.UpdatedAt) {
		t.Fatalf("the issue changed on the forge: %+v %v", after, err)
	}
	if ws := e.audited("forge.*"); len(ws) != 0 {
		t.Fatalf("a stand-in must not write to the forge: %+v", ws)
	}

	// The second ask is a cache hit: no call, $0.
	out = e.mustAI("standin", ref)
	if !strings.Contains(out, "cached · $0.00") || calls() != 1 {
		t.Fatalf("second ask (calls=%d):\n%s", calls(), out)
	}
	asJSON(t)
	var res standInOut
	if err := json.Unmarshal([]byte(e.mustAI("standin", ref)), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Needed || !res.LocalOnly || !res.CacheHit || res.Feature != "standin" || res.Text == "" || res.Artifact == "" {
		t.Fatalf("json = %+v", res)
	}
	jsonOut = false

	// An issue with a description needs none: no call at all.
	out = e.mustAI("standin", fake.GroupPath+"/tracker#1")
	if !strings.Contains(out, "no stand-in needed") || calls() != 1 {
		t.Fatalf("issue with a description (calls=%d):\n%s", calls(), out)
	}
	// AI off: refused, nothing sent.
	e.app.Config.AI.Mode = "off"
	if _, err := e.ai("standin", ref); err == nil || !strings.Contains(err.Error(), "ai.mode") || calls() != 1 {
		t.Fatalf("with AI off: %v", err)
	}
}
