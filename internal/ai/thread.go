package ai

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

const threadSummaryVersion = "thread-summary-v3"

//go:embed prompts/thread.md
var threadSummarySystem string

// ThreadSummaryJob summarises one discussion: where it is, the file's diff for
// a diff thread, and the notes oldest first. The answer is cached until the
// thread or the MR's code changes.
func ThreadSummaryJob(forgeName string, mr forge.MR, disc forge.Discussion, d forge.Diff) Job {
	var b strings.Builder
	b.WriteString(mrHeader(mr, firstNonEmpty(d.HeadSHA, mr.HeadSHA)))
	if p := disc.Position; p != nil {
		var pos strings.Builder
		fmt.Fprintf(&pos, "Thread is on %s", p.Path)
		if p.NewLine > 0 {
			fmt.Fprintf(&pos, " line %d (new file)", p.NewLine)
		} else if p.OldLine > 0 {
			fmt.Fprintf(&pos, " line %d (old file)", p.OldLine)
		}
		if f, ok := review.File(d, p.Path); ok && f.Diff != "" {
			fmt.Fprintf(&pos, "\nThat file's diff:\n%s", clip(f.Diff, maxFileDiff))
		}
		b.WriteString("\n" + untrusted("thread location and diff", pos.String()))
	}
	state := "open"
	if disc.Resolved {
		state = "resolved"
	}
	var thread strings.Builder
	fmt.Fprintf(&thread, "The thread (%s), oldest first:\n", state)
	notes := disc.Notes
	if len(notes) > maxThreadNotes {
		notes = notes[len(notes)-maxThreadNotes:]
	}
	for _, n := range notes {
		if n.System {
			continue
		}
		fmt.Fprintf(&thread, "- %s: %s\n", n.Author.Username, clip(strings.Join(strings.Fields(n.Body), " "), maxNoteChars))
	}
	b.WriteString("\n" + untrusted("review thread", thread.String()))
	return Job{
		Feature: "summarize-thread", Role: RoleExplain, PromptVersion: threadSummaryVersion,
		System: guarded(threadSummarySystem), Prompt: b.String(),
		Forge: forgeName, Ref: mr.Ref, Name: "thread-mr" + fmt.Sprint(mr.Ref.IID) + "-" + slug(disc.ID),
		MaxTokens: 700,
		Params:    map[string]any{"discussion": disc.ID},
	}
}

// slug shortens a discussion id for an artifact name.
func slug(id string) string {
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}
