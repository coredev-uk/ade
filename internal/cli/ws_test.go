package cli

import (
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/workspace"
)

func TestKeyValues(t *testing.T) {
	got, err := keyValues("--with", []string{"api=feature/x", "lib=/abs/path"})
	if err != nil || got["api"] != "feature/x" || got["lib"] != "/abs/path" {
		t.Fatalf("got %v %v", got, err)
	}
	if m, err := keyValues("--with", nil); m != nil || err != nil {
		t.Fatalf("no flags: %v %v", m, err)
	}
	for _, bad := range []string{"api", "=x", "api=", ""} {
		if _, err := keyValues("--with", []string{bad}); err == nil || !strings.Contains(err.Error(), "want repo=value") {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

func TestServicesStrip(t *testing.T) {
	got := servicesStrip([]workspace.Result{
		{Node: "api.check", Outcome: workspace.OutcomeReady},
		{Node: "api.dev", Port: 54031, Outcome: workspace.OutcomeReady, Shared: true},
		{Node: "web.dev", Port: 54044, Outcome: workspace.OutcomeReady},
	})
	want := "services: api.check ready · api.dev ready 54031 (shared) · web.dev ready 54044"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
