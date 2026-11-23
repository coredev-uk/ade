package forge

import (
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/aimark"
)

// QuickActions lists the quick-action commands (/close, /assign, /label …)
// GitLab may run when it saves text: the lines aimark.NoQuickActions would
// escape (indented too, code fences included: no reading of fences is right
// for every GitLab version). A person writing a description by hand may mean
// them; they are never stripped, only named so a write can warn first. Each
// command appears once, in order.
func QuickActions(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if cmd, ok := aimark.QuickActionName(line); ok && !slices.Contains(out, cmd) {
			out = append(out, cmd)
		}
	}
	return out
}
