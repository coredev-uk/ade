package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/stagemove"
)

// openStageMove is Home's `m`: bring the selected item's workflow label in
// line with its stage. Under labels.workflow.sync = "ask" (the default) a sync
// only reports that the label is behind; this makes the move once the dialog
// has shown it (add this label, remove that one), audited as
// forge.issue.labels like `ade issue stage`.
func (m model) openStageMove() (model, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok || r.item == nil {
		return m, nil
	}
	ref, results, a, open := r.item.Ref, m.results, m.app, m.writerOpen()
	return m.openAction(actionSpec{
		title:   "Move the workflow label · " + ref.String(),
		heading: r.item.Issue.Title,
		reading: "reading the issue…",
		retry:   "m",
		resolve: func(ctx context.Context) (actionPlan, error) {
			t, err := stagemove.Resolve(ctx, a, open, results, ref)
			if err != nil {
				return actionPlan{}, err
			}
			if !t.Has {
				return actionPlan{nothing: true, lines: []string{"Nothing to move: " + t.Why + "."}}, nil
			}
			mv := t.Move
			from := mv.From
			if from == "" {
				from = "none"
			}
			lines := []string{"workflow " + from + " → " + mv.To + " (the stage says " + t.Item.Stage.String() + ")"}
			if mv.Add != "" {
				lines = append(lines, "add     "+mv.Add)
			}
			for _, l := range mv.Remove {
				lines = append(lines, "remove  "+l)
			}
			return actionPlan{
				lines: lines, verb: "move",
				note: "ADE moves the label only; the board column follows it. ADE never closes issues.",
				apply: func(ctx context.Context) ([]string, error) {
					done, err := t.Apply(ctx, a)
					if err != nil {
						return nil, err
					}
					return []string{strings.TrimSpace(done.String())}, nil
				},
				after: func(m model) tea.Cmd { return m.refreshItem(t.Manifest, t.Forge, t.Item.Ref) },
			}, nil
		},
	})
}
