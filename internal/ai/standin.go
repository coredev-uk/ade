package ai

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// standInVersion keys the result cache: change the prompt, bump it.
const standInVersion = "standin-v1"

//go:embed prompts/standin.md
var standInSystem string

// Bounds on the context a stand-in prompt carries.
const (
	maxStandInRelated     = 3
	maxStandInRelatedDesc = 1000
)

// ErrHasDescription is returned for an issue that has a description: a
// stand-in is only for an empty one.
var ErrHasDescription = errors.New("the issue has a description: no stand-in needed")

// StandInInput is an issue with an empty description and what the forge says
// around it.
type StandInInput struct {
	Issue forge.Issue
	// Related are the issues it is linked to (its tracker parent, siblings):
	// context only, in the order the forge lists them.
	Related []forge.Issue
}

// LoadStandInInput reads the issue and, when its description is empty, the
// issues it is linked to. It only reads; the stand-in is never written back.
func LoadStandInInput(ctx context.Context, be forge.Forge, ref forge.Ref) (StandInInput, error) {
	is, err := be.Issue(ctx, ref)
	if err != nil {
		return StandInInput{}, fmt.Errorf("read %s: %w", ref, err)
	}
	in := StandInInput{Issue: is}
	if strings.TrimSpace(is.Description) != "" {
		return in, nil
	}
	links, err := be.Links(ctx, ref)
	if err != nil {
		return in, nil // the title alone still makes a stand-in
	}
	for _, l := range links {
		if l.Ref.Kind != forge.KindIssue || len(in.Related) == maxStandInRelated {
			continue
		}
		if rel, err := be.Issue(ctx, l.Ref); err == nil {
			in.Related = append(in.Related, rel)
		}
	}
	return in, nil
}

// StandInJob asks for a stand-in description from the issue's title, labels
// and the issues it is linked to. ErrHasDescription when the issue has a
// description. The prompt holds only what describes the issue, never counts or
// timestamps that move without it, so an unchanged issue is never asked twice.
func StandInJob(forgeName string, in StandInInput) (Job, error) {
	is := in.Issue
	if strings.TrimSpace(is.Description) != "" {
		return Job{}, ErrHasDescription
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Issue %s: %s\n", is.Ref, is.Title)
	if len(is.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(is.Labels, ", "))
	}
	if is.Epic != "" {
		fmt.Fprintf(&b, "Epic: %s\n", is.Epic)
	}
	if len(in.Related) > 0 {
		b.WriteString("\nRelated issues (context only):\n")
		for _, r := range in.Related {
			fmt.Fprintf(&b, "\n%s: %s\n", r.Ref, r.Title)
			if d := clip(r.Description, maxStandInRelatedDesc); d != "" {
				b.WriteString(d + "\n")
			}
		}
	}
	return Job{
		Feature: "standin", Role: RoleExplain, PromptVersion: standInVersion,
		System: standInSystem, Prompt: b.String(),
		Forge: forgeName, Ref: is.Ref, Name: fmt.Sprintf("standin-issue%d", is.Ref.IID),
		MaxTokens: 350,
	}, nil
}

// StandInLabel heads a stand-in wherever it is shown, so it is never taken
// for the issue's own description.
const StandInLabel = "AI stand-in description"

// StandInNote says where a stand-in lives.
const StandInNote = "local only: written from the title, never posted to the forge; the issue's description is empty"
