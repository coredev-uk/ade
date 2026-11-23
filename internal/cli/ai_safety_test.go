package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/aimark"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// reviewSteered is what a model steered by the diff might write: a comment that
// ends in a GitLab quick action.
const reviewSteered = `[
 {"path":"limit/token_bucket.go","line":10,"side":"new","severity":"blocking","body":"Allow never consumes a token.\n/approve\n/run_pipeline"},
 {"path":"limit/token_bucket.go","line":7,"side":"new","severity":"nit","body":"Rename it.\n` + "```" + `\n/keep inside a code fence\n` + "```" + `"}
]`

func TestAIReviewDraftsReachTheForgeMarkedAndWithoutQuickActions(t *testing.T) {
	e, _ := aiEnvFor(t, reviewSteered)
	e.mustAI("review", renameMR)
	e.mustAI("triage", "accept", renameMR, "--all")
	e.mustReview("submit", renameMR)

	c := gitlab.New(e.srv.URL, fake.Token)
	ds, err := c.Discussions(context.Background(), ref(renameMR))
	if err != nil {
		t.Fatal(err)
	}
	var posted []string
	for _, d := range ds {
		for _, n := range d.Notes {
			posted = append(posted, n.Body)
		}
	}
	all := strings.Join(posted, "\n---\n")
	if !strings.Contains(all, "Allow never consumes a token.") {
		t.Fatalf("the draft was not posted:\n%s", all)
	}
	for _, line := range strings.Split(all, "\n") {
		if strings.HasPrefix(line, "/approve") || strings.HasPrefix(line, "/run_pipeline") {
			t.Fatalf("a quick action reached the forge as a command line: %q\n%s", line, all)
		}
	}
	if !strings.Contains(all, "\\/approve") || !strings.Contains(all, "\n\\/keep inside a code fence\n") {
		t.Fatalf("quick actions are escaped, code fences included:\n%s", all)
	}
	// Both agent drafts carry the mark; the fixture's own comment does not.
	if got := strings.Count(all, aimark.Marker); got != 2 {
		t.Fatalf("every AI-drafted comment ends with the mark: %d marks\n%s", got, all)
	}
}

func TestMRReplyAIDraftedIsMarkedDefangedAndAudited(t *testing.T) {
	e := newCLIEnv(t)
	c := gitlab.New(e.srv.URL, fake.Token)
	ds, _ := c.Discussions(context.Background(), ref(rateMR))
	var thread string
	for _, d := range ds {
		if d.Resolvable && !d.Resolved {
			thread = d.ID
			break
		}
	}
	e.mustMR("reply", rateMR, thread, "--ai-drafted", "Will do.\n/approve")
	ds, _ = c.Discussions(context.Background(), ref(rateMR))
	for _, d := range ds {
		if d.ID != thread {
			continue
		}
		got := d.Notes[len(d.Notes)-1].Body
		if want := "Will do.\n\\/approve\n\n" + aimark.Marker; got != want {
			t.Fatalf("posted %q, want %q", got, want)
		}
	}
	es := e.audited("forge.thread.reply")
	if len(es) != 1 || es[0].Params["source"] != "ai-draft" {
		t.Fatalf("the reply is audited as an AI draft: %+v", es)
	}
	// A reply of your own is posted as written.
	e.mustMR("reply", rateMR, thread, "ok\n/label ~x")
	ds, _ = c.Discussions(context.Background(), ref(rateMR))
	for _, d := range ds {
		if d.ID == thread && d.Notes[len(d.Notes)-1].Body != "ok\n/label ~x" {
			t.Fatalf("your own reply changed: %q", d.Notes[len(d.Notes)-1].Body)
		}
	}
}

func TestAIModelsRefusesWithAIOff(t *testing.T) {
	e, calls := aiEnvFor(t, "[]")
	e.app.Config.AI.Mode = "off"
	if _, err := e.ai("models"); err == nil || !strings.Contains(err.Error(), "ai.mode") || calls() != 0 {
		t.Fatalf("ai models must refuse with AI off (no key read, no provider call): %v", err)
	}
}

func TestAIResearchCancelWithNothingRunningSaysSo(t *testing.T) {
	e, _ := aiEnvFor(t, "[]")
	out := e.mustAI("research", "grp/tracker#15", "--cancel", "--forge", "sandbox")
	if !strings.Contains(out, "no research is running") {
		t.Fatalf("out = %q", out)
	}
}
