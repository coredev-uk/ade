package ai

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

var closesRe = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+((?:[\w.-]+(?:/[\w.-]+)+)?#\d+)`)

// ClosedIssue finds the issue an MR description closes ("Closes #4",
// "Fixes group/proj#12"); a bare #n is in the MR's own project.
func ClosedIssue(mr forge.MR) (forge.Ref, bool) {
	m := closesRe.FindStringSubmatch(mr.Description)
	if m == nil {
		return forge.Ref{}, false
	}
	target := m[1]
	if strings.HasPrefix(target, "#") {
		target = mr.Ref.Project + target
	}
	ref, err := forge.ParseRef(target)
	if err != nil || ref.Kind != forge.KindIssue {
		return forge.Ref{}, false
	}
	return ref, true
}

// LinkedIssue reads the issue mr closes. A missing or unreadable issue is not
// an error: the prompt simply goes without it.
func LinkedIssue(ctx context.Context, f forge.Forge, mr forge.MR) *forge.Issue {
	ir, ok := ClosedIssue(mr)
	if !ok {
		return nil
	}
	is, err := f.Issue(ctx, ir)
	if err != nil {
		return nil
	}
	return &is
}

// LoadMR reads the MR, its latest diff, and the issue it closes (if any): the
// inputs of the summarize, draft-reply and review jobs.
func LoadMR(ctx context.Context, f review.Backend, ref forge.Ref) (forge.MR, forge.Diff, *forge.Issue, error) {
	mr, err := f.MR(ctx, ref)
	if err != nil {
		return mr, forge.Diff{}, nil, fmt.Errorf("read %s: %w", ref, err)
	}
	diff, err := f.MRDiff(ctx, ref)
	if err != nil {
		return mr, diff, nil, fmt.Errorf("read the diff of %s: %w", ref, err)
	}
	return mr, diff, LinkedIssue(ctx, f, mr), nil
}

// guideFiles are the repository documents a review reads as its guide
// (docs/PLAN.md §6.9), from the MR's target branch: the branch's own rules, not
// the ones the MR itself may be rewriting.
var guideFiles = []string{"AGENTS.md"}

// RepoGuides reads the repository's guide files from the MR's target branch,
// keyed by file name. Files the forge does not have, or cannot read, are left
// out: a review goes ahead without them. nil when there are none or the forge
// cannot read files.
func RepoGuides(ctx context.Context, f any, mr forge.MR) map[string]string {
	fr, ok := f.(interface {
		ReadFile(ctx context.Context, project, ref, path string) ([]byte, error)
	})
	if !ok || mr.TargetBranch == "" {
		return nil
	}
	var out map[string]string
	for _, name := range guideFiles {
		data, err := fr.ReadFile(ctx, mr.Ref.Project, mr.TargetBranch, name)
		if err != nil || strings.TrimSpace(string(data)) == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[name] = string(data)
	}
	return out
}
