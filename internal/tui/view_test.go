package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/project"
)

// The selected tree must stay on screen however far down the list it is.
func TestLinesKeepsSelectionVisible(t *testing.T) {
	var repos []project.RepoStatus
	for i := range 40 {
		repos = append(repos, project.RepoStatus{
			Name: fmt.Sprintf("repo%02d", i), Present: true, InManifest: true,
			Baseline: &project.TreeStatus{Name: "main", Branch: "main", HasUpstream: true},
		})
	}
	ps := []project.ProjectStatus{{Name: "p", Repos: repos}}
	m := model{projects: ps, rows: flatten(ps), g: unicodeGlyphs}
	for _, sel := range []int{0, 15, 39} {
		m.sel = sel
		out := ansi.Strip(m.lines(80, 10))
		if got := len(strings.Split(out, "\n")); got != 10 {
			t.Fatalf("sel %d: %d lines, want 10", sel, got)
		}
		want := fmt.Sprintf("repo%02d", sel)
		if !strings.Contains(out, want) {
			t.Errorf("sel %d: %s not visible in\n%s", sel, want, out)
		}
	}
}
