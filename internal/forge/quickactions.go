package forge

import (
	"regexp"
	"slices"
	"strings"
)

var quickActionLine = regexp.MustCompile(`^/[a-z_]+(?:\s|$)`)

// QuickActions lists the quick-action commands (/close, /assign, /label …)
// GitLab would run when it saves text: lines that start with a slash and a
// lowercase command name, outside fenced code. A person writing a description
// by hand may mean them; they are never stripped, only named so a write can
// warn first. Each command appears once, in order.
func QuickActions(text string) []string {
	var out []string
	fenced := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced || !quickActionLine.MatchString(line) {
			continue
		}
		cmd, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		if !slices.Contains(out, cmd) {
			out = append(out, cmd)
		}
	}
	return out
}
