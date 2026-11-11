package agile

import (
	"context"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/ai"
)

// Runner runs an AI job; *ai.Service is one.
type Runner interface {
	Run(ctx context.Context, j ai.Job) (ai.Result, error)
}

// AINarrator writes standup and sprint-review narratives with the AI service:
// cached by content, audited, capped, and refused when ai.mode is off. Tune
// sets per-call options (profile, refresh, confirmation) on every job. Last
// holds the most recent result, for cost lines.
type AINarrator struct {
	Svc  Runner
	Tune func(*ai.Job)
	Last ai.Result
}

func (n *AINarrator) run(ctx context.Context, j ai.Job) (string, error) {
	if n.Tune != nil {
		n.Tune(&j)
	}
	res, err := n.Svc.Run(ctx, j)
	n.Last = res
	return res.Text, err
}

// Narrate implements Narrator. The facts are the standup without any
// narrative, as Markdown.
func (n *AINarrator) Narrate(ctx context.Context, s Standup) (string, error) {
	s.Narrative, s.Warnings = "", nil
	day, _ := time.Parse("2006-01-02", s.Date)
	return n.run(ctx, ai.StandupJob("all", day, RenderStandup(s, Markdown)))
}

// NarrateReview writes the sprint review narrative.
func (n *AINarrator) NarrateReview(ctx context.Context, r Review) (string, error) {
	r.Narrative, r.Warnings = "", nil
	return n.run(ctx, ai.SprintReviewJob(r.Project, r.Sprint.Title, r.Sprint.Start.Time, RenderReview(r, Markdown)))
}

// ReviewNarrator is the sprint-review counterpart of Narrator.
type ReviewNarrator interface {
	NarrateReview(ctx context.Context, r Review) (string, error)
}

// WithReviewNarrative returns r with the narrator's text attached; a nil
// narrator or an error leaves r as it was.
func WithReviewNarrative(ctx context.Context, n ReviewNarrator, r Review) (Review, error) {
	if n == nil {
		return r, nil
	}
	text, err := n.NarrateReview(ctx, r)
	if err != nil {
		return r, err
	}
	r.Narrative = strings.TrimSpace(text)
	return r, nil
}
