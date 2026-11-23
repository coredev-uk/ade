package ai

import (
	_ "embed"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/aimark"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// Prompt versions key the result cache: change a prompt, bump its version.
const (
	summarizeVersion = "summarize-v3"
	classifyVersion  = "classify-v4"
	replyVersion     = "draft-reply-v3"
	reviewVersion    = "review-v2"
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
// without the code moving), so an unchanged MR hashes the same. The title,
// branch names and description are the forge's text: an untrusted block.
func mrHeader(mr forge.MR, head string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Merge request: %s\n", mr.Title)
	fmt.Fprintf(&b, "Branches: %s -> %s\n", mr.SourceBranch, mr.TargetBranch)
	fmt.Fprintf(&b, "Head: %s\n", head)
	if d := clip(mr.Description, maxDescription); d != "" {
		fmt.Fprintf(&b, "\nDescription:\n%s\n", d)
	}
	return untrusted("merge request", b.String())
}

func issueBlock(is *forge.Issue) string {
	if is == nil {
		return ""
	}
	return "\n" + untrusted("linked issue", fmt.Sprintf("Linked issue %s: %s\n%s", is.Ref, is.Title, clip(is.Description, maxDescription)))
}

// renderDiff writes the MR's files as unified hunks, each file clipped and the
// whole bounded. With numbered, every line carries its line number (new-file
// numbers for added and unchanged lines, old-file numbers for removed ones),
// which is what lets a reviewer cite lines. The result is plain text: callers
// put it in an untrusted block.
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
		System: guarded(summarizeSystem),
		Prompt: mrHeader(mr, head) + issueBlock(issue) + "\nChanges:\n" + untrusted("diff", renderDiff(d, false)),
		Forge:  forgeName, Ref: mr.Ref, Name: "summary-mr" + fmt.Sprint(mr.Ref.IID),
		MaxTokens: 500,
		Params:    map[string]any{"head": head},
	}
}

// ReplyRole is whose voice a draft reply is written in. The thread's reader
// may be the merge request's author, a reviewer, or unknown (the dashboard did
// not say who is signed in): a reviewer must not get "I'll fix it" drafted for
// them.
type ReplyRole int

const (
	ReplyUnknown  ReplyRole = iota // identity not known: no promises either way
	ReplyAuthor                    // the reader opened the merge request
	ReplyReviewer                  // the reader is a reviewer or another participant
)

// ReplyRoleFor decides the role of me (a username; "" when unknown) on mr.
func ReplyRoleFor(mr forge.MR, me string) ReplyRole {
	switch {
	case me == "":
		return ReplyUnknown
	case strings.EqualFold(me, mr.Author.Username):
		return ReplyAuthor
	}
	return ReplyReviewer
}

func (r ReplyRole) instruction() string {
	switch r {
	case ReplyAuthor:
		return "Write the reply to the last comment, as the merge request's author."
	case ReplyReviewer:
		return "Write the reply to the last comment, as a reviewer of this merge request: you are not its author, so never say you will change the code; ask, answer or agree as a reviewer."
	}
	return "Write the reply to the last comment. It is not known whether you are the merge request's author or a reviewer, so do not promise changes to the code and do not speak for the author."
}

// ReplyJob drafts a reply to one discussion. The thread's notes and, for a
// diff thread, that file's clipped diff are the input; role says whose voice
// the reply is in.
func ReplyJob(forgeName string, mr forge.MR, disc forge.Discussion, d forge.Diff, role ReplyRole) Job {
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
	notes := disc.Notes
	if len(notes) > maxThreadNotes {
		notes = notes[len(notes)-maxThreadNotes:]
	}
	var thread strings.Builder
	thread.WriteString("The thread, oldest first:\n")
	for _, n := range notes {
		if n.System {
			continue
		}
		fmt.Fprintf(&thread, "- %s: %s\n", n.Author.Username, clip(strings.Join(strings.Fields(n.Body), " "), maxNoteChars))
	}
	b.WriteString("\n" + untrusted("review thread", thread.String()))
	b.WriteString("\n" + role.instruction() + "\n")
	return Job{
		Feature: "draft-reply", Role: RoleDraft, PromptVersion: replyVersion,
		System: guarded(replySystem), Prompt: b.String(),
		Forge: forgeName, Ref: mr.Ref, Name: "reply-mr" + fmt.Sprint(mr.Ref.IID),
		MaxTokens: 300,
		Params:    map[string]any{"discussion": disc.ID},
	}
}

// ReplyDraft is a draft reply as it may be edited and posted: every line that
// would run as a forge quick action is neutralised (the thread steered the
// model). The text is marked as AI drafted when it is posted (aimark.Mark).
func ReplyDraft(text string) string {
	return strings.TrimSpace(aimark.NoQuickActions(strings.TrimSpace(text)))
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
		fmt.Fprintf(&b, "\nGuide %s:\n%s\n", name, untrusted("guide "+name, clip(guides[name], maxDescription)))
	}
	if open := openThreads(threads); open != "" {
		b.WriteString("\nExisting review threads (do not repeat these):\n" + untrusted("existing review threads", open))
	}
	b.WriteString("\nDiff:\n" + untrusted("diff", renderDiff(d, true)))
	return Job{
		Feature: "review", Role: RoleReview, PromptVersion: reviewVersion,
		System: guarded(reviewSystem), Prompt: b.String(),
		Forge: forgeName, Ref: mr.Ref, Name: "review-mr" + fmt.Sprint(mr.Ref.IID),
		MaxTokens: 2500,
		Params:    map[string]any{"head": firstNonEmpty(d.HeadSHA, mr.HeadSHA)},
		Validate:  func(text string) error { _, err := ParseReview(text); return err },
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
