package bootstrap

import (
	"testing"

	"github.com/coredev-uk/ade/internal/config"
)

// Forge-supplied text (a milestone or label value) reaches comments; a newline
// in it must not start a setting of its own.
func TestRenderKeepsHostileTextInComments(t *testing.T) {
	d := &Draft{
		Group: "g",
		Manifest: config.Manifest{
			Name: "demo", Forge: "work",
			Tracker: config.Tracker{
				Project:    "g/tracker",
				Iterations: config.IterationsSource{Source: "milestones"},
				Weight:     config.WeightSource{Source: "none"},
			},
		},
		TrackerWhy:    "named like a tracker\nname = \"evil\"",
		IterationsWhy: []string{"latest \"S\"\r\n[repos.evil]\npath = \"x\""},
		WorkflowWhy:   []string{"values found: a\u2028forge = \"evil\""},
	}
	d.Manifest.Labels.Workflow = config.WorkflowFacet{Pattern: "workflow::{value}", Sync: "auto"}
	m, err := config.ParseManifest([]byte(d.Render()), "x")
	if err != nil {
		t.Fatalf("hostile text broke the TOML: %v\n%s", err, d.Render())
	}
	if m.Name != "demo" || m.Forge != "work" || len(m.Repos) != 0 || len(m.Undecoded) != 0 {
		t.Errorf("manifest = %+v: forge text escaped its comment\n%s", m, d.Render())
	}
}

func TestQuoteEscapes(t *testing.T) {
	for _, s := range []string{`plain`, `a "quoted" word`, `back\slash`, "tab\there", "line\nbreak", "bell\a", "unicode é ✓", `C:\path\"x"`} {
		m, err := config.ParseManifest([]byte("name = "+quote(s)+"\n"), "x")
		if err != nil || m.Name != s {
			t.Errorf("quote(%q) round-trips to %q, %v", s, m.Name, err)
		}
	}
}
