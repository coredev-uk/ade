package ai

import (
	_ "embed"
	"fmt"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

const (
	standupVersion = "standup-v1"
	sprintVersion  = "sprint-review-v1"
	parentVersion  = "sprint-parent-v2"

	// StandupForge and SprintForge are the item-record roots of the agile
	// artifacts: items/_standup/<project>/<yyyymmdd>/ai/ and items/_sprint/....
	StandupForge = "_standup"
	SprintForge  = "_sprint"
)

var (
	//go:embed prompts/standup.md
	standupSystem string
	//go:embed prompts/sprint.md
	sprintSystem string
	//go:embed prompts/sprint-parent.md
	sprintParentSystem string
)

// dayKey is yyyymmdd as a record number.
func dayKey(t time.Time) int {
	y, m, d := t.Date()
	return y*10000 + int(m)*100 + d
}

// StandupJob turns the deterministic standup facts (Markdown) into a spoken
// summary. The facts are the whole input, so an unchanged day is a cache hit.
func StandupJob(project string, day time.Time, facts string) Job {
	return Job{
		Feature: "standup", Role: RoleExplain, PromptVersion: standupVersion,
		System: standupSystem, Prompt: facts,
		Forge: StandupForge, Ref: forge.Ref{Project: project, IID: dayKey(day)},
		Name: "standup-" + day.Format("2006-01-02"), MaxTokens: 400,
		Params: map[string]any{"day": day.Format("2006-01-02")},
	}
}

// SprintReviewJob turns the review facts (Markdown) into a narrative.
func SprintReviewJob(project, sprint string, start time.Time, facts string) Job {
	return Job{
		Feature: "sprint-review", Role: RoleExplain, PromptVersion: sprintVersion,
		System: sprintSystem, Prompt: facts,
		Forge: SprintForge, Ref: forge.Ref{Project: project, IID: dayKey(start)},
		Name: "review-" + fmt.Sprint(dayKey(start)), MaxTokens: 500,
		Params: map[string]any{"sprint": sprint},
	}
}

// SprintParentJob turns the facts of one parent of a sprint review (a tracker
// issue with the MRs merged for it, as Markdown) into a short paragraph for
// the review's Markdown export. It is filed under the parent's own number
// next to the sprint's artifacts.
func SprintParentJob(project, sprint string, parent forge.Ref, facts string) Job {
	return Job{
		Feature: "sprint-parent", Role: RoleExplain, PromptVersion: parentVersion,
		System: sprintParentSystem, Prompt: facts,
		Forge: SprintForge, Ref: forge.Ref{Project: project, IID: parent.IID},
		Name: fmt.Sprintf("parent-%d", parent.IID), MaxTokens: 600,
		Params: map[string]any{"sprint": sprint, "parent": parent.String()},
	}
}
