package ai

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/review"
	"github.com/coredev-uk/ade/internal/startwork"
)

// Describe drafts a merge request's description from the repo's MR template,
// the MR's commits and its diff (feedback F15). The draft is only shown until
// the user applies it; the apply is a forge write and is audited as
// forge.mr.description (review.AuditedWriter.SetDescription).

const describeVersion = "describe-v1"

//go:embed prompts/describe.md
var describeSystem string

// maxCommits bounds the commit list of one prompt.
const maxCommits = 40

// ErrNoTextWriter is returned when the forge driver cannot read commits or
// rewrite descriptions.
var ErrNoTextWriter = errors.New("this forge cannot read commits or update merge request descriptions yet")

// DescribeInput is what a describe job reads.
type DescribeInput struct {
	MR      forge.MR
	Diff    forge.Diff
	Commits []forge.Commit
	Issue   *forge.Issue
	// TemplateName and Template are the repo's MR template with its quick
	// actions already stripped (the MR exists: they ran when it was created);
	// empty when the project has none.
	TemplateName string
	Template     string
}

// keepRe matches the lines of a description that tie the MR to work items:
// "Closes #4", "Fixes group/proj#12", "Related to #7", "Parent ...", "Part of ...".
var keepRe = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?|related to|relates to|parent|part of)\b[^\n]*#\d+`)

// keptLines are the lines of the current description that must survive a
// rewrite: the links to work items.
func keptLines(desc string) []string {
	var out []string
	for line := range strings.SplitSeq(desc, "\n") {
		if l := strings.TrimSpace(line); l != "" && keepRe.MatchString(l) && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// DescribeJob builds the describe prompt. The MR's current description is not
// part of it (only its links to work items are), so applying a draft does not
// change the cache key of the next ask: the same head, commits and template
// are answered once.
func DescribeJob(forgeName string, in DescribeInput) Job {
	mr, d := in.MR, in.Diff
	head := firstNonEmpty(d.HeadSHA, mr.HeadSHA)
	var b strings.Builder
	fmt.Fprintf(&b, "Merge request: %s\n", mr.Title)
	fmt.Fprintf(&b, "Branches: %s -> %s\n", mr.SourceBranch, mr.TargetBranch)
	fmt.Fprintf(&b, "Head: %s\n", head)
	if kept := keptLines(mr.Description); len(kept) > 0 {
		b.WriteString("\nLines of the current description to keep exactly:\n")
		for _, l := range kept {
			b.WriteString(l + "\n")
		}
	}
	b.WriteString(issueBlock(in.Issue))
	if in.Template != "" {
		fmt.Fprintf(&b, "\nTemplate %q:\n```\n%s\n```\n", in.TemplateName, clip(in.Template, maxDescription))
	} else {
		b.WriteString("\nThe project has no MR template.\n")
	}
	commits := in.Commits
	omitted := 0
	if len(commits) > maxCommits {
		commits, omitted = commits[len(commits)-maxCommits:], len(commits)-maxCommits
	}
	b.WriteString("\nCommits, oldest first:\n")
	if omitted > 0 {
		fmt.Fprintf(&b, "(%d earlier commits omitted)\n", omitted)
	}
	for _, c := range commits {
		fmt.Fprintf(&b, "- %s\n", c.Title)
		if body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c.Message), c.Title)); body != "" {
			fmt.Fprintf(&b, "  %s\n", clip(strings.Join(strings.Fields(body), " "), 400))
		}
	}
	b.WriteString("\nChanges:\n" + renderDiff(d, false))
	return Job{
		Feature: "describe", Role: RoleDraft, PromptVersion: describeVersion,
		System: describeSystem, Prompt: b.String(),
		Forge: forgeName, Ref: mr.Ref, Name: "describe-mr" + fmt.Sprint(mr.Ref.IID),
		MaxTokens: 900,
		Params:    map[string]any{"head": head},
	}
}

// PrepareDescription turns the model's answer into the text that is written
// to the MR: a wrapping code fence is removed, quick-action lines (they would
// run again on update) are stripped, and the links to work items of the
// current description are put back when the model dropped them.
func PrepareDescription(draft, current string) string {
	text := strings.TrimSpace(draft)
	if strings.HasPrefix(text, "```") && strings.HasSuffix(text, "```") && strings.Count(text, "```") == 2 {
		_, inner, _ := strings.Cut(text, "\n")
		text = strings.TrimSpace(strings.TrimSuffix(inner, "```"))
	}
	text = strings.TrimSpace(startwork.StripQuickActions(text, []string{"*"}))
	var missing []string
	for _, l := range keptLines(current) {
		if !strings.Contains(text, l) {
			missing = append(missing, l)
		}
	}
	if len(missing) > 0 {
		if text != "" {
			text += "\n\n"
		}
		text += strings.Join(missing, "\n")
	}
	return text
}

// GatherDescribe reads the MR, its diff and commits, the issue it closes and
// the project's MR template. template names one to use; empty takes the
// manifest's conventions.mr.template, then "Default", then the only one.
// An MR template that cannot be read only means the prompt goes without one.
func GatherDescribe(ctx context.Context, a *app.App, be review.Backend, ref forge.Ref, template string) (DescribeInput, error) {
	tw, ok := be.(forge.MRTextWriter)
	if !ok {
		return DescribeInput{}, ErrNoTextWriter
	}
	mr, diff, issue, err := LoadMR(ctx, be, ref)
	if err != nil {
		return DescribeInput{}, err
	}
	commits, err := tw.MRCommits(ctx, ref)
	if err != nil {
		return DescribeInput{}, fmt.Errorf("read the commits of %s: %w", ref, err)
	}
	in := DescribeInput{MR: mr, Diff: diff, Commits: commits, Issue: issue}
	if template == "" && a != nil {
		if m, err := project.Serving(ctx, a, project.FetchManifest, ref.Project); err == nil {
			template = m.Conventions.MR.Template
		}
	}
	if sf, ok := be.(startwork.Forge); ok {
		in.TemplateName, in.Template, err = loadMRTemplate(ctx, sf, ref.Project, mr.TargetBranch, template)
		if err != nil {
			return DescribeInput{}, err
		}
	}
	return in, nil
}

// loadMRTemplate reads the project's MR template from branch (the MR's target,
// where the template of the work lives, falling back to the default branch).
// An explicit want that does not exist is an error naming what exists; with no
// want, "Default" or the only template is taken and several are left unused.
func loadMRTemplate(ctx context.Context, f startwork.Forge, proj, branch, want string) (name, body string, err error) {
	ref := branch
	names, pathOf, lerr := startwork.TemplateNames(ctx, f, proj, ref)
	if lerr != nil || len(names) == 0 {
		if def, derr := f.DefaultBranch(ctx, proj); derr == nil && def != ref {
			ref = def
			names, pathOf, lerr = startwork.TemplateNames(ctx, f, proj, ref)
		}
	}
	if lerr != nil || len(names) == 0 {
		return "", "", nil
	}
	slices.Sort(names)
	switch {
	case want != "":
		if !slices.Contains(names, want) {
			return "", "", fmt.Errorf("MR template %q not found in %s (have: %s)", want, proj, strings.Join(names, ", "))
		}
		name = want
	case slices.Contains(names, "Default"):
		name = "Default"
	case len(names) == 1:
		name = names[0]
	default:
		return "", "", nil
	}
	raw, err := f.ReadFile(ctx, proj, ref, pathOf(name))
	if errors.Is(err, forge.ErrNotFound) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("template %s: %w", name, err)
	}
	return name, strings.TrimSpace(startwork.StripQuickActions(string(raw), []string{"*"})), nil
}
