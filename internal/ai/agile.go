package ai

import (
	_ "embed"
	"fmt"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

const (
	standupVersion = "standup-v3"
	sprintVersion  = "sprint-review-v5"
	retroVersion   = "sprint-retro-v2"
	parentVersion  = "sprint-parent-v3"
	lastDayVersion = "lastday-v2"

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
	//go:embed prompts/sprint-mine.md
	retroSystem string
	//go:embed prompts/sprint-parent.md
	sprintParentSystem string
	//go:embed prompts/lastday.md
	lastDaySystem string
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
		System: guarded(standupSystem), Prompt: untrusted("facts", facts),
		Forge: StandupForge, Ref: forge.Ref{Project: project, IID: dayKey(day)},
		Name: "standup-" + day.Format("2006-01-02"), MaxTokens: 400,
		Params: map[string]any{"day": day.Format("2006-01-02")},
	}
}

// LastDayJob turns the facts of your last working day (Markdown) into a short
// structured summary for the dashboard. It is filed next to that day's standup.
func LastDayJob(day time.Time, facts string) Job {
	return Job{
		Feature: "lastday", Role: RoleExplain, PromptVersion: lastDayVersion,
		System: guarded(lastDaySystem), Prompt: untrusted("facts", facts),
		Forge: StandupForge, Ref: forge.Ref{Project: "all", IID: dayKey(day)},
		Name: "lastday-" + day.Format("2006-01-02"), MaxTokens: 500,
		Params: map[string]any{"day": day.Format("2006-01-02")},
	}
}

// SprintReviewJob turns the review facts (Markdown) into a structured retro:
// the team's, or with mine the personal one of your own items.
func SprintReviewJob(project, sprint string, start time.Time, mine bool, facts string) Job {
	j := Job{
		Feature: "sprint-review", Role: RoleExplain, PromptVersion: sprintVersion,
		System: guarded(sprintSystem), Prompt: untrusted("facts", facts),
		Forge: SprintForge, Ref: forge.Ref{Project: project, IID: dayKey(start)},
		Name: "review-" + fmt.Sprint(dayKey(start)), MaxTokens: 800,
		Params: map[string]any{"sprint": sprint},
	}
	if mine {
		j.PromptVersion, j.System, j.Name = retroVersion, guarded(retroSystem), "retro-"+fmt.Sprint(dayKey(start))
		j.Params["mine"] = true
	}
	return j
}

// SprintParentJob turns the facts of one parent of a sprint review (a tracker
// issue with the MRs merged for it, as Markdown) into a short paragraph for
// the review's Markdown export. It is filed under the parent's own number
// next to the sprint's artifacts.
func SprintParentJob(project, sprint string, parent forge.Ref, facts string) Job {
	return Job{
		Feature: "sprint-parent", Role: RoleExplain, PromptVersion: parentVersion,
		System: guarded(sprintParentSystem), Prompt: untrusted("facts", facts),
		Forge: SprintForge, Ref: forge.Ref{Project: project, IID: parent.IID},
		Name: fmt.Sprintf("parent-%d", parent.IID), MaxTokens: 600,
		Params: map[string]any{"sprint": sprint, "parent": parent.String()},
	}
}
