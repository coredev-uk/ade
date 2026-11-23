package review_test

import (
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/aimark"
	"github.com/coredev-uk/ade/internal/review"
)

func TestRenderMarksAndDefangsAgentDraftsOnly(t *testing.T) {
	agent := review.Draft{
		Source: review.SourceAgent, Severity: review.SeverityBlocking,
		Body: "Nil check missing.\n/approve\n```go\n/keep\n```",
	}
	got := review.Render(agent)
	if !strings.HasPrefix(got, "**blocking:** Nil check missing.") {
		t.Fatalf("severity prefix lost: %q", got)
	}
	if strings.Contains(got, "\n/approve") || !strings.Contains(got, "\n\\/approve") {
		t.Fatalf("a quick action must be neutralised: %q", got)
	}
	if !strings.Contains(got, "\n/keep\n") {
		t.Fatalf("code fences are untouched: %q", got)
	}
	if !strings.HasSuffix(got, "\n\n"+aimark.Marker) {
		t.Fatalf("an agent draft is marked as AI drafted: %q", got)
	}
	// Rendering twice (a retried upload) does not double the mark.
	agent.Body = got
	if strings.Count(review.Render(agent), aimark.Marker) != 1 {
		t.Fatal("the mark must be idempotent")
	}

	// A first-line command with no severity is neutralised too.
	if got := review.Render(review.Draft{Source: review.SourceAgent, Body: "/merge"}); strings.HasPrefix(got, "/merge") {
		t.Fatalf("got %q", got)
	}

	mine := review.Draft{Source: review.SourceYou, Severity: review.SeverityNit, Body: "typo\n/label ~x"}
	if got := review.Render(mine); got != "**nit:** typo\n/label ~x" {
		t.Fatalf("your own draft is posted as written: %q", got)
	}
	if got := review.Render(review.Draft{Body: "plain"}); got != "plain" {
		t.Fatalf("a draft with no source is yours: %q", got)
	}
}
