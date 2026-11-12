package startwork_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/store"
)

func TestCachedCapsInKeepsAnswersButNotAnUnsampledUserNamespace(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		caps       forge.Caps
		wantProbes int
	}{
		{"a group is cached", forge.Caps{Iterations: true}, 1},
		{"a user namespace with weight is cached", forge.Caps{UserNamespace: true, Weight: true}, 1},
		{"a user namespace without weight is probed again", forge.Caps{UserNamespace: true}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "cache.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.DB.Close() })
			probes := 0
			fetch := func() (forge.Caps, error) { probes++; return tt.caps, nil }
			for range 2 {
				got, err := startwork.CachedCapsIn(st, "f", "g", now, fetch)
				if err != nil || got != tt.caps {
					t.Fatalf("caps = %+v, %v; want %+v", got, err, tt.caps)
				}
			}
			if probes != tt.wantProbes {
				t.Errorf("probed %d times, want %d", probes, tt.wantProbes)
			}
		})
	}
}
