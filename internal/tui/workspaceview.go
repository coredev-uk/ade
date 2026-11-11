package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
)

// serviceLines is the Services strip of an item's preview (PLAN 2.1):
// "services  api.dev ● 54031 (feature tree) · web.dev ● 54044". Every state
// is a glyph plus a word or number, so it reads without colour. Nothing is
// drawn when none of the item's workspaces runs anything.
func (m model) serviceLines(ref string, w int) []string {
	chips := attentionFirst(m.ws.chips[ref])
	if len(chips) == 0 {
		return nil
	}
	g := m.g
	parts := make([]string, 0, len(chips))
	for _, c := range chips {
		var s string
		switch c.state {
		case "starting":
			s = c.node + " " + stWarn.Render(g.Ahead) + " starting"
		case "stopped":
			s = c.node + " " + stBad.Render(g.Bad) + " stopped"
		case "blocked":
			s = c.node + " " + stWarn.Render(g.Warn) + " blocked by " + c.blocker
		case "failed":
			s = c.node + " " + stBad.Render(g.Bad) + " failed"
		default:
			s = c.node + " " + stOK.Render(g.Done)
			if c.port > 0 {
				s += fmt.Sprintf(" %d", c.port)
			}
		}
		var tags []string
		if c.feature {
			tags = append(tags, "feature tree")
		}
		if c.shared {
			tags = append(tags, "shared")
		}
		if len(tags) > 0 {
			s += stFaint.Render(" (" + strings.Join(tags, ", ") + ")")
		}
		parts = append(parts, s)
	}
	// Chips never break across lines: fill lines greedily, continuation lines
	// aligned under the first chip. Chips that need attention come first and a
	// ready chip never shares their line, so a failure is not buried in a block.
	const label = "services  "
	sep := stFaint.Render(" " + g.Sep + " ")
	indent := strings.Repeat(" ", len(label))
	line := stFaint.Render(label)
	var lines []string
	for i, p := range parts {
		switch {
		case i == 0:
			line += p
		case lipgloss.Width(line)+lipgloss.Width(sep)+lipgloss.Width(p) > w,
			chips[i-1].state != "ready" && chips[i].state == "ready":
			lines = append(lines, line)
			line = indent + p
		default:
			line += sep + p
		}
	}
	return append(lines, line)
}

// attentionFirst orders chips by what needs a look: failed, blocked, stopped,
// starting, then ready. The order within a rank is the order they started in.
func attentionFirst(chips []svcChip) []svcChip {
	rank := map[string]int{"failed": 0, "blocked": 1, "stopped": 2, "starting": 3}
	out := slices.Clone(chips)
	slices.SortStableFunc(out, func(a, b svcChip) int {
		ra, ok := rank[a.state]
		if !ok {
			ra = 4
		}
		rb, ok := rank[b.state]
		if !ok {
			rb = 4
		}
		return ra - rb
	})
	return out
}
