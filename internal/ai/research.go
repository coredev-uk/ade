package ai

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

const researchVersion = "research-v1"

//go:embed prompts/research.md
var researchSystem string

// maxResearchLinks bounds the related items one research prompt lists.
const maxResearchLinks = 12

// ResearchInput is what a research job reads about one issue. It holds only
// what describes the work, never counts or timestamps that move without the
// work moving, so an unchanged issue hashes the same and is not bought twice.
type ResearchInput struct {
	Issue    forge.Issue
	Parent   *forge.Issue // the tracker issue the description names as its parent
	Comments []forge.Discussion
	Links    []ResearchLink
	MRs      []forge.MR
}

// ResearchLink is a linked issue with its title, for the prompt.
type ResearchLink struct {
	Type  string
	Ref   forge.Ref
	Title string
	State string
}

// GatherResearch reads the issue, its discussion, parent, links and MRs from
// the forge. Anything that cannot be read (a link to a project the token has
// no access to) is left out; only the issue itself is required.
func GatherResearch(ctx context.Context, f forge.Forge, ref forge.Ref) (ResearchInput, error) {
	is, err := f.Issue(ctx, ref)
	if err != nil {
		return ResearchInput{}, fmt.Errorf("read %s: %w", ref, err)
	}
	in := ResearchInput{Issue: is}
	if parents := domain.ParentRefs(is); len(parents) > 0 {
		if p, err := f.Issue(ctx, parents[0]); err == nil {
			in.Parent = &p
		}
	}
	if is.Comments == nil || *is.Comments > 0 {
		if ds, err := f.Discussions(ctx, ref); err == nil {
			in.Comments = ds
		}
	}
	if links, err := f.Links(ctx, ref); err == nil {
		for _, l := range links {
			if len(in.Links) == maxResearchLinks {
				break
			}
			rl := ResearchLink{Type: l.Type, Ref: l.Ref}
			if li, err := f.Issue(ctx, l.Ref); err == nil {
				rl.Title, rl.State = li.Title, li.State
			}
			in.Links = append(in.Links, rl)
		}
	}
	if mrs, err := f.MRsForIssue(ctx, ref); err == nil {
		in.MRs = mrs
	}
	return in, nil
}

// ResearchJob builds the research prompt: the issue, its parent, the human
// discussion, links and the MRs already open for it. Add the user's notes with
// WithNotes. The role is plan: research is analysis, and a profile without a
// plan model falls back to explain.
func ResearchJob(forgeName string, in ResearchInput) Job {
	var b strings.Builder
	is := in.Issue
	fmt.Fprintf(&b, "Issue %s: %s\n", is.Ref, is.Title)
	if len(is.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(is.Labels, ", "))
	}
	if is.State != "" {
		fmt.Fprintf(&b, "State: %s\n", is.State)
	}
	if d := clip(is.Description, maxDescription); d != "" {
		fmt.Fprintf(&b, "\nDescription:\n%s\n", d)
	}
	if p := in.Parent; p != nil {
		fmt.Fprintf(&b, "\nParent issue %s: %s\n%s\n", p.Ref, p.Title, clip(p.Description, maxDescription/2))
	}
	if disc := humanComments(in.Comments); disc != "" {
		b.WriteString("\nDiscussion, oldest first:\n" + disc)
	}
	if len(in.Links) > 0 {
		b.WriteString("\nLinked issues:\n")
		for _, l := range in.Links {
			fmt.Fprintf(&b, "- %s %s: %s", firstNonEmpty(l.Type, "related"), l.Ref, firstNonEmpty(l.Title, "(unreadable)"))
			if l.State != "" {
				fmt.Fprintf(&b, " [%s]", l.State)
			}
			b.WriteByte('\n')
		}
	}
	if len(in.MRs) > 0 {
		b.WriteString("\nMerge requests already open for it:\n")
		for _, mr := range in.MRs {
			fmt.Fprintf(&b, "- %s %s (%s -> %s) [%s]\n", mr.Ref, mr.Title, mr.SourceBranch, mr.TargetBranch, mr.State)
		}
	}
	return Job{
		Feature: "research", Role: RolePlan, PromptVersion: researchVersion,
		System: researchSystem, Prompt: b.String(),
		Forge: forgeName, Ref: is.Ref, Name: "research-issue" + fmt.Sprint(is.Ref.IID),
		MaxTokens: 1500,
	}
}

// ResearchNoteRefs are the item records whose notes belong to researching an
// issue: its own, then the tracker issues it names as parent.
func ResearchNoteRefs(is forge.Issue) []forge.Ref {
	return append([]forge.Ref{is.Ref}, domain.ParentRefs(is)...)
}

// humanComments renders the people's comments of discussions, oldest first,
// bounded like the thread prompts.
func humanComments(ds []forge.Discussion) string {
	var b strings.Builder
	n := 0
	for _, d := range ds {
		for _, c := range d.Notes {
			if c.System {
				continue
			}
			if n == maxThreadNotes*2 {
				return b.String()
			}
			n++
			fmt.Fprintf(&b, "- %s: %s\n", c.Author.Username, clip(strings.Join(strings.Fields(c.Body), " "), maxNoteChars))
		}
	}
	return b.String()
}
