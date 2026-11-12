package weight

import (
	"context"
	"sync"
	"time"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/store"
)

const (
	// historyFresh is how long the closed-issue history is taken as current.
	historyFresh = 6 * time.Hour
	// historyStale is how old a history may be and still be better than none
	// for a dialog that cannot wait for a new one.
	historyStale = 30 * 24 * time.Hour
)

var historyReading sync.Map // cache key → struct{}: reads in flight

// HistorySamples returns the closed issues of the tracker project that say
// what work weighs (ai.ClosedSamples: a year of them, several pages of the
// forge's list) as the cache holds them, and never waits for the forge: with
// no fresh copy it starts one read in the background (one per project at a
// time, as background work) and answers with the older copy, or nothing the
// first time. A dialog's suggestion is better with the history and not worth
// waiting for it.
func HistorySamples(ctx context.Context, a *app.App, f forge.Forge, forgeName string, m *config.Manifest) []ai.WeightSample {
	st, err := a.Cache()
	if err != nil {
		return nil
	}
	key := "weight:history:" + forgeName + ":" + m.Tracker.Project
	now := time.Now()
	if v, ok := store.Peek[[]ai.WeightSample](st, key, historyFresh, now); ok {
		return v
	}
	stale, _ := store.Peek[[]ai.WeightSample](st, key, historyStale, now)
	if _, busy := historyReading.LoadOrStore(key, struct{}{}); !busy {
		go func() {
			defer historyReading.Delete(key)
			hist, err := ai.ClosedSamples(forge.Background(ctx), f, m.Tracker.Project, m.Labels, m.Tracker.Weight, time.Now())
			if err == nil {
				store.Put(st, key, time.Now(), hist)
			}
		}()
	}
	return stale
}
