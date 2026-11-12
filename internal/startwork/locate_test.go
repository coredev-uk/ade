package startwork_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/startwork"
)

func TestServingManifestPrefersTheItemsOwnProject(t *testing.T) {
	h := newHarness(t, fake.Free)
	dir := t.TempDir()
	var refs []config.ProjectRef
	for _, name := range []string{"first", "second"} {
		path := filepath.Join(dir, name+".ade.toml")
		if err := os.WriteFile(path, []byte(strings.Replace(manifestTOML, `name  = "demo-gl"`, `name  = "`+name+`"`, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, config.ProjectRef{Source: path})
	}
	h.app.Config.Projects = refs

	for _, tt := range []struct{ preferred, want string }{
		{"", "first"},
		{"second", "second"},
		{"first", "first"},
		{"unknown", "first"},
	} {
		m, err := startwork.ServingManifest(context.Background(), h.app, tt.preferred, apiProj)
		if err != nil || m.Name != tt.want {
			t.Errorf("preferred %q: got %v, %v; want %s", tt.preferred, m, err, tt.want)
		}
	}
	// Preferring a project that does not serve the path falls back to the one that does.
	if m, err := startwork.ServingManifest(context.Background(), h.app, "second", "other/place"); err == nil {
		t.Errorf("nothing serves other/place, got %s", m.Name)
	}
}
