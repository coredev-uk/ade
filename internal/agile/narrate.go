package agile

import (
	"context"
	"fmt"
	"slices"
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

// NarrateParent writes the narrative of one parent (a tracker issue and the MRs
// merged for it) from its own facts.
func (n *AINarrator) NarrateParent(ctx context.Context, r Review, it ReviewItem) (string, error) {
	return n.run(ctx, ai.SprintParentJob(r.Project, r.Sprint.Title, it.Ref, parentFacts(r, it)))
}

// parentFacts is the deterministic input of a parent's narrative: the item
// line with its cycle time, and the sprint it belongs to.
func parentFacts(r Review, it ReviewItem) string {
	basis := "by item count"
	if r.Basis == BasisWeight {
		basis = "by weight"
	}
	return fmt.Sprintf("Sprint %s (%s – %s), measured %s.\n\n%s\n", r.Sprint.Title, r.Sprint.Start, r.Sprint.Due, basis, itemLine(it))
}

// ParentNarrator writes the narrative of one parent of a sprint review.
type ParentNarrator interface {
	NarrateParent(ctx context.Context, r Review, it ReviewItem) (string, error)
}

// ParentNarrated says which parents get a narrative: done, with merged MRs
// (what was delivered is what there is to narrate).
func ParentNarrated(it ReviewItem) bool { return it.Done && len(it.MRs) > 0 }

// WithParentNarratives attaches a narrative to every parent that has merged MRs
// (PLAN 6.4, the Markdown export). Calls are cached by content and capped like
// any other, so the first failure (a budget stop, AI off) ends the run: what
// was written stays, and the failure is returned with the count done.
func WithParentNarratives(ctx context.Context, n ParentNarrator, r Review) (Review, int, error) {
	if n == nil {
		return r, 0, nil
	}
	items := slices.Clone(r.Items)
	r.Items = items
	done := 0
	for i := range items {
		if !ParentNarrated(items[i]) {
			continue
		}
		text, err := n.NarrateParent(ctx, r, items[i])
		if err != nil {
			return r, done, fmt.Errorf("%s: %w", short(items[i].Ref), err)
		}
		items[i].Narrative = strings.TrimSpace(text)
		done++
	}
	return r, done, nil
}
