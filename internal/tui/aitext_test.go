package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/records"
)

const (
	stubDigest = "- reviewer-rae asks whether 50 requests a minute is too high.\nNeeds you: answer rae"
	stubStand  = "Probably a bug fix for webhook timeouts. Likely touches the delivery worker."
)

// aiTextStub points the model at a stub omp that answers the digest and the
// stand-in prompts, keeping what it was sent and how often it was asked.
func (e *mrEnv) aiTextStub(mode string) (prompt func(feature string) string, asked func(feature string) int) {
	e.t.Helper()
	dir := e.t.TempDir()
	for name, text := range map[string]string{"digest": stubDigest, "standin": stubStand} {
		if err := os.WriteFile(filepath.Join(dir, name+".jsonl"), []byte(turnEnd(text)), 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
	script := `#!/bin/sh
in=$(cat)
case "$*" in
  *"brief a developer on the comments"*) f=digest ;;
  *"stand-in description for an issue"*) f=standin ;;
  *) f=unknown ;;
esac
printf '%s' "$in" > "` + dir + `/prompt-$f.txt"
echo $f >> "` + dir + `/calls.log"
cat "` + dir + `/$f.jsonl"
`
	path := filepath.Join(dir, "omp")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		e.t.Fatal(err)
	}
	a := e.m.app
	a.RecordsDir = e.t.TempDir()
	a.Config.AI.Mode, a.Config.AI.Profile = mode, "sub"
	a.Config.AI.Profiles["sub"] = config.AIProfile{
		Driver: "omp", Command: path,
		Models: map[string]string{"explain": "claude-haiku-4-5"},
		Prices: map[string]config.Price{"claude-haiku-4-5": {Input: 1, Output: 5}},
	}
	return func(f string) string {
			b, err := os.ReadFile(filepath.Join(dir, "prompt-"+f+".txt"))
			if err != nil {
				e.t.Fatalf("no %s prompt was sent: %v", f, err)
			}
			return string(b)
		}, func(f string) int {
			b, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
			return strings.Count(string(b), f)
		}
}

// palette runs a palette entry by name, as typing it and pressing enter does.
func (e *mrEnv) palette(name string) {
	e.t.Helper()
	for _, en := range e.m.paletteEntries() {
		if en.name == name {
			next, cmd := en.run(e.m)
			e.m = next
			e.settle(cmd)
			return
		}
	}
	e.t.Fatalf("no palette entry %q", name)
}

func (e *mrEnv) selectItem(ref string) {
	e.t.Helper()
	for i, r := range e.m.homeRows {
		if r.ref() == ref {
			e.m.homeSel = i
			return
		}
	}
	e.t.Fatalf("no Home row for %s", ref)
}

func TestDigestDialogSummarisesTheItemsNewCommentsOnceAndMarksThemSeen(t *testing.T) {
	e := newMREnv(t, fake.Free)
	prompt, asked := e.aiTextStub("auto")
	e.selectItem("ade-demo-env/tracker#1")

	e.palette("AI digest of new comments")
	e.want("AI digest", "ade-demo-env/tracker#1", "ade-demo-env/api!1", "new comments", "first look", "reviewer-rae asks whether 50 requests", "AI · claude-haiku-4-5 · $0.0012", "no new comments")
	if p := prompt("digest"); !strings.Contains(p, "Is a burst of 50 too high") || strings.Contains(p, "Keeping 50 for now") || strings.Contains(p, "assigned to") {
		t.Fatalf("the digest prompt has the comments of others only:\n%s", p)
	}
	if asked("digest") != 1 {
		t.Fatalf("the item has one MR with comments: %d calls", asked("digest"))
	}
	seen, err := records.New(e.m.app.RecordsDir).CommentsSeen("f", mrRef("api", 1))
	if err != nil || seen.IsZero() {
		t.Fatalf("the marker was not moved: %v %v", seen, err)
	}
	e.press("esc")
	if e.m.ait != nil {
		t.Fatal("esc closes the dialog")
	}

	// Opened again with nothing new: no call, and the dialog says so.
	e.palette("AI digest of new comments")
	e.want("no new comments since")
	e.wantNot("reviewer-rae asks")
	if asked("digest") != 1 {
		t.Fatalf("nothing new must not call the model: %d", asked("digest"))
	}
	e.press("esc")

	// A new comment arrives: only it is summarised.
	e.srv.Advance(time.Hour)
	if _, err := e.srv.AddNote(mrRef("api", 1), "", "reviewer-rae", "NEWEST-COMMENT: and the 429 body?"); err != nil {
		t.Fatal(err)
	}
	e.palette("AI digest of new comments")
	if p := prompt("digest"); !strings.Contains(p, "NEWEST-COMMENT") || strings.Contains(p, "Is a burst of 50 too high") {
		t.Fatalf("only the comment after the marker is summarised:\n%s", p)
	}
	if asked("digest") != 2 {
		t.Fatalf("calls = %d", asked("digest"))
	}
}

func TestDigestDialogListsCommentsWithAIOffAndNeverCallsTheModel(t *testing.T) {
	e := newMREnv(t, fake.Free)
	_, asked := e.aiTextStub("off")
	e.selectItem("ade-demo-env/tracker#1")
	e.palette("AI digest of new comments")
	e.want("Is a burst of 50 too high", "listed as they are: no model was asked")
	if asked("digest") != 0 {
		t.Fatal("AI is off: no call")
	}
}

func TestStandInDialogIsMarkedLocalCachedAndWritesNothingToTheForge(t *testing.T) {
	e := newMREnv(t, fake.Free)
	prompt, asked := e.aiTextStub("auto")
	empty, err := e.api.CreateIssue(context.Background(), fake.GroupPath+"/tracker", forge.IssueDraft{Title: "Handle webhook timeouts gracefully"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range e.m.homeRows {
		if it := e.m.homeRows[i].item; it != nil && it.Ref.IID == 1 {
			it.Ref, it.Issue = empty.Ref, empty
			e.m.homeSel = i
		}
	}
	e.palette("AI stand-in description for an empty issue")
	e.want("AI stand-in description", "Handle webhook timeouts gracefully", "Probably a bug fix for webhook timeouts", "local only", "never posted to the forge", "$0.0012")
	if p := prompt("standin"); !strings.Contains(p, "Handle webhook timeouts gracefully") {
		t.Fatalf("the prompt is built from the title:\n%s", p)
	}
	after, err := e.api.Issue(context.Background(), empty.Ref)
	if err != nil || after.Description != "" || !after.UpdatedAt.Equal(empty.UpdatedAt) {
		t.Fatalf("the issue changed on the forge: %+v %v", after, err)
	}
	e.press("esc")
	// Asked again: a free cache hit.
	e.palette("AI stand-in description for an empty issue")
	e.want("cached, $0")
	if asked("standin") != 1 {
		t.Fatalf("the second ask must be cached: %d calls", asked("standin"))
	}
	e.press("esc")

	// With AI off the dialog says so and sends nothing.
	e.m.app.Config.AI.Mode = "off"
	e.palette("AI stand-in description for an empty issue")
	e.want("AI is off", "Nothing was sent")
	if asked("standin") != 1 {
		t.Fatal("AI off: no call")
	}
}

func TestStandInDialogLeavesIssuesWithADescriptionAlone(t *testing.T) {
	e := newMREnv(t, fake.Free)
	_, asked := e.aiTextStub("auto")
	for i := range e.m.homeRows {
		if it := e.m.homeRows[i].item; it != nil {
			it.Issue.Description = "Described already."
			e.m.homeSel = i
		}
	}
	e.palette("AI stand-in description for an empty issue")
	e.want("no stand-in needed")
	if asked("standin") != 0 {
		t.Fatal("an issue with a description needs no call")
	}
}

func TestAITextDialogFitsNarrowAndAsciiTerminals(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.aiTextStub("auto")
	e.m.g, e.m.w, e.m.h = asciiGlyphs, 60, 30
	e.selectItem("ade-demo-env/tracker#1")
	e.palette("AI digest of new comments")
	out := e.screen()
	if !strings.Contains(out, "AI digest") || !strings.Contains(out, "reviewer-rae") {
		t.Fatalf("the digest is not on screen:\n%s", out)
	}
	for i, ln := range strings.Split(out, "\n") {
		for _, r := range ln {
			if r > 126 {
				t.Fatalf("line %d has %q in ascii mode: %q", i+1, r, ln)
			}
		}
		if w := len([]rune(ln)); w > 60 {
			t.Fatalf("line %d is %d wide at 60 columns: %q", i+1, w, ln)
		}
	}
}
