package forge

import (
	"slices"
	"testing"
)

func TestQuickActionsNamesTheCommandsGitLabWouldRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{"none", "## What\nA token bucket.\n\nSee /var/log for details", nil},
		{"commands once, in order", "Closes #1\n/assign @rae\n/label ~bug\n/assign @sam\n/close", []string{"/assign", "/label", "/close"}},
		{"inside a fence", "```\n/close\n```\n/ready", []string{"/ready"}},
		{"a path is not a command", "/Users/me/x\n/ not a command\n//comment", nil},
		{"indented is text", "  /close", nil},
	} {
		if got := QuickActions(tc.text); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
