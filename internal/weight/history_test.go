package weight_test

import (
	"context"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/weight"
)

// The closed-issue history is several pages of the forge's list: a dialog
// gets whatever the cache has at once and the forge is read in the
// background, so the next dialog has it.
func TestHistorySamplesNeverWaitForTheForge(t *testing.T) {
	// A slow forge (2 s) and a 1 s bound: far apart, so a busy CI runner can't
	// turn "answered from the cache" into a false wait.
	srv := fake.New(fake.Premium, fake.WithLatency(2*time.Second))
	t.Cleanup(srv.Close)
	a := &app.App{Paths: paths.Paths{CacheDir: t.TempDir()}}
	t.Cleanup(func() { a.Close() })
	c := gitlab.New(srv.URL, fake.Token)
	m := &config.Manifest{Name: "demo", Forge: "gl", Tracker: config.Tracker{Project: tracker, Weight: config.WeightSource{Source: "auto"}}}
	ctx := context.Background()

	start := time.Now()
	if got := weight.HistorySamples(ctx, a, c, "gl", m); len(got) != 0 {
		t.Fatalf("first call returned %d samples from an empty cache", len(got))
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("first call took %v with a 2s forge: it waited", d)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		start = time.Now()
		got := weight.HistorySamples(ctx, a, c, "gl", m)
		if d := time.Since(start); d > time.Second {
			t.Fatalf("a call took %v with a 2s forge: it waited", d)
		}
		if len(got) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the background read never filled the cache")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
