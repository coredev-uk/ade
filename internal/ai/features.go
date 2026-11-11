package ai

import (
	_ "embed"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// Prompt versions key the result cache: change a prompt, bump its version.
const (
	summarizeVersion = "summarize-v1"
	classifyVersion  = "classify-v1"
	replyVersion     = "draft-reply-v1"
	reviewVersion    = "review-v1"
)

var (
	//go:embed prompts/summarize.md
	summarizeSystem string
	//go:embed prompts/classify.md
	classifySystem string
	//go:embed prompts/reply.md
	replySystem string
	//go:embed prompts/review.md
	reviewSystem string
)

// Size bounds keep prompts (and so spend) small: thread diffs are clipped.
const (
	maxDescription = 4000
	maxFileDiff    = 6000
	maxDiffTotal   = 40000
	maxThreadNotes = 8
	maxNoteChars   = 1200
)

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	if i := strings.LastIndexByte(cut, '\n'); i > n/2 {
		cut = cut[:i]
	}
	return cut + "\n… (clipped)"
}

// mrHeader is the part of an MR prompt that identifies the change. It holds
// only what describes the change itself (never counts or timestamps that move
// without the code moving), so an unchanged MR hashes the same.
func mrHeader(mr forge.MR, head string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Merge request: %s\n", mr.Title)
	fmt.Fprintf(&b, "Branches: %s -> %s\n", mr.SourceBranch, mr.TargetBranch)
	fmt.Fprintf(&b, "Head: %s\n", head)
	if d := clip(mr.Description, maxDescription); d != "" {
		fmt.Fprintf(&b, "\nDescription:\n%s\n", d)
	}
	return b.String()
}

func issueBlock(is *forge.Issue) string {
	if is == nil {
		return ""
	}
	return fmt.Sprintf("\nLinked issue %s: %s\n%s\n", is.Ref, is.Title, clip(is.Description, maxDescription))
}

// renderDiff writes the MR's files as unified hunks, each file clipped and the
// whole bounded. With numbered, every line carries its line number (new-file
// numbers for added and unchanged lines, old-file numbers for removed ones),
// which is what lets a reviewer cite lines.
func renderDiff(d forge.Diff, numbered bool) string {
	var b strings.Builder
	omitted := 0
	for _, f := range d.Files {
		if b.Len() >= maxDiffTotal {
			omitted++
			continue
		}
		status := "modified"
		switch {
		case f.NewFile:
			status = "new file"
		case f.Deleted:
			status = "deleted"
		case f.Renamed || f.OldPath != f.NewPath:
			status = "renamed from " + f.OldPath
		}
		fmt.Fprintf(&b, "\n### %s (%s)\n", f.NewPath, status)
		if f.Diff == "" {
			b.WriteString("(diff withheld: collapsed or too large)\n")
			continue
		}
		body := f.Diff
		if numbered {
			var nb strings.Builder
			for _, l := range review.Lines(f) {
				n := l.New
				if l.Kind == '-' {
					n = l.Old
				}
				fmt.Fprintf(&nb, "%d %c| %s\n", n, l.Kind, l.Text)
			}
			body = nb.String()
		}
		b.WriteString(clip(body, maxFileDiff))
		b.WriteByte('\n')
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "\n(%d more files omitted)\n", omitted)
	}
	return b.String()
}

// SummarizeJob summarises an MR. The head SHA is part of the prompt, so the
// summary is cached until the MR's code changes.
func SummarizeJob(forgeName string, mr forge.MR, d forge.Diff, issue *forge.Issue) Job {
	head := firstNonEmpty(d.HeadSHA, mr.HeadSHA)
	return Job{
		Feature: "summarize", Role: RoleExplain, PromptVersion: summarizeVersion,
		System: summarizeSystem,
		Prompt: mrHeader(mr, head) + issueBlock(issue) + "\nChanges:\n" + renderDiff(d, false),
		Forge:  forgeName, Ref: mr.Ref, Name: "summary-mr" + fmt.Sprint(mr.Ref.IID),
		MaxTokens: 500,
		Params:    map[string]any{"head": head},
	}
}

// ReplyJob drafts a reply to one discussion. The thread's notes and, for a
// diff thread, that file's clipped diff are the input.
func ReplyJob(forgeName string, mr forge.MR, disc forge.Discussion, d forge.Diff) Job {
	var b strings.Builder
	b.WriteString(mrHeader(mr, firstNonEmpty(d.HeadSHA, mr.HeadSHA)))
	if p := disc.Position; p != nil {
		fmt.Fprintf(&b, "\nThread is on %s", p.Path)
		if p.NewLine > 0 {
			fmt.Fprintf(&b, " line %d (new file)", p.NewLine)
		} else if p.OldLine > 0 {
			fmt.Fprintf(&b, " line %d (old file)", p.OldLine)
		}
		b.WriteString("\n")
		if f, ok := review.File(d, p.Path); ok && f.Diff != "" {
			fmt.Fprintf(&b, "That file's diff:\n%s\n", clip(f.Diff, maxFileDiff))
		}
	}
	b.WriteString("\nThe thread, oldest first:\n")
	notes := disc.Notes
	if len(notes) > maxThreadNotes {
		notes = notes[len(notes)-maxThreadNotes:]
	}
	for _, n := range notes {
		if n.System {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", n.Author.Username, clip(strings.Join(strings.Fields(n.Body), " "), maxNoteChars))
	}
	b.WriteString("\nWrite the reply to the last comment, as the merge request's author.\n")
	return Job{
		Feature: "draft-reply", Role: RoleDraft, PromptVersion: replyVersion,
		System: replySystem, Prompt: b.String(),
		Forge: forgeName, Ref: mr.Ref, Name: "reply-mr" + fmt.Sprint(mr.Ref.IID),
		MaxTokens: 300,
		Params:    map[string]any{"discussion": disc.ID},
	}
}

// ReviewJob asks the review role for structured comments on the MR's diff.
// Existing threads and the linked issue are included so the agent does not
// repeat reviewers or miss the acceptance list; guides are extra context
// documents (AGENTS.md, contributing notes), already read by the caller.
func ReviewJob(forgeName string, mr forge.MR, d forge.Diff, issue *forge.Issue, threads []forge.Discussion, guides map[string]string) Job {
	var b strings.Builder
	b.WriteString(mrHeader(mr, firstNonEmpty(d.HeadSHA, mr.HeadSHA)))
	b.WriteString(issueBlock(issue))
	for _, name := range sortedKeys(guides) {
		fmt.Fprintf(&b, "\nGuide %s:\n%s\n", name, clip(guides[name], maxDescription))
	}
	if open := openThreads(threads); open != "" {
		b.WriteString("\nExisting review threads (do not repeat these):\n" + open)
	}
	b.WriteString("\nDiff:\n" + renderDiff(d, true))
	return Job{
		Feature: "review", Role: RoleReview, PromptVersion: reviewVersion,
		System: reviewSystem, Prompt: b.String(),
		Forge: forgeName, Ref: mr.Ref, Name: "review-mr" + fmt.Sprint(mr.Ref.IID),
		MaxTokens: 2500,
		Params:    map[string]any{"head": firstNonEmpty(d.HeadSHA, mr.HeadSHA)},
	}
}

func openThreads(ds []forge.Discussion) string {
	var b strings.Builder
	for _, d := range ds {
		if len(d.Notes) == 0 || d.Notes[0].System {
			continue
		}
		where := "overview"
		if d.Position != nil {
			where = d.Position.Path
			if d.Position.NewLine > 0 {
				where += fmt.Sprintf(":%d", d.Position.NewLine)
			}
		}
		state := "open"
		if d.Resolved {
			state = "resolved"
		}
		fmt.Fprintf(&b, "- [%s] %s: %s\n", state, where, clip(strings.Join(strings.Fields(d.Notes[0].Body), " "), 300))
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }
