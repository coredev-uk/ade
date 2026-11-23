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
		{"inside a fence", "```\n/close\n```\n/ready", []string{"/close", "/ready"}},
		{"not a command", "/ not a command\n//comment\n", nil},
		{"indented", "  /close\n\t/label ~bug", []string{"/close", "/label"}},
		{"same rule as aimark", "/Close\n/spend_time 1h", []string{"/Close", "/spend_time"}},
	} {
		if got := QuickActions(tc.text); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
